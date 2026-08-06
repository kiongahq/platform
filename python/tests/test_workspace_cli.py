import importlib.util
import json
import os
import subprocess
import sys
import tomllib
from pathlib import Path
from types import SimpleNamespace

import pytest


MODULE_PATH = Path(__file__).parents[2] / "deploy" / "workspace" / "kionga.py"
REPOSITORY = MODULE_PATH.parents[2]
SPEC = importlib.util.spec_from_file_location("kionga_workspace", MODULE_PATH)
assert SPEC and SPEC.loader
kionga = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(kionga)


EXPECTED_TEMPLATES = {
    "production-ml",
    "distributed-training",
    "production-agent",
    "fullstack-ai",
    "blank-python",
}


def generate(tmp_path: Path, template: str, **options) -> Path:
    target = tmp_path / template
    kionga.create_base(
        target,
        template,
        template,
        "A production test project",
        initialize_git=False,
        **options,
    )
    return target


def run_generated_tests(target: Path) -> subprocess.CompletedProcess[str]:
    environment = os.environ.copy()
    python_paths = [str(target / "src"), str(REPOSITORY / "python")]
    if environment.get("PYTHONPATH"):
        python_paths.append(environment["PYTHONPATH"])
    environment["PYTHONPATH"] = os.pathsep.join(python_paths)
    return subprocess.run(
        [sys.executable, "-m", "pytest", "-q", "tests"],
        cwd=target,
        env=environment,
        check=False,
        capture_output=True,
        text=True,
        timeout=120,
    )


def test_catalog_is_versioned_and_canonical() -> None:
    document = kionga.catalog_document()

    assert document["api_version"] == "kionga.io/v1"
    assert document["catalog_version"]
    assert {item["id"] for item in document["templates"]} == EXPECTED_TEMPLATES
    assert all(item["version"] == "1.0.0" for item in document["templates"])
    assert all(item["required_services"] for item in document["templates"])
    by_id = {item["id"]: item for item in document["templates"]}
    assert by_id["production-ml"]["frameworks"] == (
        "scikit-learn",
        "xgboost",
        "pytorch",
    )
    assert by_id["production-ml"]["accelerators"] == ("cpu", "single-gpu")
    assert by_id["distributed-training"]["frameworks"] == ("pytorch-ddp",)
    assert by_id["distributed-training"]["recommended_profile"] == "gpu"
    assert by_id["fullstack-ai"]["frameworks"] == ("fastapi-ml", "fastapi-agent")


def test_resolve_template_applies_defaults_and_validates_version() -> None:
    resolved = kionga.resolve_template("production-ml")

    assert resolved.framework == "scikit-learn"
    assert resolved.accelerator == "cpu"
    assert resolved.profile == "power"
    with pytest.raises(ValueError, match="requested version"):
        kionga.resolve_template("production-ml", version="99.0.0")


@pytest.mark.parametrize(
    ("options", "message"),
    [
        ({"framework": "langgraph"}, "framework"),
        ({"accelerator": "quantum"}, "accelerator"),
        ({"profile": "tiny"}, "profile"),
    ],
)
def test_template_options_fail_closed(options: dict[str, str], message: str) -> None:
    with pytest.raises(ValueError, match=message):
        kionga.resolve_template("production-ml", **options)


def test_production_ml_accelerators_match_real_framework_support() -> None:
    with pytest.raises(ValueError, match="only the cpu accelerator"):
        kionga.resolve_template(
            "production-ml",
            framework="scikit-learn",
            accelerator="single-gpu",
        )
    with pytest.raises(ValueError, match="distributed-training"):
        kionga.resolve_template(
            "production-ml",
            framework="pytorch",
            accelerator="multi-gpu",
        )


def test_distributed_training_and_custom_profile_match_api_contract() -> None:
    resolved = kionga.resolve_template(
        "distributed-training",
        framework="pytorch-ddp",
        accelerator="multi-gpu",
        profile="gpu",
    )
    assert resolved.profile == "gpu"
    assert resolved.accelerator == "multi-gpu"
    custom = kionga.resolve_template("production-ml", profile="custom")
    assert custom.profile == "custom"


def test_custom_profile_defers_resource_values_to_administrator(tmp_path: Path) -> None:
    target = generate(tmp_path, "production-ml", profile="custom")

    request = json.loads((target / "platform/project.json").read_text())
    pipeline = json.loads((target / "platform/pipeline.json").read_text())
    assert request["requested_profile"] == "custom"
    assert pipeline["jobs"][0]["resources"] == {}


@pytest.mark.parametrize("template", sorted(EXPECTED_TEMPLATES))
def test_every_template_generates_the_delivery_contract(tmp_path: Path, template: str) -> None:
    target = generate(tmp_path, template)

    required = {
        "README.md",
        "pyproject.toml",
        "Dockerfile",
        ".dockerignore",
        ".env.example",
        ".github/workflows/ci.yml",
        ".kionga/template.json",
        "platform/project.yaml",
        "platform/project.json",
        "tests/test_smoke.py",
    }
    assert required <= {
        str(path.relative_to(target)) for path in target.rglob("*") if path.is_file()
    }
    metadata = json.loads((target / ".kionga/template.json").read_text())
    assert metadata["template"] == {"id": template, "version": "1.0.0"}
    assert metadata["runtime"]["profile"] in kionga.RESOURCE_PROFILES
    assert "secret" not in json.dumps(metadata).lower()
    project_request = json.loads((target / "platform/project.json").read_text())
    assert project_request["template_version"] == "1.0.0"

    pyproject = tomllib.loads((target / "pyproject.toml").read_text())
    assert pyproject["project"]["requires-python"] == ">=3.11,<3.14"
    assert "pytest>=8,<9" in pyproject["project"]["optional-dependencies"]["dev"]

    for source in target.rglob("*.py"):
        compile(source.read_text(), str(source), "exec")


def test_production_ml_generates_real_training_serving_and_pipeline(tmp_path: Path) -> None:
    target = generate(tmp_path, "production-ml")
    package = target / "src/production_ml"

    training = (package / "train.py").read_text()
    assert "LogisticRegression" in training
    assert "def register" in training
    assert "def publish_model" in training
    assert "mlflow_flavor.log_model" in training
    assert "sk_model=model" in training
    assert "mlflow.get_artifact_uri" in training
    assert '"serving_image": os.getenv("KIONGA_SERVING_IMAGE", "")' in training
    assert "metrics.json" in training
    assert "accuracy\": 0.0" not in training
    serving = (package / "serve.py").read_text()
    assert '@app.post("/predict")' in serving
    assert "def load_model" in serving
    assert "def deserialize_model_uri" in serving
    assert "mlflow_flavor.load_model" in serving
    assert "KIONGA_MODEL_URI" in serving
    assert "sum(payload.values) / len(payload.values)" in serving
    assert "model_score(model, feature)" in serving
    pipeline = json.loads((target / "platform/pipeline.json").read_text())
    assert pipeline["execution_mode"] == "prefect"
    assert pipeline["jobs"][0]["name"] == "train-evaluate-register"
    assert pipeline["jobs"][0]["kind"] == "container"
    assert pipeline["jobs"][0]["environment"]["KIONGA_ARTIFACT_DIR"] == (
        "/tmp/kionga-artifacts"
    )
    assert pipeline["jobs"][0]["environment"]["KIONGA_SERVING_IMAGE"] == (
        "ghcr.io/your-org/production-ml:latest"
    )
    assert "MLFLOW_TRACKING_URI" not in pipeline["jobs"][0]["environment"]
    environment = (target / ".env.example").read_text()
    assert "MLFLOW_TRACKING_URI=http://mlflow:5000" in environment
    assert "KIONGA_MODEL_URI=" in environment
    assert "KIONGA_ARTIFACT_URI" not in environment


def test_production_ml_can_select_pytorch(tmp_path: Path) -> None:
    target = generate(
        tmp_path,
        "production-ml",
        framework="pytorch",
        accelerator="single-gpu",
        profile="gpu",
    )

    training = (target / "src/production_ml/train.py").read_text()
    serving = (target / "src/production_ml/serve.py").read_text()
    assert "torch.optim.Adam" in training
    assert 'return torch.device("cuda:0")' in training
    assert 'return torch.device("cuda:0")' in serving
    assert "single-gpu selected but CUDA is not available" in training
    metadata = json.loads((target / ".kionga/template.json").read_text())
    assert metadata["runtime"]["accelerator"] == "single-gpu"
    assert metadata["resources"]["gpu"] == 1


def test_production_ml_xgboost_single_gpu_selects_cuda(tmp_path: Path) -> None:
    target = generate(
        tmp_path,
        "production-ml",
        framework="xgboost",
        accelerator="single-gpu",
        profile="gpu",
    )

    training = (target / "src/production_ml/train.py").read_text()
    serving = (target / "src/production_ml/serve.py").read_text()
    assert 'return "cuda"' in training
    assert 'device=selected_device()' in training
    assert "xgb_model=model" in training
    assert "from mlflow import xgboost as mlflow_flavor" in serving


def test_distributed_template_has_ddp_resume_amp_and_rank_zero_tracking(tmp_path: Path) -> None:
    target = generate(
        tmp_path,
        "distributed-training",
        framework="pytorch-ddp",
        accelerator="multi-gpu",
        profile="gpu",
    )

    training = (target / "src/distributed_training/train.py").read_text()
    assert "DistributedDataParallel" in training
    assert "torch.autocast" in training
    assert "RESUME_FROM_CHECKPOINT" in training
    assert 'if rank == 0 and os.getenv("MLFLOW_TRACKING_URI")' in training
    assert 'world_size=1 is the CPU smoke path' in training
    assert "torchrun" in (target / "scripts/launch-training.sh").read_text()

    manifest = json.loads((target / "platform/pipeline.json").read_text())
    assert manifest["execution_mode"] == "prefect"
    assert manifest["jobs"][0]["kind"] == "container"
    assert manifest["jobs"][0]["environment"]["KIONGA_FRAMEWORK"] == "pytorch-ddp"
    assert manifest["jobs"][0]["resources"]["gpu"] == 2
    assert manifest["jobs"][0]["command"][0] == "torchrun"
    assert manifest["jobs"][0]["command"][2] == "--nproc-per-node=2"
    assert "${WORKERS_PER_NODE:-2}" in (
        target / "scripts/launch-training.sh"
    ).read_text()


def test_single_gpu_distributed_launch_uses_one_worker(tmp_path: Path) -> None:
    target = generate(
        tmp_path,
        "distributed-training",
        accelerator="single-gpu",
        profile="gpu",
    )

    pipeline = json.loads((target / "platform/pipeline.json").read_text())
    assert pipeline["jobs"][0]["resources"]["gpu"] == 1
    assert pipeline["jobs"][0]["command"][2] == "--nproc-per-node=1"
    assert "${WORKERS_PER_NODE:-1}" in (
        target / "scripts/launch-training.sh"
    ).read_text()


@pytest.mark.parametrize(
    ("template", "framework"),
    [("production-agent", "langgraph"), ("fullstack-ai", "fastapi-agent")],
)
def test_agent_templates_generate_graph_runtime_evals_and_manifest(
    tmp_path: Path, template: str, framework: str
) -> None:
    target = generate(tmp_path, template, framework=framework)
    package = template.replace("-", "_")

    graph = (target / f"src/{package}/agent.py").read_text()
    assert "StateGraph(MessagesState)" in graph
    assert "NotImplementedError" not in graph
    assert "checkpointer=checkpointer" in graph
    assert "from langgraph.graph.message import MessagesState" in graph
    assert 'os.getenv("MLAIOPS_LLM_BACKEND", "mock")' in graph
    runtime = (target / f"src/{package}/runtime.py").read_text()
    assert '@app.post("/invoke")' in runtime
    assert '@app.get("/healthz")' in runtime
    assert (target / "evals/golden.jsonl").read_text().count("\n") == 2
    assert "def evaluate" in (target / "evals/run.py").read_text()
    manifest = (target / "platform/agent.yaml").read_text()
    profile_name = kionga.TEMPLATE_CATALOG[template].recommended_profile
    profile = kionga.RESOURCE_PROFILES[profile_name]
    assert "graphModule:" in manifest
    assert "backend: mock" in manifest
    assert "replicas:\n    min: 1\n    max: 1" in manifest
    resource_values = f'cpu: "{profile.cpu}"\n      memory: "{profile.memory}"'
    assert f"requests:\n      {resource_values}" in manifest
    assert f"limits:\n      {resource_values}" in manifest
    environment = (target / ".env.example").read_text()
    assert "MLAIOPS_LLM_BACKEND=mock" in environment
    request = json.loads((target / "platform/agent.json").read_text())
    assert request == {
        "project_id": "replace-with-project-id",
        "name": template,
        "version": "0.1.0",
        "image": f"ghcr.io/your-org/{template}:latest",
        "graph_module": f"{package}.agent:build",
        "llm_backend": "mock",
        "replicas": 1,
        "autoscaling": {"min_replicas": 1, "max_replicas": 1},
        "resources": {
            "cpu": profile.cpu,
            "memory": profile.memory,
            "gpu": 0,
            "gpu_type": "",
        },
        "tools": [],
    }
    dependencies = tomllib.loads((target / "pyproject.toml").read_text())["project"][
        "dependencies"
    ]
    assert "langgraph>=1.0,<2" in dependencies
    assert "langchain-core>=1.0,<2" in dependencies


def test_agent_manifest_requests_and_limits_accelerator_resources(tmp_path: Path) -> None:
    target = generate(
        tmp_path,
        "production-agent",
        accelerator="single-gpu",
        profile="gpu",
    )

    manifest = (target / "platform/agent.yaml").read_text()
    assert manifest.count('nvidia.com/gpu: "1"') == 2
    assert 'cpu: "8"' in manifest
    assert 'memory: "32Gi"' in manifest


def test_custom_agent_profile_defers_resource_values(tmp_path: Path) -> None:
    target = generate(tmp_path, "production-agent", profile="custom")

    assert "resources:" not in (target / "platform/agent.yaml").read_text()


@pytest.mark.parametrize(
    ("template", "framework"),
    [("production-agent", "langgraph"), ("fullstack-ai", "fastapi-agent")],
)
def test_generated_agent_project_tests_pass_offline(
    tmp_path: Path, template: str, framework: str
) -> None:
    target = generate(tmp_path, template, framework=framework)

    result = run_generated_tests(target)

    assert result.returncode == 0, result.stdout + result.stderr


def test_fullstack_ai_defaults_to_ml_browser_client(tmp_path: Path) -> None:
    target = generate(tmp_path, "fullstack-ai")

    page = (target / "src/fullstack_ai/web/index.html").read_text()
    assert 'fetch("/predict"' in page
    assert "aria-live" in page
    assert '@app.get("/")' in (target / "src/fullstack_ai/serve.py").read_text()
    assert (target / "platform/pipeline.yaml").exists()
    assert (target / "functions/event_handler.py").exists()
    function = json.loads((target / "platform/function.json").read_text())
    assert function["name"] == "fullstack-ai-events"
    assert function["annotations"]["topic"] == "fullstack-ai.events"


def test_fullstack_ml_single_gpu_selects_a_cuda_capable_runtime(tmp_path: Path) -> None:
    target = generate(
        tmp_path,
        "fullstack-ai",
        framework="fastapi-ml",
        accelerator="single-gpu",
        profile="gpu",
    )

    dependencies = tomllib.loads((target / "pyproject.toml").read_text())["project"][
        "dependencies"
    ]
    assert "torch>=2.4,<3" in dependencies
    assert not any(dependency.startswith("scikit-learn") for dependency in dependencies)
    training = (target / "src/fullstack_ai/train.py").read_text()
    assert 'DEFAULT_ACCELERATOR = "single-gpu"' in training
    assert 'return torch.device("cuda:0")' in training


def test_fullstack_ai_agent_mode_uses_invoke_client(tmp_path: Path) -> None:
    target = generate(tmp_path, "fullstack-ai", framework="fastapi-agent")

    page = (target / "src/fullstack_ai/web/index.html").read_text()
    assert 'fetch("/invoke"' in page
    assert (target / "platform/agent.yaml").exists()
    assert '@app.post("/")' in (target / "functions/event_handler.py").read_text()


@pytest.mark.parametrize("template", ["production-ml", "fullstack-ai"])
def test_generated_ml_project_tests_train_publish_materialize_and_serve(
    tmp_path: Path, template: str
) -> None:
    target = generate(tmp_path, template)

    result = run_generated_tests(target)

    assert result.returncode == 0, result.stdout + result.stderr


def test_generated_pytorch_project_tests_use_the_real_artifact_on_cpu(
    tmp_path: Path,
) -> None:
    target = generate(
        tmp_path,
        "production-ml",
        framework="pytorch",
        accelerator="cpu",
    )

    result = run_generated_tests(target)

    assert result.returncode == 0, result.stdout + result.stderr


def test_blank_template_is_container_and_cli_ready(tmp_path: Path) -> None:
    target = generate(tmp_path, "blank-python")

    assert "def greeting" in (target / "src/blank_python/cli.py").read_text()
    assert 'blank-python = "blank_python.cli:main"' in (target / "pyproject.toml").read_text()
    assert "USER 65532:65532" in (target / "Dockerfile").read_text()


def test_scaffolder_refuses_non_empty_target_without_modifying_it(tmp_path: Path) -> None:
    target = tmp_path / "existing"
    target.mkdir()
    sentinel = target / "keep.txt"
    sentinel.write_text("owned by the user")

    with pytest.raises(RuntimeError, match="not empty"):
        kionga.create_base(
            target,
            "existing",
            "blank-python",
            initialize_git=False,
        )

    assert sentinel.read_text() == "owned by the user"
    assert list(target.iterdir()) == [sentinel]


@pytest.mark.parametrize("precreate_destination", [False, True])
def test_scaffolder_cleans_staging_after_generation_failure(
    tmp_path: Path, monkeypatch, precreate_destination: bool
) -> None:
    target = tmp_path / "atomic-project"
    if precreate_destination:
        target.mkdir()
    original_write_file = kionga.write_file
    writes = 0

    def fail_mid_generation(path: Path, content: str) -> None:
        nonlocal writes
        writes += 1
        if writes == 4:
            raise RuntimeError("injected generation failure")
        original_write_file(path, content)

    monkeypatch.setattr(kionga, "write_file", fail_mid_generation)

    with pytest.raises(RuntimeError, match="injected generation failure"):
        kionga.create_base(
            target,
            "atomic-project",
            "blank-python",
            initialize_git=False,
        )

    assert target.exists() is precreate_destination
    if target.exists():
        assert list(target.iterdir()) == []
    assert list(tmp_path.glob(".atomic-project.kionga-staging-*")) == []


def test_scaffold_stays_inside_workspace_and_uses_canonical_options(
    tmp_path: Path, monkeypatch, capsys
) -> None:
    monkeypatch.setattr(kionga, "WORKSPACE", tmp_path)
    monkeypatch.setattr(kionga.shutil, "which", lambda _: None)
    args = SimpleNamespace(
        name="risk platform",
        template="production-ml",
        template_version="1.0.0",
        framework="scikit-learn",
        accelerator="cpu",
        profile="power",
        prompt="Predict risk safely",
        agent="none",
    )

    kionga.scaffold(args)

    assert (tmp_path / "risk-platform/.kionga/template.json").exists()
    assert "Created" in capsys.readouterr().out


def test_numeric_project_name_gets_valid_python_package(tmp_path: Path) -> None:
    target = tmp_path / "123-risk"
    kionga.create_base(
        target,
        "123-risk",
        "blank-python",
        initialize_git=False,
    )

    assert (target / "src/project_123_risk/__init__.py").exists()


def test_agent_command_preserves_workspace_boundary(tmp_path: Path) -> None:
    command = kionga.agent_command("codex", tmp_path, "Complete it")

    assert command[:4] == ["codex", "exec", "--sandbox", "workspace-write"]
    assert command[command.index("-C") + 1] == str(tmp_path)
    assert "Do not access credentials" in command[-1]


def test_project_sync_clones_connected_repository(tmp_path, monkeypatch) -> None:
    calls = []
    monkeypatch.setattr(kionga, "WORKSPACE", tmp_path)
    monkeypatch.setattr(
        kionga,
        "api_get",
        lambda path: {
            "namespace": "fraud-model",
            "repository": {
                "url": "https://github.com/acme/fraud-model.git",
                "default_branch": "main",
            },
        },
    )
    monkeypatch.setattr(kionga.shutil, "which", lambda _: "/usr/bin/git")
    monkeypatch.setattr(
        kionga.subprocess,
        "run",
        lambda command, **kwargs: calls.append((command, kwargs))
        or SimpleNamespace(stdout=""),
    )

    kionga.project_sync(SimpleNamespace(project_id="prj/one"))

    assert calls[0][0] == [
        "git",
        "clone",
        "--single-branch",
        "--branch",
        "main",
        "https://github.com/acme/fraud-model.git",
        str(tmp_path / "fraud-model"),
    ]


def test_project_sync_rejects_workspace_escape(tmp_path, monkeypatch) -> None:
    monkeypatch.setattr(kionga, "WORKSPACE", tmp_path)
    monkeypatch.setattr(
        kionga,
        "api_get",
        lambda _: {
            "namespace": "../outside",
            "repository": {
                "url": "https://github.com/acme/fraud-model.git",
                "default_branch": "main",
            },
        },
    )

    with pytest.raises(RuntimeError, match="escaped"):
        kionga.project_sync(SimpleNamespace(project_id="prj-one"))
