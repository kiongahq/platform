#!/usr/bin/env bash
# End-to-end Agent Designer check across real processes: the gateway binary
# (file store) and agent-runtime's designer runner with the mock chat model.
# Builds a design from the customer-support template, validates it, runs it
# with a streamed trace, saves test cases, evaluates them against a quality
# gate, publishes an immutable version and runs that version.
#
#   AGENT_RUNTIME_DIR=../agent-runtime scripts/agent-designer-smoke.sh
#
# KEEP_RUNNING=1 leaves the gateway and runner up after the checks pass and
# prints the URL and credentials for load tests.
#
# AGENT_RUNTIME_DIR must contain a .venv with its requirements installed
# (see agent-runtime/CONTRIBUTING.md). Ports: GATEWAY_PORT (18080), RUNNER_PORT
# (19100) and the gateway's fixed internal listener 8087.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
AGENT_RUNTIME_DIR="$(cd "${AGENT_RUNTIME_DIR:-$ROOT/../agent-runtime}" && pwd)"
GATEWAY_PORT="${GATEWAY_PORT:-18080}"
RUNNER_PORT="${RUNNER_PORT:-19100}"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/agent-designer-smoke.XXXXXX")"
TOKEN="smoke-runner-token-$(date +%s)"
BASE="http://127.0.0.1:$GATEWAY_PORT/api/v1"
PY="$AGENT_RUNTIME_DIR/.venv/bin/python"
pids=()

cleanup() {
  for pid in "${pids[@]:-}"; do [ -n "$pid" ] && kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
  if [ "${KEEP_WORK:-}" = "1" ]; then echo "logs kept in $WORK"; else rm -rf "$WORK"; fi
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; echo "--- gateway log" >&2; tail -40 "$WORK/gateway.log" >&2 || true; echo "--- runner log" >&2; tail -40 "$WORK/runner.log" >&2 || true; KEEP_WORK=1; exit 1; }
json() { "$PY" -c "import json,sys; data=json.load(sys.stdin); print(eval(sys.argv[1], {'d': data}))" "$1"; }
ORIGIN="http://127.0.0.1:$GATEWAY_PORT"
JAR="$WORK/cookies.txt"
PASSWORD="smoke-$(date +%s)-password"
call() { # method path [body]  (signed-in session from $JAR)
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then curl -sS -b "$JAR" -H "Origin: $ORIGIN" -X "$method" -H 'Content-Type: application/json' -H 'Accept: application/json' --data "$body" "$BASE$path"
  else curl -sS -b "$JAR" -H "Origin: $ORIGIN" -X "$method" -H 'Accept: application/json' "$BASE$path"; fi
}
wait_http() { for _ in $(seq 1 100); do curl -fsS "$1" >/dev/null 2>&1 && return 0; sleep 0.2; done; fail "$1 did not become ready"; }

[ -x "$PY" ] || fail "no virtualenv at $AGENT_RUNTIME_DIR/.venv"
command -v go >/dev/null || fail "go is required"

step "build gateway"
(cd "$ROOT/go" && GOFLAGS="${GOFLAGS:--mod=mod}" go build -o "$WORK/gateway" ./cmd/gateway)

step "start runner on :$RUNNER_PORT (mock model)"
(cd "$AGENT_RUNTIME_DIR" && KIONGA_DESIGNER_RUNNER_TOKEN="$TOKEN" KIONGA_DESIGNER_PORT="$RUNNER_PORT" KIONGA_DESIGNER_HOST=127.0.0.1 \
  MLAIOPS_LLM_BACKEND=mock exec "$PY" -m agent_runtime.designer) >"$WORK/runner.log" 2>&1 &
pids+=($!)
wait_http "http://127.0.0.1:$RUNNER_PORT/healthz"

step "start gateway on :$GATEWAY_PORT (file store, local admin)"
(cd "$WORK" && PORT="$GATEWAY_PORT" MLAIOPS_DATA_PATH="$WORK/platform.json" MLAIOPS_LOCAL_ROLE=admin \
  MLAIOPS_LOCAL_USERNAME=admin MLAIOPS_LOCAL_PASSWORD="$PASSWORD" \
  KIONGA_AGENT_DESIGNER_RUNNER_URL="http://127.0.0.1:$RUNNER_PORT" KIONGA_AGENT_DESIGNER_RUNNER_TOKEN="$TOKEN" \
  KIONGA_AGENT_DESIGNER_CALLBACK_URL="http://127.0.0.1:8087/api/v1" exec "$WORK/gateway") >"$WORK/gateway.log" 2>&1 &
pids+=($!)
wait_http "$BASE/health"
curl -sS -c "$JAR" -o /dev/null -H "Origin: $ORIGIN" --data-urlencode username=admin --data-urlencode "password=$PASSWORD" \
  --data-urlencode return_to=/console.html "$ORIGIN/auth/local/login"
grep -q . "$JAR" || fail "local sign-in returned no session"

step "components and templates"
call GET /agent-designs/components > "$WORK/components.json"
components="$(json "len(d.get('components') or [])" < "$WORK/components.json")" || fail "components: $(head -c 300 "$WORK/components.json")"
[ "$components" -ge 10 ] || fail "registry has $components components"
call GET /agent-designs/templates > "$WORK/templates.json"
project="$(call GET /projects | json "(d if isinstance(d, list) else d.get('items', []))[0]['id']")"
echo "registry components=$components project=$project"

step "create a design from the customer-support template"
body="$("$PY" - "$WORK/templates.json" "$project" <<'EOF'
import json, sys
templates = json.load(open(sys.argv[1]))["items"]
graph = next(t for t in templates if t["id"] == "customer-support")["graph"]
print(json.dumps({"project_id": sys.argv[2], "name": "Smoke support agent", "revision": 0,
                  "nodes": graph["nodes"], "edges": graph["edges"], "settings": graph["settings"]}))
EOF
)"
design="$(call POST /agent-designs "$body" | json "d['id']")"
echo "design=$design"

step "validate"
runnable="$(call POST "/agent-designs/$design/validate" | json "d['runnable']")"
[ "$runnable" = "True" ] || fail "template is not runnable"

step "playground run: refund branch"
run="$(call POST "/agent-designs/$design/runs" '{"input":"I want a refund for my order","history":[]}' | json "d['id']")"
for _ in $(seq 1 100); do
  status="$(call GET "/agent-designs/$design/runs/$run" | json "d['status']")"
  [ "$status" = running ] || break; sleep 0.2
done
call GET "/agent-designs/$design/runs/$run" > "$WORK/run.json"
[ "$(json "d['status']" < "$WORK/run.json")" = succeeded ] || fail "run did not succeed: $(cat "$WORK/run.json")"
"$PY" - "$WORK/run.json" <<'EOF' || fail "trace is wrong"
import json, sys
run = json.load(open(sys.argv[1]))
events = [json.loads(e) if isinstance(e, str) else e for e in run["events"]]
types = [e["type"] for e in events]
by_node = {e.get("node_id"): e for e in events if e["type"] in ("node.completed", "node.skipped")}
assert types[0] == "run.started" and types[-1] == "run.completed", types
seqs = [e["seq"] for e in events]
assert seqs == sorted(set(seqs)), "seq must increase strictly (gaps are live-only deltas)"
assert by_node["route"]["port"] == "true", by_node["route"]
assert by_node["refund"]["type"] == "node.completed" and by_node["general"]["type"] == "node.skipped"
assert run["output"], "empty reply"
print(f"events={len(events)} branch=true output={run['output'][:60]!r} tokens={run['usage']}")
EOF

step "server-sent events resume after event 2"
curl -sS -N --max-time 5 -b "$JAR" -H 'Accept: text/event-stream' -H 'Last-Event-ID: 2' "$BASE/agent-designs/$design/runs/$run/events" > "$WORK/sse.txt" || true
grep -q '^event: end' "$WORK/sse.txt" || fail "SSE stream did not end"
! grep -q '^id: [012]$' "$WORK/sse.txt" || fail "SSE replayed events before Last-Event-ID"

step "test cases, evaluation and quality gate"
call PUT "/agent-designs/$design/fixtures" '{"revision":0,"cases":[{"id":"refund","input":"Where is my refund?","expected":{}},{"id":"general","input":"What are your hours?","expected":{"not_contains":["Traceback"]}}]}' >/dev/null
evaluation="$(call POST "/agent-designs/$design/evaluations" '{"gate":1}' | json "d['id']")"
for _ in $(seq 1 150); do
  status="$(call GET "/agent-evaluations/$evaluation" | json "d['status']")"
  [ "$status" = running ] || break; sleep 0.2
done
call GET "/agent-evaluations/$evaluation" > "$WORK/evaluation.json"
[ "$(json "d['status']" < "$WORK/evaluation.json")" = completed ] || fail "evaluation did not complete: $(cat "$WORK/evaluation.json")"
[ "$(json "d['passed']" < "$WORK/evaluation.json")" = True ] || fail "evaluation gate failed: $(cat "$WORK/evaluation.json")"
echo "evaluation pass_rate=$(json "d['pass_rate']" < "$WORK/evaluation.json") p95_ms=$(json "d['p95_latency_ms']" < "$WORK/evaluation.json")"

step "publish with the evaluation, then publish again (idempotent)"
revision="$(call GET "/agent-designs/$design" | json "d['revision']")"
version="$(call POST "/agent-designs/$design/publish" "{\"revision\":$revision,\"notes\":\"smoke\",\"evaluation_id\":\"$evaluation\"}" | json "d['id']")"
again="$(call POST "/agent-designs/$design/publish" "{\"revision\":$revision}" | json "d['id']")"
[ "$version" = "$again" ] || fail "republishing identical behavior created a new version"
hash="$(call GET "/agent-versions/$version" | json "d['content_sha256']")"
call GET "/agent-versions/$version" > "$WORK/version.json"
"$PY" - "$AGENT_RUNTIME_DIR" "$WORK/version.json" <<'EOF' || fail "runtime rejects the published artifact hash"
import json, sys
sys.path.insert(0, sys.argv[1])
from agent_runtime.native_graph import content_sha256
artifact = json.load(open(sys.argv[2]))
assert content_sha256(artifact["graph"]) == artifact["content_sha256"], "Go and Python hashes differ"
print("python runtime verifies hash", artifact["content_sha256"][:12])
EOF

step "run the published version"
vrun="$(call POST "/agent-versions/$version/runs" '{"input":"hello there"}' | json "d['id']")"
for _ in $(seq 1 100); do
  status="$(call GET "/agent-designs/$design/runs/$vrun" | json "d['status']")"
  [ "$status" = running ] || break; sleep 0.2
done
[ "$status" = succeeded ] || fail "version run ended $status"

step "API node: run credential callback through the internal listener"
ECHO_PORT="${ECHO_PORT:-19111}"
"$PY" - "$ECHO_PORT" >"$WORK/echo.log" 2>&1 <<'EOF' &
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
class Echo(BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        reply = json.dumps({"echo": body, "authorization": self.headers.get("Authorization", "")}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(reply)
HTTPServer(("127.0.0.1", int(sys.argv[1])), Echo).serve_forever()
EOF
pids+=($!)
sleep 0.5
connection="$(call POST /admin/agent-api-connections "{\"project_id\":\"$project\",\"name\":\"smoke-echo\",\"endpoint\":\"http://127.0.0.1:$ECHO_PORT/echo\",\"secret_ref\":\"none\"}" | json "d['id']")"
api_design="$(call POST /agent-designs "{\"project_id\":\"$project\",\"name\":\"Smoke API agent\",\"revision\":0,\"nodes\":[{\"id\":\"input\",\"kind\":\"input\",\"name\":\"In\",\"x\":0,\"y\":0},{\"id\":\"lookup\",\"kind\":\"api\",\"name\":\"Lookup\",\"x\":200,\"y\":0,\"config\":{\"connection\":\"$connection\",\"request\":\"{\\\"q\\\": \\\"{input}\\\"}\"}},{\"id\":\"output\",\"kind\":\"output\",\"name\":\"Out\",\"x\":400,\"y\":0}],\"edges\":[{\"from\":\"input\",\"to\":\"lookup\"},{\"from\":\"lookup\",\"to\":\"output\"}]}" | json "d['id']")"
finish_run() { # design run -> final run JSON on stdout
  for _ in $(seq 1 100); do
    status="$(call GET "/agent-designs/$1/runs/$2" | json "d['status']")"
    [ "$status" = running ] || break; sleep 0.2
  done
  call GET "/agent-designs/$1/runs/$2"
}
denied="$(call POST "/agent-designs/$api_design/runs" '{"input":"order-42"}' | json "d['id']")"
finish_run "$api_design" "$denied" > "$WORK/denied.json"
grep -q 'side_effects_not_approved' "$WORK/denied.json" || fail "API node ran without side-effect approval: $(cat "$WORK/denied.json")"
! grep -q 'order-42' "$WORK/echo.log" || fail "the API was called before approval"
approved="$(call POST "/agent-designs/$api_design/runs" '{"input":"order-42","allow_side_effects":true}' | json "d['id']")"
finish_run "$api_design" "$approved" > "$WORK/approved.json"
[ "$(json "d['status']" < "$WORK/approved.json")" = succeeded ] || fail "API run failed: $(cat "$WORK/approved.json")"
json "d['output']" < "$WORK/approved.json" | grep -q '"q":"order-42"' || fail "API node output: $(json "d['output']" < "$WORK/approved.json")"
echo "approved API run output: $(json "d['output']" < "$WORK/approved.json")"

step "error port: a failing API call falls back instead of failing the run"
dead="$(call POST /admin/agent-api-connections "{\"project_id\":\"$project\",\"name\":\"smoke-dead\",\"endpoint\":\"http://127.0.0.1:9/never\",\"secret_ref\":\"none\"}" | json "d['id']")"
fallback_design="$(call POST /agent-designs "{\"project_id\":\"$project\",\"name\":\"Smoke fallback\",\"revision\":0,\"nodes\":[{\"id\":\"input\",\"kind\":\"input\",\"name\":\"In\",\"x\":0,\"y\":0},{\"id\":\"lookup\",\"kind\":\"api\",\"name\":\"Lookup\",\"x\":200,\"y\":0,\"config\":{\"connection\":\"$dead\"}},{\"id\":\"pick\",\"kind\":\"extract\",\"name\":\"Pick\",\"x\":400,\"y\":0,\"config\":{\"path\":\"result.status\"}},{\"id\":\"sorry\",\"kind\":\"prompt\",\"name\":\"Sorry\",\"x\":400,\"y\":150,\"config\":{\"template\":\"Lookup unavailable\"}},{\"id\":\"output\",\"kind\":\"output\",\"name\":\"Out\",\"x\":600,\"y\":0}],\"edges\":[{\"from\":\"input\",\"to\":\"lookup\"},{\"from\":\"lookup\",\"to\":\"pick\"},{\"from\":\"lookup\",\"to\":\"sorry\",\"from_port\":\"error\"},{\"from\":\"pick\",\"to\":\"output\"},{\"from\":\"sorry\",\"to\":\"output\"}]}" | json "d['id']")"
frun="$(call POST "/agent-designs/$fallback_design/runs" '{"input":"x","allow_side_effects":true}' | json "d['id']")"
finish_run "$fallback_design" "$frun" > "$WORK/fallback.json"
[ "$(json "d['status']" < "$WORK/fallback.json")" = succeeded ] || fail "fallback run: $(cat "$WORK/fallback.json")"
[ "$(json "d['output']" < "$WORK/fallback.json")" = "Lookup unavailable" ] || fail "fallback output: $(json "d['output']" < "$WORK/fallback.json")"
echo "fallback branch completed: $(json "d['output']" < "$WORK/fallback.json")"

step "flows: a subflow runs the published version; approval is decided over the API mid-run"
flow_body="$("$PY" - "$project" "$version" <<'EOF'
import json, sys
project, version = sys.argv[1], sys.argv[2]
n = lambda i, k, x, **c: {"id": i, "kind": k, "name": i, "x": x, "y": 0, **({"config": c} if c else {})}
e = lambda a, b, p="out": {"from": a, "to": b, "from_port": p, "to_port": "in"}
print(json.dumps({"project_id": project, "name": "Smoke flows", "revision": 0,
  "nodes": [n("input", "input", 0), n("route", "router", 200, route_1="contains:refund"),
            n("support", "subflow", 400, subflow=version), n("plain", "prompt", 400, template="plain: {input}"),
            n("ok", "approval", 600, message="Send this? {context}", timeout_seconds="60"),
            n("held", "prompt", 800, template="held for review"), n("reply", "model", 800, model="llm"), n("output", "output", 1000)],
  "edges": [e("input", "route"), e("route", "support", "route_1"), e("route", "plain", "default"),
            e("support", "ok"), e("plain", "ok"), e("ok", "reply", "approved"), e("reply", "output"), e("ok", "held", "rejected"), e("held", "output")],
  "settings": {"max_steps": 50, "timeout_seconds": 120, "max_tokens": 50000}}))
EOF
)"
flows_design="$(call POST /agent-designs "$flow_body" | json "d['id']")"
[ "$(call POST "/agent-designs/$flows_design/validate" | json "d['runnable']")" = True ] || fail "flows design is not runnable"
frun="$(call POST "/agent-designs/$flows_design/runs" '{"input":"I want a refund"}' | json "d['id']")"
curl -sS -N --max-time 60 -b "$JAR" -H 'Accept: text/event-stream' "$BASE/agent-designs/$flows_design/runs/$frun/events" > "$WORK/flows-sse.txt" &
sse_pid=$!
approval=""
for _ in $(seq 1 100); do
  approval="$(call GET "/agent-approvals?status=pending&run_id=$frun" | json "(d['items'] or [{'id': ''}])[0]['id']")"
  [ -n "$approval" ] && break; sleep 0.2
done
[ -n "$approval" ] || fail "no approval request appeared"
call POST "/agent-approvals/$approval/decision" '{"decision":"approve","comment":"smoke"}' > "$WORK/decision.json"
[ "$(json "d['status']" < "$WORK/decision.json")" = approved ] || fail "decision: $(cat "$WORK/decision.json")"
finish_run "$flows_design" "$frun" > "$WORK/flows.json"
wait "$sse_pid" 2>/dev/null || true
"$PY" - "$WORK/flows.json" "$WORK/flows-sse.txt" <<'EOF' || fail "flows run is wrong"
import json, sys
run = json.load(open(sys.argv[1]))
events = [json.loads(e) if isinstance(e, str) else e for e in run["events"]]
types = [e["type"] for e in events]
assert run["status"] == "succeeded", run
assert run["output"].startswith("[mock]"), run["output"]
assert "approval.requested" in types and "approval.resolved" in types, types
resolved = next(e for e in events if e["type"] == "approval.resolved")
assert resolved["port"] == "approved"
assert next(e for e in events if e.get("node_id") == "plain")["type"] == "node.skipped"
assert not any(e["type"] == "node.delta" for e in events), "deltas must not be stored"
sse = open(sys.argv[2]).read()
deltas = [json.loads(line[6:])["delta"] for block in sse.split("\n\n") if "event: delta" in block
          for line in block.splitlines() if line.startswith("data: ")]
assert deltas, "the model after the approval streamed no live delta frames"
assert "".join(deltas) == run["output"], ("".join(deltas), run["output"])
print(f"subflow + approval ok; {len(deltas)} live delta frame(s) reassemble the reply")
EOF

step "judge refuses honestly under the mock model"
call PUT "/agent-designs/$design/fixtures" "{\"revision\":1,\"cases\":[{\"id\":\"judged\",\"input\":\"hello\",\"expected\":{\"judge\":{\"criteria\":[\"safety\"]}}}]}" >/dev/null
jeval="$(call POST "/agent-designs/$design/evaluations" '{"gate":1}' | json "d['id']")"
for _ in $(seq 1 100); do
  [ "$(call GET "/agent-evaluations/$jeval" | json "d['status']")" = running ] || break; sleep 0.2
done
call GET "/agent-evaluations/$jeval" > "$WORK/judged.json"
json "d['cases'][0]['failures'][0]" < "$WORK/judged.json" | grep -q '^judge_unavailable: ' || fail "mock judge did not refuse: $(cat "$WORK/judged.json")"
[ "$(json "d['passed']" < "$WORK/judged.json")" = False ] || fail "an unjudged case passed the gate"
echo "judge_unavailable reported; gate not passed"

step "deploy needs the managed runtime (honest refusal in this profile)"
code="$(curl -sS -b "$JAR" -H "Origin: $ORIGIN" -o "$WORK/deploy.json" -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data '{}' "$BASE/agent-versions/$version/deploy")"
[ "$code" = 503 ] || fail "deploy without Kubernetes returned $code"

step "overview metrics"
call GET "/agent-designs/overview?project_id=$project" | tee "$WORK/overview.json"; echo
[ "$(json "d['versions']" < "$WORK/overview.json")" = 1 ] || fail "overview versions"

echo
echo "PASS: design $design · version $version · hash ${hash:0:12}"
if [ "${KEEP_RUNNING:-}" = "1" ]; then
  # Leave the stack up for load tests (deploy/tests/load/agent-designer.js);
  # stop it with Ctrl+C or by killing this script.
  echo "READY MLAIOPS_URL=$ORIGIN MLAIOPS_LOCAL_USERNAME=admin MLAIOPS_LOCAL_PASSWORD=$PASSWORD"
  wait
fi
