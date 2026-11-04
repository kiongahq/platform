#!/usr/bin/env python3
"""Drives Agent Teams end to end through the real HTTP API. Start it from
scripts/agent-teams-smoke.sh, which builds the binaries.

Every scenario makes hard assertions and prints what it saw. Model decisions come from
`[[mock-script]]` on the mock backend, so a worker "reporting" something is scripted;
what is real is everything the gateway, runner and sandbox manager do with it.

Exit status: 0 all ran and passed, 1 something failed, 3 nothing failed but a scenario
did not run. A subset or a run with no engine can never report success.
"""

from __future__ import annotations

import json
import os
import re
import secrets
import shutil
import sys
import time
import traceback
import urllib.request
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable

sys.path.insert(0, str(Path(__file__).resolve().parent))
from agent_teams_smoke_lib import (  # noqa: E402
    Api,
    ApiError,
    CountingBackend,
    GitServer,
    Process,
    RunnerRecorder,
    call,
    finish,
    note,
    run,
    script,
    unframe,
    wait_for,
)

import jsonschema  # noqa: E402

TERMINAL = {"succeeded", "failed", "cancelled"}
FULL_LIMITS = {"max_turns": 30, "max_tokens": 500000, "max_seconds": 900}
GIT_IDENTITY = "git -c user.name=kionga-smoke -c user.email=smoke@example.invalid"
QDRANT_IMAGE = "qdrant/qdrant@sha256:0fb8897412abc81d1c0430a899b9a81eb8328aa634e7242d1bc804c1fe8fe863"

NOT_EXERCISED = [
    "dataset tools: they need an object store and a registered dataset, and no scenario grants one",
    "a real function runtime: scenario 11 runs registered `tool` grants against the counting backend in place of OpenFaaS",
    "the Kubernetes sandbox driver, and Podman: this runs Docker only",
    "several gateway replicas: one gateway process at a time, restarted",
    "a real model provider: the runner uses MLAIOPS_LLM_BACKEND=mock, so model decisions are scripted",
    "trace-proxy: the mock backend makes no LLM call to send through it",
    "the console",
]
ALLOWANCE = ("Git helpers reach a local HTTPS server on a private address only because "
             "KIONGA_SANDBOX_GIT_ALLOW_PRIVATE_FOR_TESTS=1 is set and KIONGA_ENVIRONMENT=production is not. "
             "A production manager refuses to start with that allowance, and refuses private git hosts. "
             "The server's CA is passed by KIONGA_SANDBOX_GIT_CA_FILE. No other isolation setting is relaxed.")


def parse_time(value: str) -> datetime:
    # Go writes nanoseconds, which datetime does not read before Python 3.11's limit of six digits.
    match = re.match(r"^(.*?T\d\d:\d\d:\d\d)(\.\d+)?(Z|[+-]\d\d:\d\d)$", value)
    if not match:
        raise AssertionError(f"not an RFC 3339 time: {value!r}")
    fraction = (match.group(2) or ".0")[:7]
    zone = "+00:00" if match.group(3) == "Z" else match.group(3)
    return datetime.fromisoformat(match.group(1) + fraction + zone)


def check(condition: Any, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def walk_keys(value: Any) -> set[str]:
    keys: set[str] = set()
    if isinstance(value, dict):
        for key, item in value.items():
            keys.add(key)
            keys |= walk_keys(item)
    elif isinstance(value, list):
        for item in value:
            keys |= walk_keys(item)
    return keys


class Smoke:
    def __init__(self) -> None:
        self.work = Path(os.environ["KIONGA_SMOKE_WORK"])
        self.contracts = Path(os.environ["KIONGA_SMOKE_CONTRACTS"])
        self.runtime_dir = os.environ["AGENT_RUNTIME_DIR"]
        self.engine = os.environ.get("KIONGA_SMOKE_NO_ENGINE") != "1"
        self.can_sudo = os.environ.get("KIONGA_SMOKE_CAN_SUDO") == "1"
        self.image = os.environ.get("KIONGA_SMOKE_IMAGE", "")
        self.address = os.environ.get("KIONGA_SMOKE_GIT_ADDRESS", "127.0.0.1")
        self.gateway_port = int(os.environ.get("KIONGA_SMOKE_GATEWAY_PORT", "18480"))
        self.runner_port = int(os.environ.get("KIONGA_SMOKE_RUNNER_PORT", "19500"))
        self.manager_port = int(os.environ.get("KIONGA_SMOKE_MANAGER_PORT", "18488"))
        self.prefix = "ksmoke" + secrets.token_hex(3) + "-"
        self.runner_token = "smoke-runner-" + secrets.token_hex(16)
        self.manager_token = "smoke-manager-" + secrets.token_hex(16)
        self.git_secret = "-".join(["kionga", "smoke", secrets.token_hex(16)])
        self.password = "smoke-" + secrets.token_hex(12)
        self.origin = f"http://127.0.0.1:{self.gateway_port}"
        self.api = Api(self.origin, "admin", self.password)
        self.evidence: list[str] = []
        self.runs: list[str] = []
        self.api_documents: list[tuple[str, Any]] = []
        self.counter = 0
        self.git = GitServer(self.work / "git", self.address, self.git_secret)
        self.backend = CountingBackend()
        self.runner_url = f"http://127.0.0.1:{self.runner_port}"
        self.gateway_runner_url = self.runner_url
        self.git_connection = ""
        self.project = ""
        base = {"PATH": os.environ["PATH"], "HOME": str(self.work), "TMPDIR": os.environ.get("TMPDIR", "/tmp")}
        self.runner = Process("runner", [sys.executable, "-m", "agent_runtime.designer"], {
            **base, "KIONGA_DESIGNER_RUNNER_TOKEN": self.runner_token, "KIONGA_DESIGNER_PORT": str(self.runner_port),
            "KIONGA_DESIGNER_HOST": "127.0.0.1", "MLAIOPS_LLM_BACKEND": "mock", "PYTHONUNBUFFERED": "1",
        }, self.work / "runner.log", cwd=self.runtime_dir)
        self.manager = Process("sandbox-manager", [str(self.work / "sandbox-manager")], {
            **base, "KIONGA_SANDBOX_TOKEN": self.manager_token, "PORT": str(self.manager_port),
            "KIONGA_SANDBOX_DRIVER": "docker", "KIONGA_SANDBOX_RUNTIME": "runsc", "KIONGA_SANDBOX_IMAGES": self.image,
            "KIONGA_SANDBOX_NAME_PREFIX": self.prefix, "KIONGA_SANDBOX_GIT_HOSTS": self.git.host,
            "KIONGA_SANDBOX_GIT_CA_FILE": str(self.git.ca_file), "KIONGA_SANDBOX_GIT_ALLOW_PRIVATE_FOR_TESTS": "1",
        }, self.work / "manager.log")
        self.gateway = self._gateway_process(base)

    def _gateway_process(self, base: dict[str, str]) -> Process:
        env = {
            **base, "PORT": str(self.gateway_port), "MLAIOPS_LOCAL_ROLE": "admin", "MLAIOPS_LOCAL_USERNAME": "admin",
            "MLAIOPS_LOCAL_PASSWORD": self.password,
            "KIONGA_AGENT_DESIGNER_RUNNER_TOKEN": self.runner_token,
            "KIONGA_AGENT_DESIGNER_CALLBACK_URL": "http://127.0.0.1:8087/api/v1",
            "KIONGA_SANDBOX_URL": f"http://127.0.0.1:{self.manager_port}", "KIONGA_SANDBOX_TOKEN": self.manager_token,
            "KIONGA_CONNECTION_SECRET_SMOKE_GIT": self.git_secret,
            # Short, so scenario 7 can watch a real delete. Long enough that nothing
            # else in a run sees its sandbox go.
            "KIONGA_TEAM_SANDBOX_RETENTION_SECONDS": "30",
            # The function runtime is outside Kionga, so the counting backend stands in.
            "OPENFAAS_URL": self.backend.url, "OPENFAAS_PASSWORD": "smoke-" + secrets.token_hex(8),
        }
        if os.environ.get("DATABASE_URL"):
            env["DATABASE_URL"] = os.environ["DATABASE_URL"]
        else:
            env["MLAIOPS_DATA_PATH"] = str(self.work / "platform.json")
        env["KIONGA_AGENT_DESIGNER_RUNNER_URL"] = self.gateway_runner_url
        return Process("gateway", [str(self.work / "gateway")], env, self.work / "gateway.log", cwd=str(self.work))

    # --- Process control ---

    def start_runner(self) -> None:
        self.runner.start()
        wait_for("runner /healthz", lambda: self._ok(self.runner_url + "/healthz"), 60)

    def start_gateway(self) -> None:
        self.gateway.env["KIONGA_AGENT_DESIGNER_RUNNER_URL"] = self.gateway_runner_url
        self.gateway.start()
        wait_for("gateway /health", lambda: self._ok(self.origin + "/api/v1/health"), 60)

    def start_manager(self, verified: bool = True) -> None:
        self.manager.start()
        url = f"http://127.0.0.1:{self.manager_port}/healthz"
        wait_for("manager /healthz", lambda: self._ok(url), 180)
        if verified:
            wait_for("manager to verify gVisor", lambda: self.manager_health().get("runtime_verified") is True, 180)

    def manager_health(self) -> dict[str, Any]:
        with urllib.request.urlopen(f"http://127.0.0.1:{self.manager_port}/healthz", timeout=10) as response:  # noqa: S310 -- local
            return json.loads(response.read())

    @staticmethod
    def _ok(url: str) -> bool:
        with urllib.request.urlopen(url, timeout=5) as response:  # noqa: S310 -- local
            return response.status == 200

    def ensure_up(self) -> None:
        if not self.runner.alive:
            self.start_runner()
        if self.engine and not self.manager.alive:
            self.start_manager()
        if not self.gateway.alive:
            self.start_gateway()

    def stop_all(self) -> None:
        for process in (self.gateway, self.manager, self.runner):
            process.stop()

    def remove_engine_objects(self) -> None:
        """Only objects under this run's random prefix."""
        if not self.engine or not shutil.which("docker"):
            return
        label = f"label=kionga.dev/sandbox-prefix={self.prefix.rstrip('-')}"
        containers = run(["docker", "ps", "-aq", "--filter", label], check=False).split()
        if containers:
            run(["docker", "rm", "-f", *containers], check=False)
        volumes = run(["docker", "volume", "ls", "-q", "--filter", label], check=False).split()
        if volumes:
            run(["docker", "volume", "rm", "-f", *volumes], check=False)
        run(["docker", "rm", "-f", self.prefix + "qdrant"], check=False)

    # --- Evidence ---

    def say(self, text: str) -> None:
        note(text)
        self.evidence.append(text)

    # --- Resources ---

    def name(self, stem: str) -> str:
        self.counter += 1
        return f"{stem}-{self.counter}"

    def record(self, label: str, document: Any) -> Any:
        self.api_documents.append((label, document))
        return document

    def define(self, name: str, tools: list[dict[str, Any]] | None = None, workspace: str = "none", limits: dict[str, int] | None = None) -> str:
        body = {"project_id": self.project, "name": name, "description": f"Smoke agent {name}.",
                "instructions": "Follow the script.", "model": "llm", "tools": tools or [], "workspace": workspace,
                "limits": limits or FULL_LIMITS}
        created = self.record("definition", self.api.post("/agent-definitions", body, expect=(200, 201)))
        check(re.fullmatch(r"adf-[a-f0-9]{24}", created["id"]), f"definition id {created['id']}")
        return created["id"]

    def team(self, *member_ids: str) -> str:
        body = {"project_id": self.project, "name": self.name("team"), "description": "Smoke team.",
                "coordinator": {"instructions": "Coordinate.", "model": "llm"}, "members": list(member_ids),
                "brief": "Be brief.", "limits": {"max_workers_total": 8, "max_concurrent_workers": 4, "max_tokens": 2000000, "max_seconds": 1800}}
        created = self.record("team", self.api.post("/agent-teams", body, expect=(200, 201)))
        check(re.fullmatch(r"atm-[a-f0-9]{24}", created["id"]), f"team id {created['id']}")
        return created["id"]

    def sandbox_tools(self, repository: str | None = None) -> list[dict[str, Any]]:
        tools: list[dict[str, Any]] = [{"kind": "sandbox"}]
        if repository:
            tools.append({"kind": "git", "repository": repository, "connection": self.git_connection})
        return tools

    def connection(self, path: str) -> tuple[str, str]:
        """A real API connection to the counting backend. Returns its ID and tool name."""
        created = self.api.post("/admin/agent-api-connections", {"project_id": self.project, "name": self.name("backend"),
                                "endpoint": self.backend.url + path, "secret_ref": "none"}, expect=(200, 201))
        return created["id"], "connection_" + created["id"]

    # --- Runs ---

    def start(self, team_id: str, text: str, allow: bool = False) -> str:
        created = self.api.post("/team-runs", {"team_id": team_id, "input": text, "allow_side_effects": allow}, expect=202)
        check(re.fullmatch(r"atr-[a-f0-9]{24}", created["id"]), f"run id {created['id']}")
        self.runs.append(created["id"])
        return created["id"]

    def run_view(self, run_id: str) -> dict[str, Any]:
        return self.api.get(f"/team-runs/{run_id}")

    def wait_run(self, run_id: str, status: str = "succeeded", timeout: float = 240) -> dict[str, Any]:
        """Waits until the run has settled, then insists on the status."""
        def settled() -> Any:
            view = self.run_view(run_id)
            return view if view["status"] in TERMINAL and view.get("settled_at") else None
        try:
            final = wait_for(f"run {run_id} to settle", settled, timeout, every=0.5)
        except AssertionError:
            self.dump(run_id)
            raise
        if final["status"] != status:
            self.dump(run_id)
            raise AssertionError(f"run {run_id} ended {final['status']}, wanted {status}: {final.get('error')} {final.get('error_message')}")
        return final

    def dump(self, run_id: str) -> None:
        view = self.run_view(run_id)
        note(f"run {run_id}: status={view['status']} error={view.get('error')} resume_count={view.get('resume_count')}")
        for item in self.api.get(f"/team-runs/{run_id}/workers")["items"]:
            note(f"  worker {item['agent']} {item['id']} status={item['status']} last_error={item.get('last_error')} {item.get('last_error_message', '')} turns={item.get('turns')} lease={item.get('lease_expires_at')}")
        for event in self.events(run_id)[-25:]:
            note("  event " + json.dumps({k: v for k, v in event.items() if k not in ("run_id", "ts")})[:400])

    def events(self, run_id: str) -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        after = 0
        while True:
            page = self.api.get(f"/team-runs/{run_id}/events?after={after}")
            if not page["events"]:
                return out
            out.extend(page["events"])
            after = page["events"][-1]["seq"]
            if page["done"]:
                return out

    def workers(self, run_id: str) -> list[dict[str, Any]]:
        return self.api.get(f"/team-runs/{run_id}/workers")["items"]

    def worker(self, run_id: str, agent: str) -> dict[str, Any]:
        matches = [item for item in self.workers(run_id) if item["agent"] == agent]
        check(len(matches) == 1, f"run {run_id} has {len(matches)} workers named {agent}")
        return self.api.get(f"/team-runs/{run_id}/workers/{matches[0]['id']}")

    def coordinator(self, run_id: str) -> dict[str, Any]:
        view = self.run_view(run_id)
        return self.api.get(f"/team-runs/{run_id}/workers/{view['coordinator_id']}")

    def wait_worker(self, run_id: str, agent: str, statuses: set[str], timeout: float = 120) -> dict[str, Any]:
        def probe() -> Any:
            for item in self.workers(run_id):
                if item["agent"] == agent and item["status"] in statuses:
                    return self.worker(run_id, agent)
            return None
        try:
            return wait_for(f"worker {agent} to be {sorted(statuses)}", probe, timeout, every=0.3)
        except AssertionError:
            self.dump(run_id)
            raise

    def wait_event(self, run_id: str, predicate: Callable[[dict[str, Any]], bool], what: str, timeout: float = 120) -> dict[str, Any]:
        def probe() -> Any:
            return next((event for event in self.events(run_id) if predicate(event)), None)
        try:
            return wait_for(f"event {what}", probe, timeout, every=0.3)
        except AssertionError:
            self.dump(run_id)
            raise

    def wait_tool_started(self, run_id: str, tool: str, timeout: float = 120) -> dict[str, Any]:
        return self.wait_event(run_id, lambda e: e["type"] == "worker.tool_call" and e.get("tool") == tool, f"tool_call {tool}", timeout)

    def approval_for(self, run_id: str, agent: str, tool: str | None = None) -> str:
        """Waits for a parked call, checks the approvals API shows it, returns its ID."""
        worker = self.wait_worker(run_id, agent, {"waiting_approval"})
        event = self.wait_event(run_id, lambda e: e["type"] == "worker.waiting_approval" and e.get("worker_id") == worker["id"] and (tool is None or e.get("tool") == tool), "waiting_approval")
        approval = event["approval_id"]
        pending = self.api.get(f"/agent-approvals?status=pending&run_id={run_id}")["items"]
        check(any(item["id"] == approval for item in pending), f"approval {approval} is not listed as pending: {pending}")
        check(self.run_view(run_id)["status"] == "running", "a run waiting on approval must stay running")
        return approval

    def decide(self, approval: str, decision: str, comment: str = "smoke") -> None:
        done = self.api.post(f"/agent-approvals/{approval}/decision", {"decision": decision, "comment": comment})
        wanted = "approved" if decision == "approve" else "rejected"
        check(done["status"] == wanted, f"decision returned {done}")

    @staticmethod
    def tool_results(worker: dict[str, Any], name: str | None = None) -> list[dict[str, Any]]:
        """Each settled call with its result text, in order, from a worker's transcript."""
        calls = {tool["id"]: tool for message in worker["transcript"] for tool in message.get("tool_calls") or []}
        out = []
        for message in worker["transcript"]:
            if message["role"] == "tool":
                tool = calls[message["tool_call_id"]]
                if name is None or tool["name"] == name:
                    out.append({"id": tool["id"], "name": tool["name"], "status": tool["status"], "content": unframe(message["content"]), "arguments": tool["arguments"]})
        return out

    def one_result(self, worker: dict[str, Any], name: str) -> dict[str, Any]:
        results = self.tool_results(worker, name)
        check(len(results) == 1, f"wanted one {name} result on {worker['agent']}, got {len(results)}: {results}")
        return results[0]

    def containers(self, label: str, value: str, role: str = "sandbox") -> list[str]:
        return run(["docker", "ps", "-aq", "--filter", f"label=kionga.dev/sandbox-{label}={value}", "--filter", f"label=kionga.dev/sandbox-role={role}"]).split()

    def exec_actions(self, container: str, since: float) -> list[str]:
        """The commands Docker itself recorded being started in a container since a time."""
        out = run(["docker", "events", "--since", str(int(since)), "--until", str(int(time.time()) + 1), "--filter", f"container={container}",
                   "--filter", "event=exec_create", "--format", "{{.Action}}"], timeout=30)
        return [line.removeprefix("exec_create: ").strip() for line in out.splitlines()]

    def volumes(self, label: str, value: str) -> list[str]:
        return run(["docker", "volume", "ls", "-q", "--filter", f"label=kionga.dev/sandbox-{label}={value}"]).split()


def spawn_all(*workers: tuple[str, str]) -> dict[str, Any]:
    return {"tool_calls": [{"name": "spawn_worker", "arguments": {"agent": agent, "task": task}} for agent, task in workers]}


def coordinator_input(*workers: tuple[str, str]) -> str:
    """Spawn these in one turn, in parallel. The mock's default coordinator then waits
    for every member to hand back and finishes, so a team must spawn all its members."""
    return script([spawn_all(*workers)])


def push_steps(repository_url: str, repo: str, branch: str, subject: str) -> list[dict[str, Any]]:
    return [
        call("git_clone", repository=repository_url),
        call("edit_file", path=f"/workspace/{repo}/README.md", old="value = old", new="value = new"),
        call("exec", command=f"cd /workspace/{repo} && {GIT_IDENTITY} commit -qam '{subject}' && git rev-parse HEAD"),
        call("git_push", branch=branch),
    ]


def scenario_1(s: Smoke) -> None:
    """Coordinator spawns two workers in parallel; both work and hand back; the run succeeds."""
    alpha, beta = s.name("alpha"), s.name("beta")
    alpha_id = s.define(alpha, s.sandbox_tools(), "sandbox")
    beta_id = s.define(beta, s.sandbox_tools(), "sandbox")
    team = s.team(alpha_id, beta_id)

    def task(tag: str) -> str:
        return script([call("exec", command=f"sleep 6; echo {tag}-done"), finish(f"{tag} report: the exec finished")])
    run_id = s.start(team, coordinator_input((alpha, task("alpha")), (beta, task("beta"))))
    stream: list[tuple[str, str]] = []
    final = s.wait_run(run_id)
    events = s.events(run_id)
    types = [event["type"] for event in events]

    check(types[0] == "run.started" and types[-1] == "run.finished", f"event order: {types}")
    seqs = [event["seq"] for event in events]
    check(seqs == sorted(set(seqs)), f"seq must strictly increase: {seqs}")
    check(types.count("worker.spawned") == 3, f"spawned events: {types}")
    check(events[-1]["status"] == "succeeded", f"run.finished: {events[-1]}")

    # Parallel, proved from the log: each worker's exec started before the other's ended.
    start = {e["agent"]: e["seq"] for e in events if e["type"] == "worker.tool_call" and e.get("tool") == "exec"}
    end = {e["agent"]: e["seq"] for e in events if e["type"] == "worker.tool_result" and e.get("tool") == "exec"}
    check(start[alpha] < end[beta] and start[beta] < end[alpha], f"the workers did not overlap: start={start} end={end}")
    s.say(f"overlap: {alpha} exec seq {start[alpha]}..{end[alpha]}, {beta} exec seq {start[beta]}..{end[beta]}")

    for agent, tag in ((alpha, "alpha"), (beta, "beta")):
        worker = s.worker(run_id, agent)
        check(worker["status"] == "completed", f"{agent}: {worker['status']}")
        output = json.loads(s.one_result(worker, "exec")["content"])
        check(output["exit_code"] == 0 and f"{tag}-done" in output["stdout"], f"exec: {output}")
        check(worker["report"] == f"{tag} report: the exec finished", f"report: {worker['report']}")
        check(worker["sandbox_id"], f"{agent} has no sandbox")
        check(worker["usage"]["input_tokens"] > 0, f"{agent} usage: {worker['usage']}")

    coordinator = s.coordinator(run_id)
    check(coordinator["status"] == "completed", f"coordinator: {coordinator['status']}")
    framing = [m["content"] for m in coordinator["transcript"] if m["role"] == "user"]
    for agent in (alpha, beta):
        check(any(f'source="worker:{agent}" kind="hand-back"' in text and " report: the exec finished" in text for text in framing), f"no hand-back frame from {agent}: {framing}")
    check(final["report"] and alpha in final["report"] and beta in final["report"], f"final report: {final['report']}")
    worker_usage = [item["usage"] for item in s.workers(run_id)]
    check(final["usage"]["input_tokens"] == sum(u["input_tokens"] for u in worker_usage) > 0, f"run usage {final['usage']} vs {worker_usage}")
    check(final["usage"]["output_tokens"] == sum(u["output_tokens"] for u in worker_usage) > 0, f"run usage {final['usage']} vs {worker_usage}")
    check(re.fullmatch(r"[a-f0-9]{64}", final["content_sha256"]), f"content_sha256: {final['content_sha256']}")

    stream = s.api.sse(f"/team-runs/{run_id}/events/stream")
    check(stream and stream[-1][0] == "end" and json.loads(stream[-1][1])["status"] == "succeeded", f"stream end: {stream[-1:]}")
    streamed = [json.loads(data)["seq"] for name, data in stream if name == "run"]
    check(streamed == seqs, f"stream seqs {streamed} vs poll seqs {seqs}")
    audit = s.api.get(f"/audit?limit=500")
    actions = [item.get("action") for item in (audit["items"] if isinstance(audit, dict) else audit)]
    check("team_run.started" in actions and "team_run.succeeded" in actions, f"audit actions: {sorted(set(a for a in actions if a))}")
    s.say(f"run {run_id} succeeded: {len(events)} events, usage {final['usage']}, report {final['report'].splitlines()[0]!r}")
    s.say("audit has team_run.started and team_run.succeeded; SSE replay matches the poll log")


def scenario_2(s: Smoke) -> None:
    """A worker uses real sandbox tools under gVisor and pushes a real branch."""
    repo = s.name("repo")
    seed = s.git.seed(repo)
    url = s.git.url(repo)
    dev = s.name("dev")
    subject = "smoke change"
    steps = [
        call("write_file", path="/workspace/hello.txt", content="hello from the worker\n"),
        call("exec", command="uname -r; id -u; cat /workspace/hello.txt"),
        call("read_file", path="/workspace/hello.txt"),
        call("git_clone", repository=url),
        call("list_files", path=f"/workspace/{repo}"),
        call("search_files", pattern="value", path=repo),
        call("edit_file", path=f"/workspace/{repo}/README.md", old="value = old", new="value = new"),
        call("git_diff"),
        call("exec", command=f"cd /workspace/{repo} && {GIT_IDENTITY} commit -qam '{subject}' && git rev-parse HEAD"),
        call("git_push", branch="feature"),
        finish("pushed"),
    ]
    run_id = s.start(s.team(s.define(dev, s.sandbox_tools(url), "sandbox")), coordinator_input((dev, script(steps))), allow=True)
    s.decide(s.approval_for(run_id, dev, "git_push"), "approve")
    s.wait_run(run_id)
    worker = s.worker(run_id, dev)
    failed = [r for r in s.tool_results(worker) if r["status"] == "error"]
    check(not failed, f"tool errors: {failed}")

    written = json.loads(s.one_result(worker, "write_file")["content"])
    check(written["bytes_written"] == len("hello from the worker\n"), f"write_file: {written}")
    execs = s.tool_results(worker, "exec")
    identity = json.loads(execs[0]["content"])
    check(identity["exit_code"] == 0, f"exec: {identity}")
    kernel, uid, content = identity["stdout"].splitlines()[:3]
    check("gvisor" in kernel, f"uname -r is {kernel!r}, which is not gVisor")
    check(uid != "0", f"exec ran as uid {uid}")
    check(content == "hello from the worker", f"exec read {content!r}")
    s.say(f"uname -r in the sandbox: {kernel}; uid {uid}")
    check("hello from the worker" in s.one_result(worker, "read_file")["content"], "read_file")
    cloned = json.loads(s.one_result(worker, "git_clone")["content"])
    check(cloned["commit"] == seed and cloned["path"] == f"/workspace/{repo}", f"git_clone: {cloned} seed {seed}")
    check("README.md" in s.one_result(worker, "list_files")["content"], "list_files")
    check("value = old" in s.one_result(worker, "search_files")["content"], "search_files")
    check("+value = new" in s.one_result(worker, "git_diff")["content"], "git_diff shows no change")

    commit = json.loads(execs[1]["content"])
    check(commit["exit_code"] == 0, f"commit: {commit}")
    local_head = commit["stdout"].strip()
    pushed = json.loads(s.one_result(worker, "git_push")["content"])
    branch = f"kionga/{run_id}/feature"
    check(pushed["branch"] == branch and pushed["commit"] == local_head, f"git_push: {pushed} head {local_head}")
    refs = s.git.refs(repo)
    check(refs.get("refs/heads/" + branch) == local_head, f"server refs: {refs}, wanted {branch} at {local_head}")
    check(refs.get("refs/heads/main") == seed, f"main moved: {refs}")
    check("value = new" in s.git.git(repo, "show", f"{local_head}:README.md"), "the pushed file lacks the edit")
    check(s.git.git(repo, "log", "-1", "--format=%s", local_head) == subject, "the pushed commit has the wrong subject")
    check(s.git.git(repo, "rev-parse", f"{local_head}^") == seed, "the pushed commit is not on top of the seed")
    check(s.git.push_count(repo) == 1, f"pushes: {s.git.push_count(repo)}")
    s.say(f"server has {branch} at {local_head[:12]}, parent {seed[:12]}, main unmoved; one push request")
    s.say("the gateway's update-ref step ran for real: the push had a local branch to bundle")

    containers = s.containers("worker", worker["id"])
    check(len(containers) == 1, f"containers for the worker: {containers}")
    inspected = json.loads(run(["docker", "inspect", containers[0]]))[0]
    host = inspected["HostConfig"]
    check(host["Runtime"] == "runsc", f"runtime: {host['Runtime']}")
    check(host["NetworkMode"] == "none", f"network: {host['NetworkMode']}")
    check(host["ReadonlyRootfs"] is True and host["CapDrop"] and "ALL" in host["CapDrop"], f"hardening: {host['ReadonlyRootfs']} {host['CapDrop']}")
    s.say(f"docker inspect: runtime={host['Runtime']} network={host['NetworkMode']} read_only={host['ReadonlyRootfs']} cap_drop={host['CapDrop']}")

    transcript = json.dumps(worker)
    check(s.git_secret not in transcript, "the credential is in a transcript")
    for process in (s.gateway, s.manager, s.runner):
        check(s.git_secret not in process.log.read_text(errors="replace"), f"the credential is in the {process.name} log")
    s.say("the git credential is in no transcript or process log")


def scenario_3(s: Smoke) -> None:
    """The coordinator verifies a worker's sandbox with read_worker_file and worker_diff."""
    repo = s.name("repo")
    s.git.seed(repo)
    url = s.git.url(repo)
    dev = s.name("dev")
    worker_steps = [
        call("git_clone", repository=url),
        call("edit_file", path=f"/workspace/{repo}/README.md", old="value = old", new="value = verified"),
        finish("I changed the value; check me"),
    ]
    coordinator_steps = [
        spawn_all((dev, script(worker_steps))),
        {"content": "Waiting for the worker."},
        {"tool_calls": [{"name": "read_worker_file", "arguments": {"worker": dev, "path": f"/workspace/{repo}/README.md"}},
                        {"name": "worker_diff", "arguments": {"worker": dev}}]},
        finish("Verified the worker's file and diff myself."),
    ]
    run_id = s.start(s.team(s.define(dev, s.sandbox_tools(url), "sandbox")), script(coordinator_steps))
    final = s.wait_run(run_id)
    coordinator = s.coordinator(run_id)
    read = s.one_result(coordinator, "read_worker_file")
    check(read["status"] == "done" and "value = verified" in read["content"], f"read_worker_file: {read}")
    diff = s.one_result(coordinator, "worker_diff")
    check(diff["status"] == "done" and "+value = verified" in diff["content"] and "-value = old" in diff["content"], f"worker_diff: {diff}")
    check(final["report"] == "Verified the worker's file and diff myself.", f"report: {final['report']}")
    s.say(f"read_worker_file saw {read['content'].splitlines()[1]!r}; worker_diff has {[line for line in diff['content'].splitlines() if line.startswith(('+v', '-v'))]}")


def scenario_4(s: Smoke) -> None:
    """Side effects: an approved push proceeds, a rejected one and a disallowed one push nothing."""
    # Approved.
    repo = s.name("repo")
    s.git.seed(repo)
    url = s.git.url(repo)
    dev = s.name("dev")
    steps = push_steps(url, repo, "approved", "approved change") + [finish("pushed after approval")]
    run_id = s.start(s.team(s.define(dev, s.sandbox_tools(url), "sandbox")), coordinator_input((dev, script(steps))), allow=True)
    approval = s.approval_for(run_id, dev, "git_push")
    worker = s.worker(run_id, dev)
    check(worker["status"] == "waiting_approval", f"status: {worker['status']}")
    check(s.git.push_count(repo) == 0, "a push reached the server before the approval")
    check(not any(k.startswith("refs/heads/kionga/") for k in s.git.refs(repo)), "a branch exists before the approval")
    s.say(f"git_push parked in waiting_approval as {approval}; zero pushes on the server")
    s.decide(approval, "approve")
    s.wait_run(run_id)
    branch = f"refs/heads/kionga/{run_id}/approved"
    check(branch in s.git.refs(repo), f"refs after approval: {s.git.refs(repo)}")
    check(s.git.push_count(repo) == 1, f"pushes after approval: {s.git.push_count(repo)}")
    check(s.one_result(s.worker(run_id, dev), "git_push")["status"] == "done", "git_push did not settle done")
    s.say(f"approved: {branch} exists, one push")

    # Rejected. The model sees an error and nothing moves.
    repo = s.name("repo")
    s.git.seed(repo)
    url = s.git.url(repo)
    dev = s.name("dev")
    steps = push_steps(url, repo, "rejected", "rejected change") + [finish("the push was rejected")]
    run_id = s.start(s.team(s.define(dev, s.sandbox_tools(url), "sandbox")), coordinator_input((dev, script(steps))), allow=True)
    s.decide(s.approval_for(run_id, dev, "git_push"), "reject", "not this one")
    s.wait_run(run_id)
    result = s.one_result(s.worker(run_id, dev), "git_push")
    body = json.loads(result["content"])
    check(result["status"] == "error" and body["error"] == "approval_rejected", f"git_push: {result}")
    check("not this one" in body["message"], f"the comment did not reach the model: {body}")
    check(s.git.push_count(repo) == 0, f"pushes after rejection: {s.git.push_count(repo)}")
    check(list(s.git.refs(repo)) == ["refs/heads/main"], f"refs after rejection: {s.git.refs(repo)}")
    s.say(f"rejected: tool result {body['error']!r}, zero pushes, refs {list(s.git.refs(repo))}")

    # Not allowed at all.
    repo = s.name("repo")
    s.git.seed(repo)
    url = s.git.url(repo)
    dev = s.name("dev")
    steps = push_steps(url, repo, "denied", "denied change") + [finish("side effects are off")]
    run_id = s.start(s.team(s.define(dev, s.sandbox_tools(url), "sandbox")), coordinator_input((dev, script(steps))), allow=False)
    s.wait_run(run_id)
    result = s.one_result(s.worker(run_id, dev), "git_push")
    body = json.loads(result["content"])
    check(result["status"] == "error" and body["error"] == "side_effects_not_approved", f"git_push: {result}")
    check(s.git.push_count(repo) == 0, f"pushes with side effects off: {s.git.push_count(repo)}")
    check(s.api.get(f"/agent-approvals?run_id={run_id}")["items"] == [], "an approval was created with side effects off")
    check(not any(e["type"] == "worker.waiting_approval" for e in s.events(run_id)), "the worker waited with side effects off")
    s.say(f"allow_side_effects=false: tool result {body['error']!r}, zero pushes, no approval created")


def scenario_5(s: Smoke) -> None:
    """Scripted rate limits: the worker waits as told, spends no turns, then succeeds."""
    retry_after = 7
    worker_name = s.name("limited")
    # max_turns 1 is the proof: a retry that spent a turn would exhaust the worker.
    member = s.define(worker_name, [], "none", {"max_turns": 1, "max_tokens": 100000, "max_seconds": 600})
    task = script([{"error": {"code": "rate_limited", "message": "slow down", "retry_after_seconds": retry_after}, "times": 2}, finish("done after waiting")])
    run_id = s.start(s.team(member), coordinator_input((worker_name, task)))
    parked = s.wait_worker(run_id, worker_name, {"waiting_retry"}, 60)
    check(parked["next_attempt_at"], f"no next_attempt_at: {parked}")
    check(parked["turns"] == 0, f"turns while waiting: {parked['turns']}")
    check(s.run_view(run_id)["status"] == "running", "a run waiting on a retry must stay running")
    final = s.wait_run(run_id)
    events = s.events(run_id)
    retries = [e for e in events if e["type"] == "worker.retrying"]
    check(len(retries) == 2, f"retrying events: {retries}")
    for event in retries:
        check(event["retry_after_seconds"] == retry_after, f"retry_after: {event}")
        check(event["error"]["code"] == "rate_limited", f"error: {event}")
        check(event["status"] == "waiting_retry", f"status: {event}")
    first, second = parse_time(retries[0]["ts"]), parse_time(retries[1]["ts"])
    check((second - first).total_seconds() >= retry_after, f"retried after {(second - first).total_seconds()}s, told {retry_after}s")
    turn = next(e for e in events if e["type"] == "worker.turn" and e.get("agent") == worker_name)
    check(parse_time(turn["ts"]) >= parse_time(retries[1]["next_attempt_at"]), f"turn at {turn['ts']} before {retries[1]['next_attempt_at']}")
    worker = s.worker(run_id, worker_name)
    check(worker["status"] == "completed" and worker["turns"] == 1, f"worker: {worker['status']} turns={worker['turns']}")
    check(worker["report"] == "done after waiting", f"report: {worker['report']}")
    check(final["status"] == "succeeded", final["status"])
    s.say(f"2 waits of {retry_after}s, gap {(second - first).total_seconds():.1f}s, turns {worker['turns']} with max_turns 1")


def scenario_6a(s: Smoke) -> None:
    """kill -9 the gateway with work in flight in four runs at once; one lease expiry covers them."""
    # In-flight exec: a sandbox tool, so outcome_unknown and not repeated.
    exec_name = s.name("execer")
    exec_steps = [call("write_file", path="/workspace/before.txt", content="kept\n"),
                  call("exec", command="echo started >> /workspace/ran.log; sleep 170; echo ended >> /workspace/ran.log"),
                  call("exec", command="cat /workspace/ran.log; cat /workspace/before.txt"),
                  finish("exec outcome unknown handled")]
    exec_run = s.start(s.team(s.define(exec_name, s.sandbox_tools(), "sandbox")), coordinator_input((exec_name, script(exec_steps))))

    # In-flight git_push held at the server.
    repo = s.name("repo")
    s.git.seed(repo)
    url = s.git.url(repo)
    pusher = s.name("pusher")
    git_hold = s.git.hold(repo)
    push_run = s.start(s.team(s.define(pusher, s.sandbox_tools(url), "sandbox")),
                       coordinator_input((pusher, script(push_steps(url, repo, "durable", "durable change") + [finish("push outcome unknown handled")]))), allow=True)
    s.decide(s.approval_for(push_run, pusher, "git_push"), "approve")
    wait_for("the push to reach the git server", lambda: s.git.push_count(repo) == 1, 90)

    # In-flight read-only search, blocked on a FIFO. After the crash the fixture
    # replaces the FIFO with a regular file, which only a fresh search can read.
    searcher = s.name("searcher")
    search_started = time.time()
    search_steps = [call("exec", command="mkfifo /workspace/pipe"),
                    call("search_files", pattern="needle", path="/workspace/pipe"),
                    finish("search re-ran")]
    search_run = s.start(s.team(s.define(searcher, s.sandbox_tools(), "sandbox")), coordinator_input((searcher, script(search_steps))))

    wait_for("exec to be in flight", lambda: s.wait_tool_started(exec_run, "exec", 60) and True, 90)
    s.wait_tool_started(search_run, "search_files", 60)

    # In-flight API connection, held at the backend. Started last, because the gateway's
    # client gives up on it after 15 seconds.
    path = "/durable-" + secrets.token_hex(4)
    caller = s.name("caller")
    backend_hold = s.backend.hold(path)
    connection_id, tool = s.connection(path)
    call_run = s.start(s.team(s.define(caller, [{"kind": "connection", "ref": connection_id}], "none")),
                       coordinator_input((caller, script([call(tool, q="once"), finish("call outcome unknown handled")]))), allow=True)
    s.decide(s.approval_for(call_run, caller, tool), "approve")
    wait_for("the call to reach the backend", lambda: s.backend.count(path) == 1, 20, every=0.05)

    runs = {"exec": (exec_run, exec_name), "git_push": (push_run, pusher), "search": (search_run, searcher), "connection": (call_run, caller)}
    pending: dict[str, dict[str, Any]] = {}
    for label, (run_id, agent) in runs.items():
        pending[label] = s.worker(run_id, agent)
        check(pending[label]["lease_expires_at"], f"{label}: no lease at kill time")
    search_containers = s.containers("worker", pending["search"]["id"])
    check(len(search_containers) == 1, f"search containers: {search_containers}")
    search_container = search_containers[0]
    def search_invocations() -> list[str]:
        return [command for command in s.exec_actions(search_container, search_started)
                if "grep -rnIE" in command and "/workspace/pipe" in command]
    check(len(search_invocations()) == 1, "the initial search must reach the engine before the crash")
    search_calls = [c for m in pending["search"]["transcript"] for c in m.get("tool_calls") or [] if c["name"] == "search_files"]
    check(len(search_calls) == 1 and search_calls[0]["status"] == "pending" and search_calls[0].get("started_at"),
          f"search at kill time: {search_calls}")
    pending_call = [c for m in pending["connection"]["transcript"] for c in m.get("tool_calls") or [] if c["name"] == tool]
    check(pending_call and pending_call[0]["status"] == "pending" and pending_call[0].get("started_at"), f"call at kill time: {pending_call}")
    s.say(f"at kill: exec, search_files, git_push (server count {s.git.push_count(repo)}) and connection (backend count {s.backend.count(path)}) all pending with started_at")

    s.gateway.kill9()
    killed = datetime.now(timezone.utc)
    # Cancellation sweeps all processes in this sandbox, including background writers.
    # The fixture changes only the filesystem; neither the persisted call nor its result
    # is touched. An old reader of the FIFO cannot read this new inode.
    def release_search() -> bool:
        run(["docker", "exec", search_container, "python3", "-c",
             "from pathlib import Path; p=Path('/workspace/replay.txt'); p.write_text('needle\\n'); p.replace('/workspace/pipe')"])
        return True
    wait_for("replace the FIFO after cancellation cleanup", release_search, 30)
    backend_hold.set()
    git_hold.set()
    s.start_gateway()
    view = s.run_view(call_run)
    lease = parse_time(pending["connection"]["lease_expires_at"])
    if datetime.now(timezone.utc) < lease:
        check(view["resume_count"] == 0, f"resumed before lease expiry: {view['resume_count']}")
    s.say(f"gateway restarted {(datetime.now(timezone.utc) - killed).total_seconds():.1f}s after kill; waiting out the 60 s lease")

    finals = {label: s.wait_run(run_id, timeout=300) for label, (run_id, _) in runs.items()}
    for label, final in finals.items():
        check(final["resume_count"] >= 1, f"{label}: resume_count {final['resume_count']}")
        run_id, agent = runs[label]
        resumed = [e for e in s.events(run_id) if e["type"] == "run.resumed"]
        check(resumed, f"{label}: no run.resumed event")
        worker_resumed = [e for e in resumed if e.get("worker_id") == pending[label]["id"]]
        check(worker_resumed, f"{label}: the worker itself did not resume: {resumed}")
        check(parse_time(worker_resumed[0]["ts"]) >= parse_time(pending[label]["lease_expires_at"]), f"{label}: resumed at {worker_resumed[0]['ts']} before lease {pending[label]['lease_expires_at']}")
        check(worker_resumed[0]["resume_count"] >= 1, f"{label}: {worker_resumed[0]}")
    s.say(f"resume_count: {[(label, final['resume_count']) for label, final in finals.items()]}; every worker resumed only after its lease")

    # exec: outcome_unknown, ran once.
    worker = s.worker(exec_run, exec_name)
    results = s.tool_results(worker, "exec")
    check(len(results) == 2, f"exec results: {results}")
    check(json.loads(results[0]["content"])["error"] == "outcome_unknown", f"first exec: {results[0]}")
    after = json.loads(results[1]["content"])
    check(after["stdout"].count("started") == 1, f"the exec ran more than once: {after}")
    check("kept" in after["stdout"], f"/workspace lost a file: {after}")
    s.say(f"exec: outcome_unknown; ran.log has {after['stdout'].count('started')} start line; /workspace kept its file")

    # Connection: outcome_unknown, one request, and only one approval.
    worker = s.worker(call_run, caller)
    result = s.one_result(worker, tool)
    check(json.loads(result["content"])["error"] == "outcome_unknown", f"connection: {result}")
    check(s.backend.count(path) == 1, f"the backend saw {s.backend.count(path)} calls")
    approvals = s.api.get(f"/agent-approvals?run_id={call_run}")["items"]
    check(len(approvals) == 1, f"approvals for the call: {approvals}")
    s.say(f"connection: outcome_unknown; backend count stays {s.backend.count(path)}; {len(approvals)} approval")

    # git_push: outcome_unknown, one push request, whether or not it landed.
    worker = s.worker(push_run, pusher)
    result = s.one_result(worker, "git_push")
    check(json.loads(result["content"])["error"] == "outcome_unknown", f"git_push: {result}")
    check(s.git.push_count(repo) == 1, f"the server saw {s.git.push_count(repo)} pushes")
    s.say(f"git_push: outcome_unknown; server push count stays {s.git.push_count(repo)}; refs afterwards {sorted(s.git.refs(repo))}")

    # Read-only: re-run, not outcome_unknown.
    worker = s.worker(search_run, searcher)
    result = s.one_result(worker, "search_files")
    check(result["status"] == "done", f"search_files: {result}")
    check("needle" in result["content"], f"search_files did not run again: {result}")
    check(len(search_invocations()) == 2, f"expected exactly two search execs: {search_invocations()}")
    s.say("search_files (read-only): two engine execs; replay read the replacement file after the restart")
    check("outcome_unknown" not in json.dumps(s.tool_results(worker)), "a read-only call got outcome_unknown")


def scenario_6b(s: Smoke) -> None:
    """The runner dies too: gateway and runner killed together, then the runner alone."""
    name = s.name("survivor")
    steps = [call("exec", command="sleep 150"), call("exec", command="echo after-restart"), finish("survived both restarts")]
    run_id = s.start(s.team(s.define(name, s.sandbox_tools(), "sandbox")), coordinator_input((name, script(steps))))
    s.wait_tool_started(run_id, "exec")
    s.gateway.kill9()
    s.runner.kill9()
    s.start_gateway()
    s.say("gateway and runner killed together; gateway restarted, runner left down")
    retrying = s.wait_event(run_id, lambda e: e["type"] == "worker.retrying" and e.get("agent") == name, "worker.retrying", 180)
    check(retrying["error"]["code"] == "provider_unavailable", f"retry: {retrying}")
    check(s.worker(run_id, name)["status"] in ("waiting_retry", "running"), "the worker must wait for the runner")
    s.say(f"with the runner down the worker waits: {retrying['error']['code']}, next attempt {retrying.get('next_attempt_at')}")
    s.start_runner()
    final = s.wait_run(run_id)
    check(final["resume_count"] >= 1, f"resume_count {final['resume_count']}")
    worker = s.worker(run_id, name)
    results = s.tool_results(worker, "exec")
    check(json.loads(results[0]["content"])["error"] == "outcome_unknown", f"first exec: {results[0]}")
    check("after-restart" in json.loads(results[1]["content"])["stdout"], f"second exec: {results[1]}")
    s.say(f"run succeeded after runner restart; resume_count {final['resume_count']}")

    # The runner alone, mid-run, with a healthy gateway.
    name = s.name("survivor")
    steps = [call("exec", command="sleep 8"), finish("survived the runner restart")]
    run_id = s.start(s.team(s.define(name, s.sandbox_tools(), "sandbox")), coordinator_input((name, script(steps))))
    s.wait_tool_started(run_id, "exec")
    s.runner.kill9()
    retrying = s.wait_event(run_id, lambda e: e["type"] == "worker.retrying" and e.get("agent") == name, "worker.retrying", 60)
    check(retrying["error"]["code"] == "provider_unavailable", f"retry: {retrying}")
    s.start_runner()
    final = s.wait_run(run_id)
    check(s.worker(run_id, name)["report"] == "survived the runner restart", "report")
    s.say(f"runner alone: {retrying['error']['code']}, then succeeded; resume_count {final['resume_count']} (no gateway restart)")


def scenario_6c(s: Smoke) -> None:
    """kill -9 and restart the sandbox manager mid-run: /workspace survives."""
    gate_a, gate_b = "/gate-a-" + secrets.token_hex(3), "/gate-b-" + secrets.token_hex(3)
    conn_a, tool_a = s.connection(gate_a)
    conn_b, tool_b = s.connection(gate_b)
    name = s.name("keeper")
    tools = s.sandbox_tools() + [{"kind": "connection", "ref": conn_a}, {"kind": "connection", "ref": conn_b}]
    steps = [
        call("write_file", path="/workspace/keep.txt", content="survive me\n"),
        call(tool_a, step="gate"),  # parks in waiting_approval while the manager restarts
        call("read_file", path="/workspace/keep.txt"),
        call("exec", command="sleep 120"),  # the manager dies under this one
        call(tool_b, step="gate"),  # parks again until the manager is back
        call("read_file", path="/workspace/keep.txt"),
        call("exec", command="cat /workspace/keep.txt; uname -r"),
        finish("the workspace survived"),
    ]
    run_id = s.start(s.team(s.define(name, tools, "sandbox")), coordinator_input((name, script(steps))), allow=True)
    approval = s.approval_for(run_id, name, tool_a)
    worker = s.worker(run_id, name)
    sandbox_id = worker["sandbox_id"]
    check(sandbox_id, "the worker has no sandbox before the restart")
    before = s.containers("worker", worker["id"])
    s.manager.kill9()
    time.sleep(11)  # past the gateway's health cache
    down = s.run_view(run_id)["sandbox"]
    check(down["available"] is False, f"sandbox with the manager down: {down}")
    s.say(f"manager down: run view says sandbox {down}")
    restarted = time.time()
    s.start_manager()
    time.sleep(11)
    up = s.run_view(run_id)["sandbox"]
    check(up["available"] is True and up["runtime_verified"] is True, f"sandbox after restart: {up}")
    s.decide(approval, "approve")

    s.wait_tool_started(run_id, "exec")
    time.sleep(2)
    # A new manager trusts nothing it did not prove itself: the first command it starts in
    # this existing sandbox is the kernel check, ahead of the read_file and the exec.
    commands = s.exec_actions(before[0], restarted)
    check(commands and commands[0] == "/bin/uname -r", f"first command after the manager restart: {commands[:5]}")
    check(len(commands) >= 3, f"commands after the manager restart: {commands}")
    s.say(f"re-attested after restart: first exec in the existing sandbox was {commands[0]!r}, then {len(commands) - 1} more")
    second_restart = time.time()
    s.manager.kill9()
    approval = s.approval_for(run_id, name, tool_b)
    interrupted = s.tool_results(s.worker(run_id, name), "exec")[0]
    check(json.loads(interrupted["content"])["error"] == "sandbox_unavailable", f"exec under a dead manager: {interrupted}")
    s.say("manager killed under a running exec: the tool result is sandbox_unavailable")
    s.start_manager()
    s.decide(approval, "approve")
    s.wait_run(run_id)
    commands = s.exec_actions(before[0], second_restart)
    check(commands and commands[0] == "/bin/uname -r", f"first command after the second restart: {commands[:5]}")
    worker = s.worker(run_id, name)
    check(worker["sandbox_id"] == sandbox_id, f"sandbox changed: {sandbox_id} -> {worker['sandbox_id']}")
    check(s.containers("worker", worker["id"]) == before, f"container changed: {before} -> {s.containers('worker', worker['id'])}")
    reads = s.tool_results(worker, "read_file")
    check(len(reads) == 2 and all("survive me" in r["content"] for r in reads), f"read_file: {reads}")
    final_exec = json.loads(s.tool_results(worker, "exec")[1]["content"])
    check("survive me" in final_exec["stdout"] and "gvisor" in final_exec["stdout"], f"exec: {final_exec}")
    s.say(f"same sandbox {sandbox_id[:12]} and container {before[0][:12]} across two manager restarts; keep.txt intact; still gVisor")


def scenario_7(s: Smoke) -> None:
    """Person messages to a running and a completed worker, and cancel."""
    # A message to a running worker arrives between its tool result and its next turn.
    busy = s.name("busy")
    run_id = s.start(s.team(s.define(busy, s.sandbox_tools(), "sandbox")),
                     coordinator_input((busy, script([call("exec", command="sleep 10"), finish("got the message")]))))
    s.wait_tool_started(run_id, "exec")
    sent = s.api.post(f"/team-runs/{run_id}/messages", {"to": busy, "text": "please also check the logs"}, expect=202)
    s.wait_run(run_id)
    worker = s.worker(run_id, busy)
    check(sent["to"] == worker["id"], f"message resolved to {sent}")
    frames = [i for i, m in enumerate(worker["transcript"]) if m["role"] == "user" and 'source="person"' in m["content"] and "please also check the logs" in m["content"]]
    check(len(frames) == 1, f"person frames: {frames}")
    last_assistant = max(i for i, m in enumerate(worker["transcript"]) if m["role"] == "assistant")
    check(frames[0] < last_assistant, "the model was not called after the message")
    event = next(e for e in s.events(run_id) if e["type"] == "worker.message" and e.get("from") == "person")
    check(event["to"] == worker["id"], f"event: {event}")
    s.say(f"running worker got a source=\"person\" frame before its next turn; event from={event['from']}")

    # A message to a completed worker resumes it, while another worker keeps the run alive.
    done_name, slow_name = s.name("done"), s.name("slow")
    done_steps = [finish("first report"), finish("second report after the message")]
    team = s.team(s.define(done_name, [], "none"), s.define(slow_name, s.sandbox_tools(), "sandbox"))
    run_id = s.start(team, coordinator_input((done_name, script(done_steps)), (slow_name, script([call("exec", command="sleep 25"), finish("slow finished")]))))
    first = s.wait_worker(run_id, done_name, {"completed"})
    check(first["report"] == "first report", f"report: {first['report']}")
    s.api.post(f"/team-runs/{run_id}/messages", {"to": done_name, "text": "one more thing"}, expect=202)
    def resumed() -> Any:
        worker = s.worker(run_id, done_name)
        return worker if worker["status"] == "completed" and worker["report"] == "second report after the message" else None
    second = wait_for("the completed worker to resume and finish again", resumed, 60)
    check(second["turns"] == 2, f"turns: {second['turns']}")
    check(second["transcript"][0]["role"] == "user" and "mock-script" in second["transcript"][0]["content"], "the resume lost its transcript")
    final = s.wait_run(run_id)
    hand_backs = [m["content"] for m in s.coordinator(run_id)["transcript"] if f'source="worker:{done_name}" kind="hand-back"' in m["content"]]
    check(len(hand_backs) == 2, f"coordinator hand-backs from {done_name}: {len(hand_backs)}")
    s.say(f"completed worker resumed with its transcript: turns {second['turns']}, report {second['report']!r}; {len(hand_backs)} hand-backs; run {final['status']}")

    # Cancel stops a worker in the middle of an exec. Its sandbox stays for the retention
    # period, then the gateway deletes it. Here retention is 30 s; the default is 24 h.
    stuck = s.name("stuck")
    run_id = s.start(s.team(s.define(stuck, s.sandbox_tools(), "sandbox")), coordinator_input((stuck, script([call("exec", command="sleep 600"), finish("never")]))))
    s.wait_tool_started(run_id, "exec")
    worker_id = s.worker(run_id, stuck)["id"]
    sandbox_id = s.worker(run_id, stuck)["sandbox_id"]
    began = time.monotonic()
    cancelling = s.api.post(f"/team-runs/{run_id}/cancel", {}, expect=202)
    check(cancelling["status"] in ("cancelling", "cancelled"), f"cancel: {cancelling['status']}")
    final = s.wait_run(run_id, "cancelled", 60)
    took = time.monotonic() - began
    check(took < 30, f"cancel took {took:.1f}s with an exec still running")
    worker = s.worker(run_id, stuck)
    check(worker["status"] == "cancelled", f"worker: {worker['status']}")
    check(s.coordinator(run_id)["status"] == "cancelled", f"coordinator: {s.coordinator(run_id)['status']}")
    events = s.events(run_id)
    check(events[-1]["type"] == "run.finished" and events[-1]["status"] == "cancelled", f"last event: {events[-1]}")
    check(sum(e["type"] == "run.finished" for e in events) == 1, "run.finished more than once")
    check(s.containers("worker", worker_id), "the sandbox was removed before the retention period ended")
    s.say(f"cancelled in {took:.1f}s with sleep 600 running; sandbox {sandbox_id[:12]} retained for now")
    def gone() -> bool:
        return not s.containers("worker", worker_id) and not s.worker(run_id, stuck).get("sandbox_id")
    wait_for("the gateway to delete the sandbox after retention", gone, 90, every=1)
    leftover = s.volumes("run", run_id)
    check(not leftover, f"volumes left: {leftover}")
    s.say("after the 30 s retention the container and /workspace volume are gone and sandbox_id is cleared")
    late = s.api.post(f"/team-runs/{run_id}/messages", {"to": stuck, "text": "too late"}, expect=409)
    s.say(f"a message to a finished run is refused: {late.get('error')}")


def scenario_8(s: Smoke) -> None:
    """A knowledge query goes through the runner's /embeddings to a real Qdrant."""
    container = s.prefix + "qdrant"
    run(["docker", "run", "-d", "--name", container, "-p", "127.0.0.1::6333", QDRANT_IMAGE], timeout=240)
    published = run(["docker", "port", container, "6333/tcp"]).splitlines()[0]
    endpoint = "http://" + published
    wait_for("qdrant", lambda: s._ok(endpoint + "/readyz"), 60)
    dimensions = 64
    base = s.api.post("/admin/knowledge-bases", {"project_id": s.project, "name": s.name("kb"), "backend": "qdrant", "endpoint": endpoint, "secret_ref": "none", "dimensions": dimensions}, expect=(200, 201))
    text = "the quarterly invoice ledger lives in the archive room"
    request = urllib.request.Request(s.runner_url + "/embeddings", data=json.dumps({"text": text, "dimensions": dimensions}).encode(),
                                     headers={"Authorization": "Bearer " + s.runner_token, "Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(request, timeout=30) as response:  # noqa: S310 -- local runner
        vector = json.loads(response.read())["vector"]
    check(len(vector) == dimensions and any(vector), f"embedding of length {len(vector)}")
    s.api.post(f"/knowledge-bases/{base['id']}/chunks", {"text": text, "vector": vector, "metadata": {"source": "smoke"}}, expect=201)
    name = s.name("reader")
    tool = "knowledge_" + base["id"]
    steps = [call(tool, query=text, limit=3), finish("found it")]
    run_id = s.start(s.team(s.define(name, [{"kind": "knowledge", "ref": base["id"]}], "none")), coordinator_input((name, script(steps))))
    s.wait_run(run_id)
    result = s.one_result(s.worker(run_id, name), tool)
    check(result["status"] == "done", f"knowledge: {result}")
    items = json.loads(result["content"])["items"]
    check(items and items[0]["text"] == text and items[0]["score"] > 0.9, f"matches: {items}")
    s.say(f"knowledge tool returned the indexed chunk with score {items[0]['score']:.3f} through /embeddings and Qdrant")


def scenario_9a(s: Smoke) -> None:
    """Fail closed with the manager stopped: the team runs and the tools say sandbox_unavailable."""
    if s.manager.alive:
        s.manager.stop()
    time.sleep(11)  # past the gateway's health cache
    plain = s.name("plain")
    plain_run = s.start(s.team(s.define(plain, [], "none")), coordinator_input((plain, script([finish("no sandbox needed")]))))
    final = s.wait_run(plain_run)
    check("sandbox" not in s.run_view(plain_run), "a team with no sandbox has a sandbox status")
    s.say(f"a team with no sandbox is unaffected: {final['status']}")

    name = s.name("closed")
    steps = [call("exec", command="echo must-not-run"), finish("I could not work: sandbox_unavailable from exec")]
    run_id = s.start(s.team(s.define(name, s.sandbox_tools(), "sandbox")), coordinator_input((name, script(steps))))
    view = s.run_view(run_id)
    check(view["sandbox"]["available"] is False, f"sandbox: {view['sandbox']}")
    final = s.wait_run(run_id)
    worker = s.worker(run_id, name)
    result = s.one_result(worker, "exec")
    body = json.loads(result["content"])
    check(result["status"] == "error" and body["error"] == "sandbox_unavailable", f"exec: {result}")
    check(not worker.get("sandbox_id"), f"sandbox_id: {worker.get('sandbox_id')}")
    framing = " ".join(m["content"] for m in s.coordinator(run_id)["transcript"] if m["role"] == "user")
    check("sandbox_unavailable" in framing, "the coordinator did not hear about it")
    check(s.run_view(run_id)["sandbox"]["reason"] == "sandbox_unavailable", f"reason: {s.run_view(run_id)['sandbox']}")
    s.say(f"manager stopped: GET run sandbox={view['sandbox']}; exec returned {body['error']!r}; the (scripted) report reached the coordinator; run {final['status']}")


def scenario_9b(s: Smoke) -> None:
    """Fail closed with runsc really unregistered from Docker. Last, because it restarts Docker."""
    s.manager.stop()
    unregister = "import json,pathlib;p=pathlib.Path('/etc/docker/daemon.json');c=json.loads(p.read_text());del c['runtimes']['runsc'];p.write_text(json.dumps(c))"
    run(["sudo", "python3", "-c", unregister])
    run(["sudo", "systemctl", "restart", "docker"], timeout=120)
    runtimes = run(["docker", "info", "--format", "{{json .Runtimes}}"])
    check("runsc" not in runtimes, f"runsc is still registered: {runtimes}")
    s.say(f"runsc unregistered; Docker runtimes: {runtimes}")
    s.start_manager(verified=False)
    health = wait_for("the manager to report", lambda: (lambda h: h if "runtime_verified" in h else None)(s.manager_health()), 60)
    check(health["runtime_verified"] is False, f"healthz: {health}")
    time.sleep(11)
    name = s.name("unsafe")
    steps = [call("exec", command="echo must-not-run"), finish("no sandbox: sandbox_unavailable")]
    before = run(["docker", "ps", "-aq", "--filter", f"label=kionga.dev/sandbox-prefix={s.prefix.rstrip('-')}"]).split()
    run_id = s.start(s.team(s.define(name, s.sandbox_tools(), "sandbox")), coordinator_input((name, script(steps))))
    view = s.run_view(run_id)
    check(view["sandbox"]["available"] is False and view["sandbox"]["runtime_verified"] is False, f"sandbox: {view['sandbox']}")
    final = s.wait_run(run_id)
    result = s.one_result(s.worker(run_id, name), "exec")
    check(json.loads(result["content"])["error"] == "sandbox_unavailable", f"exec: {result}")
    after = run(["docker", "ps", "-aq", "--filter", f"label=kionga.dev/sandbox-prefix={s.prefix.rstrip('-')}"]).split()
    check(set(after) == set(before), f"a container appeared: {set(after) - set(before)}")
    check(not s.containers("run", run_id), "a sandbox exists for this run")
    s.say(f"healthz {health}; GET run sandbox={view['sandbox']}; exec sandbox_unavailable; no container created; run {final['status']}")


def scenario_11(s: Smoke) -> None:
    """A starter who loses the functions service can no longer run a registered tool."""
    function = s.name("smoke-fn")
    s.api.post("/functions", {"project_id": s.project, "name": function, "image": "registry.example.invalid/smoke@sha256:" + "0" * 64}, expect=(200, 201, 202))
    s.api.post("/tools", {"name": function, "version": "1.0.0", "description": "Smoke tool.", "tags": [], "input_schema": {"type": "object"}}, expect=(200, 201))
    subject = s.name("starter")
    access = {"email": f"{subject}@example.invalid", "role": "user", "services": ["agents", "functions"], "project_ids": [s.project]}
    s.api.call("PUT", f"/admin/users/{subject}", access)
    password = "smoke-" + secrets.token_hex(12)
    s.api.call("PUT", f"/admin/users/{subject}/local-password", {"password": password})
    starter = Api(s.origin, subject, password)
    starter.login()

    name = s.name("toolrunner")
    tool = "tool_" + function
    steps = [call(tool, input="first"), call(tool, input="second"), finish("the tool was refused the second time")]
    team = s.team(s.define(name, [{"kind": "tool", "ref": function}], "none"))
    created = starter.post("/team-runs", {"team_id": team, "input": coordinator_input((name, script(steps))), "allow_side_effects": True}, expect=202)
    run_id = created["id"]
    s.runs.append(run_id)
    check(created["subject"] == subject, f"run subject: {created['subject']}")
    path = "/function/" + function

    # The control: with the service, the tool really runs.
    s.decide(s.approval_for(run_id, name, tool), "approve")
    wait_for("the first call to reach the function", lambda: s.backend.count(path) == 1, 30)
    def second_parked() -> Any:
        worker = s.worker(run_id, name)
        return worker["status"] == "waiting_approval" and len(s.tool_results(worker, tool)) == 1
    wait_for("the second call to wait for approval", second_parked, 60)
    first = s.tool_results(s.worker(run_id, name), tool)[0]
    check(first["status"] == "done", f"first call: {first}")
    s.say(f"with the functions service: first call done, function request count {s.backend.count(path)}")

    pending = s.api.get(f"/agent-approvals?status=pending&run_id={run_id}")["items"]
    check(len(pending) == 1, f"pending approvals: {pending}")
    s.api.call("PUT", f"/admin/users/{subject}", {**access, "services": ["agents"]})
    s.decide(pending[0]["id"], "approve")
    final = s.wait_run(run_id)
    second = s.tool_results(s.worker(run_id, name), tool)[1]
    body = json.loads(second["content"])
    check(second["status"] == "error" and body["error"] == "access_denied", f"second call: {second}")
    check(s.backend.count(path) == 1, f"the function ran without the service: count {s.backend.count(path)}")
    s.say(f"functions removed, then a person approved: result {body['error']!r}, function count stays {s.backend.count(path)}; run {final['status']}, because the starter keeps agents")


def scenario_10(s: Smoke) -> None:
    """Real responses, events and runner wire traffic against the contract schemas."""
    schema_dir = s.contracts
    def validator(name: str) -> jsonschema.Draft202012Validator:
        return jsonschema.Draft202012Validator(json.loads((schema_dir / name).read_text()), format_checker=jsonschema.FormatChecker())
    definition, team, event_schema = validator("agent-definition.v1.schema.json"), validator("team.v1.schema.json"), validator("team-run-event.v1.schema.json")
    turn_request, turn_response = validator("runner-turn-request.v1.schema.json"), validator("runner-turn-response.v1.schema.json")
    embedding_response = validator("runner-embedding-response.v1.schema.json")
    sandbox_full = json.loads((schema_dir / "sandbox-api.v1.schema.json").read_text())
    sandbox_view = jsonschema.Draft202012Validator({"$ref": "#/$defs/run_view_sandbox", "$defs": sandbox_full["$defs"]})
    problems: list[str] = []

    def validate(check_with: jsonschema.Draft202012Validator, document: Any, label: str) -> None:
        for error in check_with.iter_errors(document):
            problems.append(f"{label}: {list(error.absolute_path)}: {error.message[:300]}")

    # Resource documents, with a saved revision so the store really holds history.
    name = s.name("schema")
    agent_id = s.define(name, s.sandbox_tools(), "sandbox")
    first = s.api.get(f"/agent-definitions/{agent_id}")
    body = {key: first[key] for key in ("name", "description", "instructions", "model", "tools", "workspace", "limits")}
    body["description"] = "An edited description."
    saved = s.api.call("PUT", f"/agent-definitions/{agent_id}", {**body, "revision": first["revision"]})
    check(saved["revision"] == first["revision"] + 1, f"revision: {saved['revision']}")
    revisions = s.api.get(f"/agent-definitions/{agent_id}/revisions")
    history = revisions["items"] if isinstance(revisions, dict) else revisions
    check(len(history) >= 1, f"no stored revisions: {revisions}")
    team_id = s.team(agent_id)
    team_doc = s.api.get(f"/agent-teams/{team_id}")
    team_saved = s.api.call("PUT", f"/agent-teams/{team_id}", {**{k: team_doc[k] for k in ("name", "description", "coordinator", "members", "brief", "limits")}, "revision": team_doc["revision"]})
    s.record("definition", first)
    s.record("definition", saved)
    s.record("team", team_doc)
    s.record("team", team_saved)
    for item in s.api.get("/agent-definitions")["items"]:
        s.record("definition", item)
    for item in s.api.get("/agent-teams")["items"]:
        s.record("team", item)
    for label, document in s.api_documents:
        validate(definition if label == "definition" else team, document, f"{label} {document.get('id')}")
    s.say(f"validated {len(s.api_documents)} definition and team documents; the agent has {len(history)} stored revision(s)")

    # A run through the recorder, so the turn requests the gateway really sends are checked.
    recorder = RunnerRecorder(s.runner_url)
    s.gateway.stop()
    s.gateway_runner_url = recorder.url
    try:
        s.start_gateway()
        steps = [call("exec", command="echo schema"), finish("schema report")]
        run_id = s.start(team_id, coordinator_input((name, script(steps))))
        final = s.wait_run(run_id)
        check(final["report"], "no report")
    finally:
        s.gateway.stop()
        s.gateway_runner_url = s.runner_url
        s.start_gateway()
        recorder.close()
    turns = [item for item in recorder.exchanges if item["path"] == "/agent-turns"]
    check(len(turns) >= 4, f"recorded turns: {len(turns)}")
    for number, exchange in enumerate(turns):
        check(exchange["status"] == 200, f"turn status {exchange['status']}")
        request_body = json.loads(exchange["request"])
        response_body = json.loads(exchange["response"])
        validate(turn_request, request_body, f"turn request {number}")
        validate(turn_response, response_body, f"turn response {number}")
        wire = exchange["request"].decode() + exchange["response"].decode()
        check(s.git_secret not in wire and s.manager_token not in wire and s.runner_token not in wire, f"a credential is on the runner wire in turn {number}")
    s.say(f"recorded {len(turns)} real runner turns; requests and responses checked against the contract")

    request = urllib.request.Request(s.runner_url + "/embeddings", data=json.dumps({"text": "schema", "dimensions": 8}).encode(),
                                     headers={"Authorization": "Bearer " + s.runner_token, "Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(request, timeout=30) as response:  # noqa: S310 -- local runner
        validate(embedding_response, json.loads(response.read()), "embedding response")

    # Every event of every run so far.
    total = 0
    for run_id in s.runs:
        events = s.events(run_id)
        total += len(events)
        for event in events:
            validate(event_schema, event, f"event {run_id}#{event.get('seq')} {event.get('type')}")
        seqs = [event["seq"] for event in events]
        if seqs != sorted(set(seqs)):
            problems.append(f"run {run_id}: seq not strictly increasing: {seqs}")
        view = s.run_view(run_id)
        if "sandbox" in view:
            validate(sandbox_view, view["sandbox"], f"run {run_id} sandbox")
    s.say(f"validated {total} events across {len(s.runs)} runs")

    # Nothing stored for the scheduler or the revision history leaks into a view.
    leak_checks: list[tuple[str, Any]] = [
        ("definition get", s.api.get(f"/agent-definitions/{agent_id}")),
        ("definition put", saved),
        ("definition list", s.api.get("/agent-definitions")),
        ("team get", s.api.get(f"/agent-teams/{team_id}")),
        ("team put", team_saved),
        ("team list", s.api.get("/agent-teams")),
        ("run get", s.run_view(s.runs[-1])),
        ("run list", s.api.get("/team-runs")),
        ("workers", s.api.get(f"/team-runs/{s.runs[-1]}/workers")),
        ("worker", s.coordinator(s.runs[-1])),
    ]
    for label, document in leak_checks:
        keys = walk_keys(document)
        for forbidden in ("history", "lease_token", "inbox_seen", "active_since", "credential", "secret_ref"):
            if forbidden in keys:
                problems.append(f"{label} exposes {forbidden!r}")
    s.say(f"checked {len(leak_checks)} API views for history, lease_token, inbox_seen, credentials")

    if problems:
        for line in problems[:60]:
            note("CONTRACT: " + line)
        raise AssertionError(f"{len(problems)} contract problems, first: {problems[0]}")

    for process in (s.gateway, s.runner, s.manager):
        text = process.log.read_text(errors="replace") if process.log.exists() else ""
        check(s.git_secret not in text, f"the git credential is in the {process.name} log")
        check(s.manager_token not in text, f"the manager token is in the {process.name} log")
    s.say("no credential in any process log after the whole suite")


SCENARIOS: list[tuple[str, str, Callable[[Smoke], None], bool]] = [
    ("1", "team run: parallel workers, report, usage", scenario_1, True),
    ("2", "sandbox tools and a real git push under gVisor", scenario_2, True),
    ("3", "coordinator reads a worker's sandbox", scenario_3, True),
    ("4", "approval: approve, reject, side effects off", scenario_4, True),
    ("5", "rate limit: waits, spends no turns", scenario_5, False),
    ("6a", "durability: kill -9 gateway with tools in flight", scenario_6a, True),
    ("6b", "durability: gateway and runner die", scenario_6b, True),
    ("6c", "durability: sandbox manager restarts", scenario_6c, True),
    ("7", "person messages and cancel", scenario_7, True),
    ("8", "knowledge query through /embeddings", scenario_8, True),
    ("9a", "fail closed: manager stopped", scenario_9a, False),
    ("11", "starter loses the functions service", scenario_11, False),
    ("10", "contract schemas and leak checks", scenario_10, True),
    ("9b", "fail closed: runsc unregistered", scenario_9b, True),
]


def main() -> int:
    smoke = Smoke()
    wanted = {item.strip() for item in os.environ.get("KIONGA_SMOKE_SCENARIOS", "").split(",") if item.strip()}
    results: list[dict[str, Any]] = []
    status = 0
    try:
        smoke.start_runner()
        if smoke.engine:
            smoke.start_manager()
            print(f"manager healthz: {smoke.manager_health()}", flush=True)
        smoke.start_gateway()
        smoke.api.login()
        projects = smoke.api.get("/projects")
        items = projects if isinstance(projects, list) else projects.get("items") or []
        if not items:
            items = [smoke.api.post("/projects", {"name": "agent-teams-smoke", "description": "smoke"}, expect=(200, 201))]
        smoke.project = items[0]["id"]
        created = smoke.api.post("/admin/agent-api-connections", {"project_id": smoke.project, "name": "smoke-git",
                                 "endpoint": smoke.backend.url + "/git-unused", "secret_ref": "env:KIONGA_CONNECTION_SECRET_SMOKE_GIT"}, expect=(200, 201))
        smoke.git_connection = created["id"]
        print(f"project {smoke.project}; git connection {smoke.git_connection}; git server {smoke.git.host}", flush=True)
        print(f"LIMIT: {ALLOWANCE}", flush=True)

        for scenario_id, title, function, needs_engine in SCENARIOS:
            if wanted and scenario_id not in wanted and scenario_id.rstrip("abc") not in wanted:
                results.append({"id": scenario_id, "title": title, "result": "NOT RUN", "why": "not selected", "evidence": []})
                continue
            if needs_engine and not smoke.engine:
                results.append({"id": scenario_id, "title": title, "result": "NOT RUN", "why": "no container engine in this mode", "evidence": []})
                continue
            if scenario_id == "9b" and not smoke.can_sudo:
                results.append({"id": scenario_id, "title": title, "result": "NOT RUN", "why": "needs sudo to change Docker's runtimes", "evidence": []})
                continue
            print(f"\n== scenario {scenario_id}: {title}", flush=True)
            smoke.evidence = []
            began = time.monotonic()
            try:
                if scenario_id != "9a":
                    smoke.ensure_up()
                else:
                    if not smoke.runner.alive:
                        smoke.start_runner()
                    if not smoke.gateway.alive:
                        smoke.start_gateway()
                function(smoke)
                results.append({"id": scenario_id, "title": title, "result": "PASS", "seconds": round(time.monotonic() - began), "evidence": smoke.evidence})
                print(f"PASS {scenario_id} ({time.monotonic() - began:.0f}s)", flush=True)
            except (AssertionError, ApiError, OSError, ValueError, KeyError, IndexError, TypeError, StopIteration) as error:
                status = 1
                print(f"FAIL {scenario_id}: {error}", flush=True)
                traceback.print_exc(file=sys.stdout)
                for process in (smoke.gateway, smoke.manager, smoke.runner):
                    print(f"--- {process.name} log tail", flush=True)
                    print(process.tail(25), flush=True)
                results.append({"id": scenario_id, "title": title, "result": "FAIL", "seconds": round(time.monotonic() - began), "error": str(error)[:1500], "evidence": smoke.evidence})
    finally:
        smoke.stop_all()
        smoke.remove_engine_objects()
        smoke.git.close()
        smoke.backend.close()

    print("\n== results", flush=True)
    for item in results:
        print(f"{item['result']:8} {item['id']:4} {item['title']}" + (f" ({item.get('why')})" if item.get("why") else ""), flush=True)
        for line in item["evidence"]:
            print(f"         - {line}", flush=True)
        if item.get("error"):
            print(f"         ! {item['error'][:400]}", flush=True)
    print(f"\nLIMIT: {ALLOWANCE}", flush=True)
    print("Not exercised:", flush=True)
    for line in NOT_EXERCISED:
        print(f"  - {line}", flush=True)
    (smoke.work / "results.json").write_text(json.dumps({"results": results, "limit": ALLOWANCE, "not_exercised": NOT_EXERCISED}, indent=2))
    if status == 0 and any(item["result"] == "NOT RUN" for item in results):
        print("\nINCOMPLETE: nothing failed, but not every scenario ran", flush=True)
        return 3
    print("\nFAIL" if status else "\nPASS: every scenario ran and passed", flush=True)
    return status


if __name__ == "__main__":
    sys.exit(main())
