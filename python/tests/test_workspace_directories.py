import importlib.util
from pathlib import Path

import pytest

source = Path(__file__).resolve().parents[2] / "deploy/workspace/directories.py"
spec = importlib.util.spec_from_file_location("workspace_directories", source)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def test_project_directory_creation_preserves_existing_files(tmp_path):
    folder = module.prepare_project(tmp_path, "my-project")
    (folder / "model.txt").write_text("keep me")
    assert module.prepare_project(tmp_path, "my-project") == folder
    assert (folder / "model.txt").read_text() == "keep me"


@pytest.mark.parametrize("namespace", ["../outside", "/tmp/outside", "a/b", "", "A", "a" * 64, None])
def test_project_directory_rejects_invalid_names(tmp_path, namespace):
    with pytest.raises(ValueError):
        module.prepare_project(tmp_path, namespace)


def test_project_directory_rejects_symlink_escape(tmp_path):
    root = tmp_path / "workspace"
    root.mkdir()
    outside = tmp_path / "outside"
    outside.mkdir()
    (root / "projects").symlink_to(outside, target_is_directory=True)
    with pytest.raises(ValueError):
        module.prepare_project(root, "project")
    assert list(outside.iterdir()) == []


# ---- structured scaffold jobs ---------------------------------------------

import http.client  # noqa: E402
import json  # noqa: E402
import shutil  # noqa: E402
import threading  # noqa: E402
from http.server import ThreadingHTTPServer  # noqa: E402

KIONGA = Path(__file__).resolve().parents[2] / "deploy/workspace/kionga.py"
ARGV = ["kionga", "scaffold", "churn-model", "--template", "blank-python", "--template-version", "1.0.0",
        "--framework", "python", "--accelerator", "cpu", "--profile", "starter"]


def job(argv=None, namespace="churn-model", **extra):
    return {"job_id": "scf-1", "namespace": namespace, "argv": list(ARGV if argv is None else argv), **extra}


def test_valid_job_returns_exact_argv():
    assert module.validate_job(job()) == ("churn-model", ARGV)


@pytest.mark.parametrize(
    "payload",
    [
        job(namespace="churn-model;id", argv=["kionga", "scaffold", "churn-model;id"]),
        job(argv=["kionga", "scaffold", "other"]),
        job(argv=["sh", "-c", "id"]),
        job(argv=["kionga", "project", "churn-model"]),
        job(argv=[*ARGV, "--agent", "codex"]),
        job(argv=[*ARGV, "--prompt", "hello"]),
        job(argv=["kionga", "scaffold", "churn-model", "--template", "blank-python;id"]),
        job(argv=["kionga", "scaffold", "churn-model", "--template", "$(id)"]),
        job(argv=["kionga", "scaffold", "churn-model", "--template", "`id`"]),
        job(argv=["kionga", "scaffold", "churn-model", "--template", "blank\npython"]),
        job(argv=["kionga", "scaffold", "churn-model", "--template", "../../etc"]),
        job(argv=["kionga", "scaffold", "churn-model", "--template", "blаnk"]),  # Cyrillic a
        job(argv=["kionga", "scaffold", "churn-model", "--template", "ｂlank"]),  # fullwidth b
        job(argv=["kionga", "scaffold", "churn-model", "--template", "blank‮python"]),
        job(argv=["kionga", "scaffold", "churn-model", "--template", "--profile"]),
        job(argv=["kionga", "scaffold", "churn-model", "--template"]),
        job(argv=["kionga", "scaffold", "churn-model", "--template", "a", "--template", "b"]),
        job(argv=["kionga", "scaffold", "churn-model", "--template=blank-python"]),
        job(namespace="../x", argv=["kionga", "scaffold", "../x"]),
        job(namespace="abс", argv=["kionga", "scaffold", "abс"]),
        job(argv="kionga scaffold churn-model"),
        job(argv=["kionga", "scaffold", "churn-model", 7]),
        job(command="rm -rf /"),
        {"namespace": "churn-model", "argv": ARGV},
        job(job_id="x; rm"),
        [],
    ],
)
def test_job_validation_rejects_injection(payload):
    with pytest.raises(ValueError):
        module.validate_job(payload)


def test_scaffold_runs_real_generator_and_reports_files_and_git(tmp_path):
    result = module.run_scaffold(tmp_path, job(), cli=str(KIONGA))
    assert result["exit_code"] == 0, result["output_tail"]
    folder = tmp_path / "projects" / "churn-model"
    assert result["folder"] == str(folder.resolve())
    assert "README.md" in result["files"] and "pyproject.toml" in result["files"]
    assert not any(name.startswith(".git/") for name in result["files"])
    if shutil.which("git"):
        assert "?? README.md" in result["git_status"]
    again = module.run_scaffold(tmp_path, job(), cli=str(KIONGA))
    assert again["exit_code"] != 0
    assert "not empty" in again["output_tail"]


def test_scaffold_child_gets_exact_argv_and_no_secrets(tmp_path, monkeypatch):
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "aws-secret-value-123")
    monkeypatch.setenv("KIONGA_WORKSPACE_JOB_TOKEN", "job-token-value-456")
    fake = tmp_path / "fake_kionga.py"
    fake.write_text(
        "import json, os, sys\n"
        "print(json.dumps({'argv': sys.argv[1:], 'env': sorted(os.environ), 'cwd': os.getcwd(),"
        " 'workspace': os.environ['KIONGA_WORKSPACE']}))\n"
        "print('leak attempt Bearer abc.def token=xyz aws-secret-value-123', file=sys.stderr)\n"
    )
    root = tmp_path / "workspace"
    result = module.run_scaffold(root, job(), cli=str(fake))
    report = json.loads(result["output_tail"].splitlines()[0])
    assert report["argv"] == ARGV[1:]
    assert set(report["env"]) <= {"PATH", "LANG", "HOME", "KIONGA_WORKSPACE", "LC_CTYPE", "__CF_USER_TEXT_ENCODING"}
    assert report["cwd"] == str((root / "projects").resolve())
    assert report["workspace"] == str((root / "projects").resolve())
    assert "aws-secret-value-123" not in result["output_tail"]
    assert "abc.def" not in result["output_tail"] and "xyz" not in result["output_tail"]


def test_scaffold_times_out(tmp_path):
    slow = tmp_path / "slow.py"
    slow.write_text("import time\nprint('started', flush=True)\ntime.sleep(5)\n")
    result = module.run_scaffold(tmp_path / "w", job(), cli=str(slow), timeout=1)
    assert result["exit_code"] == 124
    assert "timed out" in result["output_tail"]


@pytest.fixture
def sidecar(tmp_path, monkeypatch):
    monkeypatch.setenv("KIONGA_WORKSPACE", str(tmp_path))
    monkeypatch.setenv("KIONGA_CLI", str(KIONGA))
    monkeypatch.setenv("KIONGA_WORKSPACE_JOB_TOKEN", "job-token")
    server = ThreadingHTTPServer(("127.0.0.1", 0), module.Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()

    def post(path, body, token="job-token"):
        connection = http.client.HTTPConnection("127.0.0.1", server.server_address[1], timeout=30)
        payload = body if isinstance(body, bytes) else json.dumps(body).encode()
        headers = {"Content-Type": "application/json"}
        if token is not None:
            headers["X-Kionga-Workspace-Token"] = token
        connection.request("POST", path, payload, headers)
        response = connection.getresponse()
        data = response.read()
        connection.close()
        return response.status, json.loads(data) if data.startswith(b"{") else data

    yield post
    server.shutdown()


def test_sidecar_endpoint_authenticates_and_runs(sidecar, tmp_path):
    assert sidecar("/scaffold-jobs", job(), token=None)[0] == 401
    assert sidecar("/scaffold-jobs", job(), token="wrong")[0] == 401
    assert sidecar("/scaffold-jobs", b"not json")[0] == 400
    status, body = sidecar("/scaffold-jobs", job(argv=[*ARGV, "--agent", "codex"]))
    assert status == 422 and "--agent" in body["error"]
    status, body = sidecar("/scaffold-jobs", job())
    assert status == 200, body
    assert body["exit_code"] == 0 and "README.md" in body["files"]
    assert (tmp_path / "projects" / "churn-model" / "README.md").exists()
    assert sidecar("/other", job())[0] == 404
    assert sidecar("/prepare-project", {"namespace": "second"})[0] == 200


def test_sidecar_disabled_without_token(sidecar, monkeypatch):
    for name in ("KIONGA_WORKSPACE_JOB_TOKEN", "JUPYTER_TOKEN", "PASSWORD"):
        monkeypatch.delenv(name, raising=False)
    status, body = sidecar("/scaffold-jobs", job(), token="")
    assert status == 503 and "KIONGA_WORKSPACE_JOB_TOKEN" in body["error"]
