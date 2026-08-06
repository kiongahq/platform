#!/usr/bin/env python3
"""Kionga project scaffolder and Git-native workspace helper.

The scaffolder intentionally has no third-party runtime dependency. Templates
are described by one versioned catalog and generated deterministically so the
console, SDK, CLI, and future remote scaffold workers can share the contract.
No credentials are accepted or written by the generator.
"""

import argparse
import hashlib
import json
import os
import re
import shlex
import shutil
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

WORKSPACE = Path(os.getenv("KIONGA_WORKSPACE", "/workspace")).resolve()
KIONGA_URL = os.getenv("MLAIOPS_URL", "http://gateway:8080").rstrip("/")
CATALOG_API_VERSION = "kionga.io/v1"
CATALOG_VERSION = "2026.1"


@dataclass(frozen=True)
class ResourceProfile:
    cpu: str
    memory: str
    storage_gb: int
    gpu: int = 0


RESOURCE_PROFILES = {
    "starter": ResourceProfile(cpu="2", memory="4Gi", storage_gb=25),
    "team": ResourceProfile(cpu="4", memory="8Gi", storage_gb=100),
    "power": ResourceProfile(cpu="8", memory="16Gi", storage_gb=250),
    "gpu": ResourceProfile(cpu="8", memory="32Gi", storage_gb=500, gpu=1),
    "custom": ResourceProfile(cpu="", memory="", storage_gb=0),
}

ACCELERATOR_RESOURCES = {
    "cpu": ("", 0),
    "single-gpu": ("nvidia.com/gpu", 1),
    "multi-gpu": ("nvidia.com/gpu", 2),
}


@dataclass(frozen=True)
class TemplateSpec:
    identifier: str
    version: str
    kind: str
    title: str
    category: str
    description: str
    frameworks: tuple[str, ...]
    accelerators: tuple[str, ...]
    capabilities: tuple[str, ...]
    required_services: tuple[str, ...]
    recommended_profile: str


TEMPLATE_CATALOG: dict[str, TemplateSpec] = {
    "production-ml": TemplateSpec(
        identifier="production-ml",
        version="1.0.0",
        kind="ml",
        title="Production ML system",
        category="Machine learning",
        description="Train, validate, track, register, serve, and monitor a production model.",
        frameworks=("scikit-learn", "xgboost", "pytorch"),
        accelerators=("cpu", "single-gpu"),
        capabilities=(
            "data-contracts",
            "training",
            "evaluation-gates",
            "mlflow",
            "batch-inference",
            "online-serving",
            "monitoring",
        ),
        required_services=("pipelines", "models", "features", "storage", "git", "workbench"),
        recommended_profile="power",
    ),
    "distributed-training": TemplateSpec(
        identifier="distributed-training",
        version="1.0.0",
        kind="ml",
        title="Distributed deep learning",
        category="Machine learning",
        description="Multi-GPU training with checkpoints, mixed precision, distributed launch, and reproducible evaluation.",
        frameworks=("pytorch-ddp",),
        accelerators=("single-gpu", "multi-gpu"),
        capabilities=(
            "distributed-training",
            "gpu",
            "checkpointing",
            "mixed-precision",
            "mlflow",
            "model-serving",
        ),
        required_services=("pipelines", "models", "storage", "git", "workbench"),
        recommended_profile="gpu",
    ),
    "production-agent": TemplateSpec(
        identifier="production-agent",
        version="1.0.0",
        kind="agent",
        title="Production AI agent",
        category="Agentic AI",
        description="A stateful LangGraph agent with tools, memory, tracing, evaluation, and an HTTP runtime.",
        frameworks=("langgraph",),
        accelerators=("cpu", "single-gpu"),
        capabilities=(
            "agent-graph",
            "tools",
            "checkpoints",
            "semantic-memory",
            "langfuse",
            "golden-evals",
            "runtime-api",
        ),
        required_services=("agents", "storage", "git", "workbench"),
        recommended_profile="team",
    ),
    "fullstack-ai": TemplateSpec(
        identifier="fullstack-ai",
        version="1.0.0",
        kind="application",
        title="Full-stack AI application",
        category="Application",
        description="Model or agent backend, governed API, event function, browser client, containers, CI, and tests.",
        frameworks=("fastapi-ml", "fastapi-agent"),
        accelerators=("cpu", "single-gpu"),
        capabilities=(
            "api",
            "web-client",
            "model-or-agent",
            "event-function",
            "containers",
            "ci",
        ),
        required_services=("models", "agents", "functions", "storage", "git", "workbench", "ide"),
        recommended_profile="power",
    ),
    "blank-python": TemplateSpec(
        identifier="blank-python",
        version="1.0.0",
        kind="application",
        title="Blank expert workspace",
        category="Foundation",
        description="A typed, container-ready Python package with testing, linting, CI, and platform configuration.",
        frameworks=("python",),
        accelerators=("cpu", "single-gpu", "multi-gpu"),
        capabilities=("python-package", "containers", "ci", "tests"),
        required_services=("git", "workbench"),
        recommended_profile="starter",
    ),
}


@dataclass(frozen=True)
class ResolvedTemplate:
    spec: TemplateSpec
    framework: str
    accelerator: str
    profile: str


def slug(value: str) -> str:
    cleaned = re.sub(r"[^a-z0-9]+", "-", value.lower()).strip("-")
    if not cleaned:
        raise ValueError("project name must contain letters or numbers")
    if len(cleaned) > 63:
        raise ValueError("project name must be at most 63 characters after normalization")
    return cleaned


def package_name(project_name: str) -> str:
    package = project_name.replace("-", "_")
    return package if not package[0].isdigit() else f"project_{package}"


def derived_name(project_name: str, suffix: str) -> str:
    candidate = f"{project_name}-{suffix}"
    if len(candidate) <= 63:
        return candidate
    digest = hashlib.sha256(candidate.encode()).hexdigest()[:7]
    prefix = project_name[: 63 - len(suffix) - len(digest) - 2].rstrip("-")
    return f"{prefix}-{suffix}-{digest}"


def catalog_document() -> dict[str, Any]:
    templates = []
    for spec in TEMPLATE_CATALOG.values():
        item = asdict(spec)
        item["id"] = item.pop("identifier")
        item["name"] = item.pop("title")
        item.pop("kind")
        templates.append(item)
    return {
        "api_version": CATALOG_API_VERSION,
        "catalog_version": CATALOG_VERSION,
        "templates": templates,
    }


def resolve_template(
    template: str,
    *,
    version: str = "",
    framework: str = "",
    accelerator: str = "",
    profile: str = "",
) -> ResolvedTemplate:
    try:
        spec = TEMPLATE_CATALOG[template]
    except KeyError as error:
        supported = ", ".join(TEMPLATE_CATALOG)
        raise ValueError(f"unknown template {template!r}; choose one of: {supported}") from error
    if version and version != spec.version:
        raise ValueError(
            f"template {template!r} has version {spec.version}; requested version {version}"
        )
    framework = framework or spec.frameworks[0]
    accelerator = accelerator or spec.accelerators[0]
    profile = profile or spec.recommended_profile
    if framework not in spec.frameworks:
        raise ValueError(
            f"framework {framework!r} is not supported by {template}; "
            f"choose one of: {', '.join(spec.frameworks)}"
        )
    if template == "production-ml" and accelerator == "multi-gpu":
        raise ValueError("multi-GPU training requires the distributed-training template")
    if accelerator not in spec.accelerators:
        raise ValueError(
            f"accelerator {accelerator!r} is not supported by {template}; "
            f"choose one of: {', '.join(spec.accelerators)}"
        )
    if profile not in RESOURCE_PROFILES:
        raise ValueError(
            f"profile {profile!r} is not supported; "
            f"choose one of: {', '.join(RESOURCE_PROFILES)}"
        )
    if template == "production-ml":
        if framework == "scikit-learn" and accelerator != "cpu":
            raise ValueError("scikit-learn production projects support only the cpu accelerator")
    return ResolvedTemplate(spec, framework, accelerator, profile)


def write_file(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content.rstrip() + "\n", encoding="utf-8")


def _toml_array(values: list[str]) -> str:
    return json.dumps(values)


def ml_framework_for(resolved: ResolvedTemplate) -> str:
    if resolved.spec.identifier == "production-ml":
        return resolved.framework
    if resolved.spec.identifier == "fullstack-ai" and resolved.framework == "fastapi-ml":
        return "scikit-learn" if resolved.accelerator == "cpu" else "pytorch"
    raise ValueError(f"{resolved.spec.identifier} is not an ML serving template")


def model_filename(framework: str) -> str:
    return {
        "scikit-learn": "model.joblib",
        "xgboost": "model.json",
        "pytorch": "model.pt",
    }[framework]


def dependencies_for(resolved: ResolvedTemplate) -> list[str]:
    template, framework = resolved.spec.identifier, resolved.framework
    dependencies: list[str] = []
    if template == "production-ml":
        dependencies.extend(
            [
                "fastapi>=0.115,<1",
                "httpx>=0.27,<1",
                "mlflow>=3,<4",
                "pydantic>=2.7,<3",
                "uvicorn[standard]>=0.34,<1",
            ]
        )
        framework_dependencies = {
            "scikit-learn": "scikit-learn>=1.5,<2",
            "xgboost": "xgboost>=2.1,<4",
            "pytorch": "torch>=2.4,<3",
        }
        dependencies.append(framework_dependencies[framework])
    elif template == "distributed-training":
        dependencies.extend(["httpx>=0.27,<1", "mlflow>=3,<4", "torch>=2.4,<3"])
    elif template == "fullstack-ai" and framework == "fastapi-ml":
        dependencies.extend(
            [
                "fastapi>=0.115,<1",
                "httpx>=0.27,<1",
                "mlflow>=3,<4",
                "pydantic>=2.7,<3",
                "uvicorn[standard]>=0.34,<1",
            ]
        )
        if ml_framework_for(resolved) == "scikit-learn":
            dependencies.extend(["joblib>=1.4,<2", "scikit-learn>=1.5,<2"])
        else:
            dependencies.append("torch>=2.4,<3")
    elif template in {"production-agent", "fullstack-ai"}:
        dependencies.extend(
            [
                "fastapi>=0.115,<1",
                "langchain-core>=1.0,<2",
                "langgraph>=1.0,<2",
                "mlaiops-sdk[agents]>=0.2,<1",
                "pydantic>=2.7,<3",
                "uvicorn[standard]>=0.34,<1",
            ]
        )
    if template == "production-ml" and framework == "scikit-learn":
        dependencies.append("joblib>=1.4,<2")
    return dependencies


def project_entrypoint(resolved: ResolvedTemplate, package: str) -> tuple[str, str]:
    template = resolved.spec.identifier
    if template == "production-ml":
        return f"{package}.serve:app", "service"
    if template == "fullstack-ai" and resolved.framework == "fastapi-ml":
        return f"{package}.serve:app", "service"
    if template in {"production-agent", "fullstack-ai"}:
        return f"{package}.runtime:app", "service"
    if template == "distributed-training":
        return f"python -m {package}.train", "job"
    return f"python -m {package}.cli", "job"


def _pyproject(name: str, package: str, resolved: ResolvedTemplate) -> str:
    dependencies = dependencies_for(resolved)
    scripts = ""
    if resolved.spec.identifier in {"production-ml", "distributed-training"}:
        scripts = f'\n[project.scripts]\n{name}-train = "{package}.train:main"\n'
    elif resolved.spec.identifier == "blank-python":
        scripts = f'\n[project.scripts]\n{name} = "{package}.cli:main"\n'
    return f'''[build-system]
requires = ["setuptools>=69,<81"]
build-backend = "setuptools.build_meta"

[project]
name = "{name}"
version = "0.1.0"
description = "Generated from Kionga {resolved.spec.identifier} {resolved.spec.version}"
readme = "README.md"
requires-python = ">=3.11,<3.14"
dependencies = {_toml_array(dependencies)}

[project.optional-dependencies]
dev = ["pytest>=8,<9", "ruff>=0.12,<1"]
{scripts}
[tool.setuptools.packages.find]
where = ["src"]

[tool.pytest.ini_options]
testpaths = ["tests"]

[tool.ruff]
line-length = 100
'''


def _dockerfile(package: str, resolved: ResolvedTemplate) -> str:
    entrypoint, workload = project_entrypoint(resolved, package)
    command = (
        f'["uvicorn", "{entrypoint}", "--host", "0.0.0.0", "--port", "8080"]'
        if workload == "service"
        else json.dumps(entrypoint.split())
    )
    healthcheck = ""
    if workload == "service":
        healthcheck = (
            "HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 "
            "CMD python -c \"import urllib.request; urllib.request.urlopen("
            "'http://127.0.0.1:8080/healthz', timeout=2)\"\n"
        )
    return f'''FROM python:3.11-slim AS builder
WORKDIR /build
COPY pyproject.toml README.md ./
COPY src ./src
RUN python -m pip wheel --no-cache-dir --wheel-dir /wheels .

FROM python:3.11-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
RUN useradd --create-home --uid 65532 app
COPY --from=builder /wheels /wheels
RUN python -m pip install --no-cache-dir /wheels/* && rm -rf /wheels
USER 65532:65532
WORKDIR /app
{healthcheck}CMD {command}
'''


def _ci_workflow() -> str:
    return '''name: verify

on:
  push:
  pull_request:

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with:
          python-version: "3.11"
          cache: pip
      - run: python -m pip install -e ".[dev]"
      - run: ruff check .
      - run: pytest -q
      - run: docker build -t kionga-project:${{ github.sha }} .
'''


def _project_manifest(name: str, resolved: ResolvedTemplate) -> str:
    return f'''name: {name}
description: Generated from {resolved.spec.identifier} {resolved.spec.version}
template: {resolved.spec.identifier}
framework: {resolved.framework}
accelerator: {resolved.accelerator}
requested_profile: {resolved.profile}
'''


def _environment_example(resolved: ResolvedTemplate) -> str:
    values = [
        "MLAIOPS_URL=http://gateway:8080",
        "MLFLOW_TRACKING_URI=http://mlflow:5000",
        "KIONGA_PROJECT_ID=replace-with-project-id",
    ]
    if resolved.spec.identifier in {"production-agent", "fullstack-ai"} and (
        resolved.spec.identifier == "production-agent"
        or resolved.framework == "fastapi-agent"
    ):
        values.extend(
            [
                "MLAIOPS_LLM_BACKEND=mock",
                "MLAIOPS_CHECKPOINT_DSN=",
                "LANGFUSE_PUBLIC_KEY=",
                "LANGFUSE_SECRET_KEY=",
            ]
        )
    if resolved.spec.identifier == "production-ml" or (
        resolved.spec.identifier == "fullstack-ai" and resolved.framework == "fastapi-ml"
    ):
        framework = ml_framework_for(resolved)
        filename = model_filename(framework)
        values.extend(
            [
                f"KIONGA_ACCELERATOR={resolved.accelerator}",
                "KIONGA_ARTIFACT_DIR=artifacts",
                f"KIONGA_MODEL_PATH=artifacts/{filename}",
                "KIONGA_MODEL_URI=",
            ]
        )
    return "\n".join(values) + "\n"


def _pipeline_request(name: str, resolved: ResolvedTemplate) -> dict[str, Any]:
    profile = RESOURCE_PROFILES[resolved.profile]
    _, accelerator_count = ACCELERATOR_RESOURCES[resolved.accelerator]
    resources: dict[str, Any] = {}
    if resolved.profile != "custom":
        resources = {"cpu": profile.cpu, "memory": profile.memory}
    if accelerator_count:
        resources["gpu"] = accelerator_count
    package = package_name(name)
    if resolved.spec.identifier == "distributed-training":
        workers = max(1, accelerator_count)
        command = [
            "torchrun",
            "--standalone",
            f"--nproc-per-node={workers}",
            "-m",
            f"{package}.train",
        ]
        job_name, definition_name = "distributed-train", f"{name}-distributed-training"
    else:
        command = ["python", "-m", f"{package}.train"]
        job_name, definition_name = "train-evaluate-register", f"{name}-training"
    return {
        "project_id": "replace-with-project-id",
        "name": definition_name,
        "version": "1",
        "execution_mode": "prefect",
        "jobs": [
            {
                "name": job_name,
                "kind": "container",
                "image": f"ghcr.io/your-org/{name}:latest",
                "command": command,
                "depends_on": [],
                "environment": {
                    "KIONGA_FRAMEWORK": resolved.framework,
                    "KIONGA_ACCELERATOR": resolved.accelerator,
                    "KIONGA_ARTIFACT_DIR": "/tmp/kionga-artifacts",
                    "KIONGA_SERVING_IMAGE": f"ghcr.io/your-org/{name}:latest",
                },
                "resources": resources,
                "retries": 2,
            }
        ],
    }


def _pipeline_manifest(name: str, resolved: ResolvedTemplate) -> str:
    """YAML form of the exact pipeline-definition API request."""
    request = _pipeline_request(name, resolved)
    job = request["jobs"][0]
    command = ", ".join(json.dumps(value) for value in job["command"])
    resources = job["resources"]
    resource_lines = "\n".join(
        f"      {key}: {json.dumps(value)}" for key, value in resources.items()
    )
    if not resource_lines:
        resource_lines = "      {}"
    return f'''project_id: replace-with-project-id
name: {request["name"]}
version: "1"
execution_mode: prefect
jobs:
  - name: {job["name"]}
    kind: container
    image: {job["image"]}
    command: [{command}]
    depends_on: []
    environment:
      KIONGA_FRAMEWORK: {resolved.framework}
      KIONGA_ACCELERATOR: {resolved.accelerator}
      KIONGA_ARTIFACT_DIR: /tmp/kionga-artifacts
      KIONGA_SERVING_IMAGE: ghcr.io/your-org/{name}:latest
    resources:
{resource_lines}
    retries: 2
'''


def _agent_manifest(name: str, package: str, resolved: ResolvedTemplate) -> str:
    profile = RESOURCE_PROFILES[resolved.profile]
    accelerator_name, accelerator_count = ACCELERATOR_RESOURCES[resolved.accelerator]
    quantities: dict[str, str] = {}
    if resolved.profile != "custom":
        quantities = {"cpu": profile.cpu, "memory": profile.memory}
    if accelerator_count:
        quantities[accelerator_name] = str(accelerator_count)

    resources = ""
    if quantities:
        resource_lines = "\n".join(
            f"      {resource_name}: {json.dumps(quantity)}"
            for resource_name, quantity in quantities.items()
        )
        resources = f'''  resources:
    requests:
{resource_lines}
    limits:
{resource_lines}
'''
    return f'''apiVersion: mlaiops.io/v1alpha1
kind: KiongaAgent
metadata:
  name: {name}
spec:
  version: "0.1.0"
  image: ghcr.io/your-org/{name}:latest
  graphModule: {package}.agent:build
  replicas:
    min: 1
    max: 1
  llm:
    backend: mock
{resources}  langfuseProject: {name}
  trafficPolicy:
    canaryWeight: 0
'''


def _agent_request(name: str, package: str, resolved: ResolvedTemplate) -> dict[str, Any]:
    profile = RESOURCE_PROFILES[resolved.profile]
    accelerator_name, accelerator_count = ACCELERATOR_RESOURCES[resolved.accelerator]
    return {
        "project_id": "replace-with-project-id",
        "name": name,
        "version": "0.1.0",
        "image": f"ghcr.io/your-org/{name}:latest",
        "graph_module": f"{package}.agent:build",
        "llm_backend": "mock",
        "replicas": 1,
        "autoscaling": {"min_replicas": 1, "max_replicas": 1},
        "resources": {
            "cpu": profile.cpu if resolved.profile != "custom" else "",
            "memory": profile.memory if resolved.profile != "custom" else "",
            "gpu": accelerator_count,
            "gpu_type": accelerator_name if accelerator_count else "",
        },
        "tools": [],
    }


def _ml_source(
    package: str, framework: str, accelerator: str = "cpu"
) -> dict[str, str]:
    """Generate training and serving code that shares one durable model artifact."""
    filename = model_filename(framework)
    if framework == "scikit-learn":
        mlflow_flavor = "sklearn"
        mlflow_model_argument = "sk_model"
        train_body = '''import joblib
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import accuracy_score


def train_model():
    features = [[0.0], [0.1], [0.2], [0.35], [0.65], [0.8], [0.9], [1.0]]
    labels = [0, 0, 0, 0, 1, 1, 1, 1]
    model = LogisticRegression(random_state=7).fit(features, labels)
    accuracy = float(accuracy_score(labels, model.predict(features)))
    return model, {"accuracy": accuracy}


def save_model(model, path: Path) -> None:
    temporary = path.with_name(path.name + ".tmp")
    joblib.dump(model, temporary)
    temporary.replace(path)
'''
        load_body = '''import joblib


def deserialize_model(path: Path):
    return joblib.load(path)


def model_score(model, feature: float) -> float:
    return float(model.predict_proba([[feature]])[0][1])


def deserialize_model_uri(uri: str):
    from mlflow import sklearn as mlflow_flavor

    return mlflow_flavor.load_model(model_uri=uri)
'''
    elif framework == "xgboost":
        mlflow_flavor = "xgboost"
        mlflow_model_argument = "xgb_model"
        train_body = '''from xgboost import XGBClassifier


def selected_device() -> str:
    selected = os.getenv("KIONGA_ACCELERATOR", DEFAULT_ACCELERATOR)
    if selected == "cpu":
        return "cpu"
    if selected == "single-gpu":
        return "cuda"
    raise RuntimeError("multi-GPU XGBoost requires a distributed-training template")


def train_model():
    features = [[0.0], [0.1], [0.2], [0.35], [0.65], [0.8], [0.9], [1.0]]
    labels = [0, 0, 0, 0, 1, 1, 1, 1]
    model = XGBClassifier(
        n_estimators=40,
        max_depth=2,
        learning_rate=0.2,
        min_child_weight=0,
        tree_method="hist",
        device=selected_device(),
        random_state=7,
    ).fit(features, labels)
    predictions = model.predict(features)
    accuracy = sum(
        int(actual == predicted) for actual, predicted in zip(labels, predictions)
    ) / len(labels)
    return model, {"accuracy": float(accuracy)}


def save_model(model, path: Path) -> None:
    temporary = path.with_name(path.stem + ".tmp" + path.suffix)
    model.save_model(temporary)
    temporary.replace(path)
'''
        load_body = '''from xgboost import XGBClassifier


def selected_device() -> str:
    selected = os.getenv("KIONGA_ACCELERATOR", DEFAULT_ACCELERATOR)
    if selected == "cpu":
        return "cpu"
    if selected == "single-gpu":
        return "cuda"
    raise RuntimeError("multi-GPU XGBoost requires a distributed-training template")


def deserialize_model(path: Path):
    model = XGBClassifier(device=selected_device())
    model.load_model(path)
    return model


def model_score(model, feature: float) -> float:
    return float(model.predict_proba([[feature]])[0][1])


def deserialize_model_uri(uri: str):
    from mlflow import xgboost as mlflow_flavor

    return mlflow_flavor.load_model(model_uri=uri)
'''
    else:
        mlflow_flavor = "pytorch"
        mlflow_model_argument = "pytorch_model"
        train_body = '''import torch
from torch import nn


def selected_device() -> torch.device:
    selected = os.getenv("KIONGA_ACCELERATOR", DEFAULT_ACCELERATOR)
    if selected == "cpu":
        return torch.device("cpu")
    if selected == "single-gpu":
        if not torch.cuda.is_available():
            raise RuntimeError("single-gpu selected but CUDA is not available")
        return torch.device("cuda:0")
    raise RuntimeError("multi-GPU PyTorch requires the distributed-training template")


def build_model() -> nn.Module:
    return nn.Sequential(nn.Linear(1, 1), nn.Sigmoid())


def train_model():
    device = selected_device()
    torch.manual_seed(7)
    features = torch.tensor(
        [[0.0], [0.1], [0.2], [0.35], [0.65], [0.8], [0.9], [1.0]],
        device=device,
    )
    labels = torch.tensor(
        [[0.0], [0.0], [0.0], [0.0], [1.0], [1.0], [1.0], [1.0]],
        device=device,
    )
    model = build_model().to(device)
    optimizer = torch.optim.Adam(model.parameters(), lr=0.1)
    loss_fn = nn.BCELoss()
    loss = torch.tensor(0.0, device=device)
    for _ in range(200):
        optimizer.zero_grad()
        loss = loss_fn(model(features), labels)
        loss.backward()
        optimizer.step()
    predictions = (model(features) >= 0.5).float()
    accuracy = float((predictions == labels).float().mean().detach().cpu())
    return model, {"accuracy": accuracy, "loss": float(loss.detach().cpu())}


def save_model(model, path: Path) -> None:
    temporary = path.with_name(path.name + ".tmp")
    state = {key: value.detach().cpu() for key, value in model.state_dict().items()}
    torch.save(state, temporary)
    temporary.replace(path)
'''
        load_body = '''import torch
from torch import nn


def selected_device() -> torch.device:
    selected = os.getenv("KIONGA_ACCELERATOR", DEFAULT_ACCELERATOR)
    if selected == "cpu":
        return torch.device("cpu")
    if selected == "single-gpu":
        if not torch.cuda.is_available():
            raise RuntimeError("single-gpu selected but CUDA is not available")
        return torch.device("cuda:0")
    raise RuntimeError("multi-GPU PyTorch requires the distributed-training template")


def deserialize_model(path: Path):
    device = selected_device()
    model = nn.Sequential(nn.Linear(1, 1), nn.Sigmoid()).to(device)
    model.load_state_dict(torch.load(path, map_location=device, weights_only=True))
    model.eval()
    return model


def model_score(model, feature: float) -> float:
    device = selected_device()
    with torch.no_grad():
        return float(model(torch.tensor([[feature]], device=device)).item())


def deserialize_model_uri(uri: str):
    from mlflow import pytorch as mlflow_flavor

    model = mlflow_flavor.load_model(model_uri=uri)
    return model.to(selected_device()).eval()
'''

    train = f'''"""Train, evaluate, persist, and register a deployable model artifact."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

FRAMEWORK = "{framework}"
DEFAULT_ACCELERATOR = "{accelerator}"
MODEL_FILENAME = "{filename}"

{train_body}

def publish_model(model, artifact: Path, metrics: dict[str, float]) -> str:
    """Log a complete MLflow model; use a raw file only for local tests."""
    tracking_uri = os.getenv("MLFLOW_TRACKING_URI", "").strip()
    if not tracking_uri:
        project_id = os.getenv("KIONGA_PROJECT_ID", "").strip()
        if project_id and project_id != "replace-with-project-id":
            raise RuntimeError(
                "MLFLOW_TRACKING_URI is required before a platform run can publish a model"
            )
        return artifact.resolve().as_uri()

    import mlflow

    mlflow.set_tracking_uri(tracking_uri)
    mlflow.set_experiment(
        os.getenv("KIONGA_MLFLOW_EXPERIMENT", "/kionga/{package}")
    )
    with mlflow.start_run(run_name=os.getenv("KIONGA_RUN_ID", "train-register")):
        mlflow.log_metrics(metrics)
        from mlflow import {mlflow_flavor} as mlflow_flavor

        mlflow_flavor.log_model(
            {mlflow_model_argument}=model,
            artifact_path="model",
        )
        return mlflow.get_artifact_uri("model")


def register(metrics: dict[str, float], artifact_uri: str) -> None:
    gateway, project_id = os.getenv("MLAIOPS_URL"), os.getenv("KIONGA_PROJECT_ID")
    if not gateway or not project_id or project_id == "replace-with-project-id":
        return
    import httpx

    response = httpx.post(
        f"{{gateway.rstrip('/')}}/api/v1/models",
        json={{
            "project_id": project_id,
            "name": "{package}",
            "version": os.getenv("GIT_SHA", "local"),
            "artifact_uri": artifact_uri,
            "serving_image": os.getenv("KIONGA_SERVING_IMAGE", ""),
            "metrics": metrics,
        }},
        timeout=10,
    )
    response.raise_for_status()


def write_json(path: Path, payload: dict[str, object]) -> None:
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\\n")
    temporary.replace(path)


def run(output: str | None = None) -> dict[str, float]:
    directory = Path(output or os.getenv("KIONGA_ARTIFACT_DIR", "artifacts"))
    directory.mkdir(parents=True, exist_ok=True)
    model, metrics = train_model()
    artifact = directory / MODEL_FILENAME
    save_model(model, artifact)
    write_json(directory / "metrics.json", metrics)
    artifact_uri = publish_model(model, artifact, metrics)
    write_json(
        directory / "model-metadata.json",
        {{
            "framework": FRAMEWORK,
            "accelerator": os.getenv("KIONGA_ACCELERATOR", DEFAULT_ACCELERATOR),
            "artifact": artifact.name,
            "artifact_uri": artifact_uri,
            "metrics": metrics,
        }},
    )
    register(metrics, artifact_uri)
    return metrics


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", default=None)
    parser.add_argument("--register-only", action="store_true")
    arguments = parser.parse_args()
    output = Path(arguments.output or os.getenv("KIONGA_ARTIFACT_DIR", "artifacts"))
    if arguments.register_only:
        metrics = json.loads((output / "metrics.json").read_text())
        metadata = json.loads((output / "model-metadata.json").read_text())
        register(metrics, str(metadata["artifact_uri"]))
    else:
        print(json.dumps(run(str(output)), sort_keys=True))


if __name__ == "__main__":
    main()
'''
    serve = f'''"""Online inference backed by the exact artifact emitted during training."""

from __future__ import annotations

import os
from functools import lru_cache
from pathlib import Path

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel, Field

FRAMEWORK = "{framework}"
DEFAULT_ACCELERATOR = "{accelerator}"
MODEL_FILENAME = "{filename}"

app = FastAPI(title="{package}", version="0.1.0")


class PredictionRequest(BaseModel):
    values: list[float] = Field(
        min_length=1,
        description="Values aggregated into the single feature used by this starter model.",
    )


def configured_model_path() -> Path:
    configured = os.getenv("KIONGA_MODEL_PATH", "").strip()
    if configured:
        return Path(configured)
    return Path(os.getenv("KIONGA_ARTIFACT_DIR", "artifacts")) / MODEL_FILENAME


{load_body}

def resolve_model_source() -> tuple[str, bool]:
    model_uri = os.getenv("KIONGA_MODEL_URI", "").strip()
    if model_uri:
        return model_uri, True
    artifact = configured_model_path()
    if not artifact.is_file():
        raise FileNotFoundError(f"model artifact does not exist: {{artifact}}")
    return str(artifact), False


@lru_cache(maxsize=1)
def load_model(source: str = "", is_uri: bool = False):
    if not source:
        source, is_uri = resolve_model_source()
    if is_uri:
        return deserialize_model_uri(source)
    return deserialize_model(Path(source))


@app.get("/healthz")
def health() -> dict[str, str]:
    return {{"status": "ok"}}


@app.get("/readyz")
def ready() -> dict[str, str]:
    try:
        source, is_uri = resolve_model_source()
        load_model(source, is_uri)
    except Exception as error:
        raise HTTPException(status_code=503, detail=str(error)) from error
    return {{"status": "ready", "model_source": source}}


@app.post("/predict")
def predict(payload: PredictionRequest) -> dict[str, float | int | str]:
    try:
        source, is_uri = resolve_model_source()
        model = load_model(source, is_uri)
    except Exception as error:
        raise HTTPException(status_code=503, detail=str(error)) from error
    feature = sum(payload.values) / len(payload.values)
    score = model_score(model, feature)
    return {{"framework": FRAMEWORK, "score": score, "prediction": int(score >= 0.5)}}
'''
    test = f'''import json
import sys
from contextlib import nullcontext
from types import SimpleNamespace

import pytest

from {package}.serve import (
    MODEL_FILENAME,
    PredictionRequest,
    health,
    load_model,
    predict,
    ready,
)
from {package}.train import publish_model, run
from {package} import serve as serving
from {package} import train as training


def test_training_artifact_drives_online_predictions(tmp_path, monkeypatch):
    monkeypatch.setenv("KIONGA_ACCELERATOR", "cpu")
    monkeypatch.delenv("MLAIOPS_URL", raising=False)
    monkeypatch.delenv("KIONGA_PROJECT_ID", raising=False)
    monkeypatch.delenv("MLFLOW_TRACKING_URI", raising=False)
    monkeypatch.delenv("KIONGA_MODEL_URI", raising=False)
    output = tmp_path / "artifacts"
    metrics = run(str(output))
    artifact = output / MODEL_FILENAME
    monkeypatch.setenv("KIONGA_MODEL_PATH", str(artifact))
    load_model.cache_clear()

    assert artifact.is_file()
    metadata = json.loads((output / "model-metadata.json").read_text())
    assert metadata["artifact_uri"] == artifact.resolve().as_uri()
    assert metrics["accuracy"] >= 0.75
    assert health() == {{"status": "ok"}}
    assert ready()["status"] == "ready"
    low = predict(PredictionRequest(values=[0.05, 0.15]))
    high = predict(PredictionRequest(values=[0.85, 0.95]))
    assert low["prediction"] == 0
    assert high["prediction"] == 1
    assert high["score"] > low["score"]


def test_publish_model_logs_a_complete_mlflow_flavor(tmp_path, monkeypatch):
    artifact = tmp_path / MODEL_FILENAME
    artifact.write_bytes(b"trained-model")
    calls = {{}}
    model = object()
    fake_flavor = SimpleNamespace(
        log_model=lambda **kwargs: calls.setdefault("log_model", kwargs)
    )
    fake_mlflow = SimpleNamespace(
        set_tracking_uri=lambda value: calls.setdefault("tracking_uri", value),
        set_experiment=lambda value: calls.setdefault("experiment", value),
        start_run=lambda **kwargs: nullcontext(),
        log_metrics=lambda value: calls.setdefault("metrics", value),
        get_artifact_uri=lambda path: f"s3://models/run-1/artifacts/{{path}}",
    )
    setattr(fake_mlflow, "{mlflow_flavor}", fake_flavor)
    monkeypatch.setitem(sys.modules, "mlflow", fake_mlflow)
    monkeypatch.setenv("MLFLOW_TRACKING_URI", "http://mlflow:5000")

    uri = publish_model(model, artifact, {{"accuracy": 1.0}})

    assert uri == "s3://models/run-1/artifacts/model"
    assert calls["tracking_uri"] == "http://mlflow:5000"
    assert calls["log_model"]["artifact_path"] == "model"
    assert calls["log_model"]["{mlflow_model_argument}"] is model


def test_platform_run_refuses_an_ephemeral_model_uri(tmp_path, monkeypatch):
    artifact = tmp_path / MODEL_FILENAME
    artifact.write_bytes(b"trained-model")
    monkeypatch.delenv("MLFLOW_TRACKING_URI", raising=False)
    monkeypatch.setenv("KIONGA_PROJECT_ID", "project-real")

    with pytest.raises(RuntimeError, match="MLFLOW_TRACKING_URI is required"):
        publish_model(object(), artifact, {{"accuracy": 1.0}})


def test_run_registers_the_logged_model_uri(tmp_path, monkeypatch):
    monkeypatch.setenv("KIONGA_ACCELERATOR", "cpu")
    registered = {{}}
    model_uri = "runs:/run-1/model"
    monkeypatch.setattr(
        training,
        "publish_model",
        lambda model, artifact, metrics: model_uri,
    )
    monkeypatch.setattr(
        training,
        "register",
        lambda metrics, artifact_uri: registered.setdefault("uri", artifact_uri),
    )

    output = tmp_path / "registered"
    training.run(str(output))

    metadata = json.loads((output / "model-metadata.json").read_text())
    assert metadata["artifact_uri"] == model_uri
    assert registered["uri"] == model_uri


def test_model_uri_loads_the_logged_mlflow_flavor(tmp_path, monkeypatch):
    monkeypatch.setenv("KIONGA_ACCELERATOR", "cpu")
    monkeypatch.delenv("MLFLOW_TRACKING_URI", raising=False)
    monkeypatch.delenv("MLAIOPS_URL", raising=False)
    output = tmp_path / "training"
    run(str(output))
    artifact = output / MODEL_FILENAME
    calls = {{}}
    local_model = serving.deserialize_model(artifact)

    def load_remote_model(*, model_uri):
        calls["model_uri"] = model_uri
        return local_model

    fake_mlflow = SimpleNamespace()
    setattr(
        fake_mlflow,
        "{mlflow_flavor}",
        SimpleNamespace(load_model=load_remote_model),
    )
    monkeypatch.setitem(
        sys.modules,
        "mlflow",
        fake_mlflow,
    )
    monkeypatch.setenv("KIONGA_MODEL_URI", "s3://models/run-1/model")
    load_model.cache_clear()

    result = predict(PredictionRequest(values=[0.9]))

    assert result["prediction"] == 1
    assert calls["model_uri"] == "s3://models/run-1/model"
'''
    return {"train.py": train, "serve.py": serve, "../../tests/test_model.py": test}


def _distributed_source(
    package: str, framework: str, accelerator_count: int
) -> dict[str, str]:
    adapters = {
        "pytorch-ddp": '''import torch
import torch.distributed as dist
from torch import nn
from torch.nn.parallel import DistributedDataParallel


def train_worker(rank: int, world_size: int, output: Path) -> float:
    """Run a resumable DDP training loop; world_size=1 is the CPU smoke path."""
    use_cuda = torch.cuda.is_available()
    device = torch.device(f"cuda:{int(os.getenv('LOCAL_RANK', '0'))}" if use_cuda else "cpu")
    if world_size > 1 and not dist.is_initialized():
        dist.init_process_group(backend="nccl" if use_cuda else "gloo")
    torch.manual_seed(7)
    features = torch.linspace(0, 1, 256, device=device).reshape(-1, 1)
    labels = (features >= 0.5).float()
    model = nn.Linear(1, 1).to(device)
    optimizer = torch.optim.Adam(model.parameters(), lr=0.05)
    checkpoint = output / "checkpoint.pt"
    start_epoch = 0
    if checkpoint.exists() and os.getenv("RESUME_FROM_CHECKPOINT", "true").lower() == "true":
        saved = torch.load(checkpoint, map_location=device, weights_only=True)
        model.load_state_dict(saved["model"])
        optimizer.load_state_dict(saved["optimizer"])
        start_epoch = int(saved["epoch"]) + 1
    if world_size > 1:
        model = DistributedDataParallel(
            model, device_ids=[device.index] if device.type == "cuda" else None
        )
    epochs = int(os.getenv("EPOCHS", "30"))
    mixed_precision = use_cuda and os.getenv("MIXED_PRECISION", "true").lower() == "true"
    loss = torch.tensor(0.0, device=device)
    for epoch in range(start_epoch, epochs):
        optimizer.zero_grad()
        with torch.autocast(device_type=device.type, dtype=torch.float16, enabled=mixed_precision):
            predictions = torch.sigmoid(model(features))
            loss = nn.functional.binary_cross_entropy(predictions, labels)
        loss.backward()
        optimizer.step()
        if rank == 0:
            state = model.module.state_dict() if isinstance(model, DistributedDataParallel) else model.state_dict()
            torch.save(
                {"model": state, "optimizer": optimizer.state_dict(), "epoch": epoch}, checkpoint
            )
    if world_size > 1:
        dist.barrier()
        dist.destroy_process_group()
    return float(loss.detach().cpu())
''',
    }
    train = f'''"""Framework-aware worker entry point for a distributed training job."""

from __future__ import annotations

import json
import os
from pathlib import Path

{adapters[framework]}

def distributed_context() -> tuple[int, int]:
    rank = int(os.getenv("RANK", os.getenv("WORKER_INDEX", "0")))
    world_size = int(os.getenv("WORLD_SIZE", os.getenv("WORKER_COUNT", "1")))
    if rank < 0 or world_size < 1 or rank >= world_size:
        raise ValueError("expected 0 <= rank < world_size")
    return rank, world_size


def main() -> None:
    rank, world_size = distributed_context()
    output = Path(os.getenv("OUTPUT_DIR", "artifacts"))
    output.mkdir(parents=True, exist_ok=True)
    metric = train_worker(rank, world_size, output)
    result = {{"framework": "{framework}", "rank": rank, "world_size": world_size,
              "worker_metric": metric}}
    (output / f"worker-{{rank}}.json").write_text(json.dumps(result, indent=2) + "\\n")
    if rank == 0 and os.getenv("MLFLOW_TRACKING_URI"):
        import mlflow

        with mlflow.start_run(run_name=os.getenv("KIONGA_RUN_ID", "distributed-training")):
            mlflow.log_params({{"framework": "{framework}", "world_size": world_size}})
            mlflow.log_metric("worker_metric", metric)
            mlflow.log_artifacts(str(output), artifact_path="training")
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
'''
    test = f'''import pytest

from {package}.train import distributed_context


def test_distributed_context_rejects_invalid_rank(monkeypatch):
    monkeypatch.setenv("RANK", "2")
    monkeypatch.setenv("WORLD_SIZE", "2")
    with pytest.raises(ValueError):
        distributed_context()
'''
    launcher = {
        "pytorch-ddp": f'''#!/bin/sh
set -eu
exec torchrun --standalone --nproc-per-node="${{WORKERS_PER_NODE:-{max(1, accelerator_count)}}}" -m {package}.train
''',
    }[framework]
    return {
        "train.py": train,
        "../../tests/test_distributed.py": test,
        "../../scripts/launch-training.sh": launcher,
    }


def _agent_source(package: str, fullstack: bool) -> dict[str, str]:
    agent = '''"""LangGraph agent with injectable model and production checkpointer support."""

from __future__ import annotations

import os
from typing import Any

from langchain_core.messages import SystemMessage
from langgraph.graph import END, StateGraph
from langgraph.graph.message import MessagesState
from langgraph.prebuilt import ToolNode

SYSTEM_PROMPT = "Be concise, cite tool results, and never invent unavailable data."


def build(model: Any = None, checkpointer: Any = None):
    from .tools import project_status

    if model is None:
        from mlaiops_sdk.llm import build_chat_model

        model = build_chat_model(backend=os.getenv("MLAIOPS_LLM_BACKEND", "mock"))
    tools = [project_status]
    bound_model = model.bind_tools(tools)

    def respond(state: MessagesState) -> dict:
        messages = state["messages"]
        if not messages or not isinstance(messages[0], SystemMessage):
            messages = [SystemMessage(content=SYSTEM_PROMPT), *messages]
        return {"messages": [bound_model.invoke(messages)]}

    def route(state: MessagesState):
        return "tools" if getattr(state["messages"][-1], "tool_calls", None) else END

    graph = StateGraph(MessagesState)
    graph.add_node("respond", respond)
    graph.add_node("tools", ToolNode(tools))
    graph.set_entry_point("respond")
    graph.add_conditional_edges("respond", route, {"tools": "tools", END: END})
    graph.add_edge("tools", "respond")
    return graph.compile(checkpointer=checkpointer)
'''
    runtime = f'''from __future__ import annotations

import uuid
from contextlib import asynccontextmanager
import os

from fastapi import FastAPI, Request
from pydantic import BaseModel, Field

from .agent import build


class InvokeRequest(BaseModel):
    message: str = Field(min_length=1)
    session_id: str = ""


@asynccontextmanager
async def lifespan(app: FastAPI):
    checkpointer_context = None
    if os.getenv("MLAIOPS_CHECKPOINT_DSN") or os.getenv("DATABASE_URL"):
        from mlaiops_sdk.agents import get_langgraph_checkpointer

        checkpointer_context = get_langgraph_checkpointer()
        checkpointer = await checkpointer_context.__aenter__()
        await checkpointer.setup()
    else:
        from langgraph.checkpoint.memory import MemorySaver

        checkpointer = MemorySaver()
    app.state.graph = build(checkpointer=checkpointer)
    try:
        yield
    finally:
        if checkpointer_context is not None:
            await checkpointer_context.__aexit__(None, None, None)


app = FastAPI(title="{package}", version="0.1.0", lifespan=lifespan)


@app.get("/healthz")
def health() -> dict[str, str]:
    return {{"status": "ok"}}


@app.post("/invoke")
async def invoke(payload: InvokeRequest, request: Request) -> dict[str, str]:
    from langchain_core.messages import HumanMessage
    from mlaiops_sdk.tracing import langfuse_handler

    session_id = payload.session_id or str(uuid.uuid4())
    config = {{"configurable": {{"thread_id": session_id}}}}
    if handler := langfuse_handler():
        config["callbacks"] = [handler]
    result = await request.app.state.graph.ainvoke(
        {{"messages": [HumanMessage(content=payload.message)]}},
        config=config,
    )
    return {{"reply": str(result["messages"][-1].content), "session_id": session_id}}
'''
    if fullstack:
        runtime += '''

from pathlib import Path
from fastapi.responses import FileResponse


@app.get("/")
def index() -> FileResponse:
    return FileResponse(Path(__file__).parent / "web" / "index.html")
'''
    tools = '''"""Testable tools plus an explicit platform-memory boundary."""

from langchain_core.tools import tool

@tool
def project_status(project_id: str) -> dict[str, str]:
    """Return the status boundary for a project without inventing remote state."""
    if not project_id.strip():
        raise ValueError("project_id is required")
    return {"project_id": project_id, "status": "lookup-not-configured"}


def remember_project_fact(project_id: str, fact: str) -> None:
    """Persist long-term memory only when the platform memory DSN is configured."""
    from mlaiops_sdk.agents import AgentMemoryClient

    with AgentMemoryClient("project-agent", session_id=project_id) as memory:
        memory.remember(fact, metadata={"project_id": project_id})
'''
    test = f'''from langchain_core.messages import AIMessage

from {package}.agent import build
from {package}.runtime import health
from {package}.tools import project_status


class DeterministicModel:
    def bind_tools(self, tools):
        return self

    def invoke(self, messages):
        return AIMessage(content="status grounded")


def test_health_and_tool_contract():
    assert health() == {{"status": "ok"}}
    assert project_status.invoke({{"project_id": "prj-1"}})["project_id"] == "prj-1"


def test_graph_is_deterministic_with_injected_model():
    result = build(model=DeterministicModel()).invoke({{"messages": [("user", "status?")]}})
    assert result["messages"][-1].content == "status grounded"


def test_graph_defaults_to_offline_mock(monkeypatch):
    monkeypatch.delenv("MLAIOPS_LLM_BACKEND", raising=False)
    result = build().invoke({{"messages": [("user", "status?")]}})
    assert result["messages"][-1].content.startswith("[mock] acknowledged:")
'''
    evaluation = '''{"input":"Summarize the current project status.","required_terms":["status"]}
{"input":"Explain a failed pipeline without inventing a cause.","required_terms":["pipeline"]}
'''
    evaluation_runner = '''"""Small deterministic evaluation harness for local and CI gates."""

from __future__ import annotations

import argparse
import json
import urllib.request
from pathlib import Path
from typing import Callable


def load_cases(path: str | Path) -> list[dict]:
    lines = Path(path).read_text(encoding="utf-8").splitlines()
    return [json.loads(line) for line in lines if line.strip()]


def evaluate(cases: list[dict], responder: Callable[[str], str]) -> dict[str, float | int]:
    passed = 0
    for case in cases:
        reply = responder(case["input"]).lower()
        if all(term.lower() in reply for term in case.get("required_terms", [])):
            passed += 1
    total = len(cases)
    return {"passed": passed, "total": total, "pass_rate": passed / total if total else 0.0}


def http_responder(base_url: str) -> Callable[[str], str]:
    def respond(message: str) -> str:
        request = urllib.request.Request(
            base_url.rstrip("/") + "/invoke",
            data=json.dumps({"message": message}).encode(),
            headers={"content-type": "application/json"},
        )
        with urllib.request.urlopen(request, timeout=60) as response:
            return json.load(response)["reply"]

    return respond


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dataset", default="evals/golden.jsonl")
    parser.add_argument("--url", default="http://localhost:8080")
    parser.add_argument("--minimum-pass-rate", type=float, default=1.0)
    arguments = parser.parse_args()
    result = evaluate(load_cases(arguments.dataset), http_responder(arguments.url))
    print(json.dumps(result, sort_keys=True))
    if result["pass_rate"] < arguments.minimum_pass_rate:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
'''
    result = {
        "agent.py": agent,
        "runtime.py": runtime,
        "tools.py": tools,
        "../../tests/test_agent.py": test,
        "../../evals/__init__.py": '"""Agent evaluation assets."""\n',
        "../../evals/golden.jsonl": evaluation,
        "../../evals/run.py": evaluation_runner,
    }
    if fullstack:
        result["web/index.html"] = f'''<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>{package}</title></head><body><main><h1>{package}</h1>
<form id="chat"><label>Message <input id="message" required></label><button>Send</button></form>
<pre id="output" aria-live="polite"></pre></main><script>
chat.addEventListener("submit", async event => {{
  event.preventDefault();
  const response = await fetch("/invoke", {{method:"POST", headers:{{"content-type":"application/json"}},
    body:JSON.stringify({{message:message.value}})}});
  output.textContent = JSON.stringify(await response.json(), null, 2);
}});
</script></body></html>
'''
    return result


def _blank_source(package: str) -> dict[str, str]:
    cli = f'''"""Command-line entry point for {package}."""


def greeting(name: str) -> str:
    return f"Hello, {{name.strip() or 'builder'}}."


def main() -> None:
    print(greeting("builder"))


if __name__ == "__main__":
    main()
'''
    test = f'''from {package}.cli import greeting


def test_greeting():
    assert greeting("team") == "Hello, team."
'''
    return {"cli.py": cli, "../../tests/test_cli.py": test}


def _event_function_sources(agent_mode: bool) -> dict[str, str]:
    result = (
        'return {"accepted": True, "event_type": payload.event_type}'
        if agent_mode
        else '''score = sum(payload.values) / len(payload.values) if payload.values else 0.0
    return {"accepted": True, "event_type": payload.event_type, "score": score}'''
    )
    handler = f'''from fastapi import FastAPI
from pydantic import BaseModel, Field

app = FastAPI(title="Kionga event function", version="0.1.0")


class Event(BaseModel):
    event_type: str = Field(min_length=1)
    values: list[float] = Field(default_factory=list)


@app.get("/healthz")
def health() -> dict[str, str]:
    return {{"status": "ok"}}


@app.post("/")
def handle(payload: Event) -> dict:
    {result}
'''
    dockerfile = '''FROM python:3.11-slim
WORKDIR /app
COPY functions/requirements.txt ./requirements.txt
RUN python -m pip install --no-cache-dir -r requirements.txt
COPY functions ./functions
RUN useradd --create-home --uid 65532 app
USER 65532:65532
EXPOSE 8080
CMD ["uvicorn", "functions.event_handler:app", "--host", "0.0.0.0", "--port", "8080"]
'''
    test = '''from functions.event_handler import Event, handle


def test_event_function_contract():
    result = handle(Event(event_type="record.created", values=[0.2, 0.8]))
    assert result["accepted"] is True
    assert result["event_type"] == "record.created"
'''
    return {
        "../../functions/__init__.py": '"""Event function package."""\n',
        "../../functions/event_handler.py": handler,
        "../../functions/requirements.txt": "fastapi>=0.115,<1\nuvicorn[standard]>=0.34,<1\n",
        "../../functions/Dockerfile": dockerfile,
        "../../tests/test_event_function.py": test,
    }


def _template_sources(package: str, resolved: ResolvedTemplate) -> dict[str, str]:
    template = resolved.spec.identifier
    if template == "production-ml":
        return _ml_source(package, resolved.framework, resolved.accelerator)
    if template == "distributed-training":
        accelerator_count = ACCELERATOR_RESOURCES[resolved.accelerator][1]
        return _distributed_source(package, resolved.framework, accelerator_count)
    if template == "production-agent":
        return _agent_source(package, False)
    if template == "fullstack-ai" and resolved.framework == "fastapi-agent":
        sources = _agent_source(package, True)
        sources.update(_event_function_sources(True))
        return sources
    if template == "fullstack-ai":
        sources = _ml_source(
            package, ml_framework_for(resolved), resolved.accelerator
        )
        sources["web/index.html"] = f'''<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>{package}</title></head><body><main><h1>{package}</h1>
<form id="predict"><label>Values <input id="values" value="0.7,0.9" required></label>
<button>Predict</button></form><pre id="output" aria-live="polite"></pre></main><script>
predict.addEventListener("submit", async event => {{
  event.preventDefault();
  const payload = {{values:values.value.split(",").map(Number)}};
  const response = await fetch("/predict", {{method:"POST", headers:{{"content-type":"application/json"}},
    body:JSON.stringify(payload)}});
  output.textContent = JSON.stringify(await response.json(), null, 2);
}});
</script></body></html>
'''
        sources["serve.py"] += '''

from pathlib import Path
from fastapi.responses import FileResponse


@app.get("/")
def index() -> FileResponse:
    return FileResponse(Path(__file__).parent / "web" / "index.html")
'''
        sources.update(_event_function_sources(False))
        return sources
    return _blank_source(package)


def _readme(name: str, prompt: str, resolved: ResolvedTemplate) -> str:
    purpose = prompt.strip() or resolved.spec.description
    entrypoint, workload = project_entrypoint(resolved, package_name(name))
    run = (
        f"uvicorn {entrypoint} --reload --port 8080"
        if workload == "service"
        else entrypoint
    )
    return f'''# {name}

{purpose}

Generated from **{resolved.spec.identifier} {resolved.spec.version}** with
`{resolved.framework}`, `{resolved.accelerator}`, and the `{resolved.profile}` resource profile.

## Develop

```bash
python -m venv .venv
. .venv/bin/activate
python -m pip install -e ".[dev]"
pytest -q
ruff check src tests
```

## Run

```bash
{run}
```

Build and publish the image referenced by `platform/`, then create the project
and workload through the Kionga console or API. Put credentials in the platform
secret store or workload identity; never commit them to this repository.
'''


def _template_metadata(name: str, package: str, resolved: ResolvedTemplate) -> dict[str, Any]:
    profile = RESOURCE_PROFILES[resolved.profile]
    accelerator_resource, accelerator_count = ACCELERATOR_RESOURCES[resolved.accelerator]
    resources = asdict(profile)
    resources["gpu"] = max(resources["gpu"], accelerator_count)
    if accelerator_resource:
        resources["accelerator_type"] = accelerator_resource
    return {
        "api_version": CATALOG_API_VERSION,
        "catalog_version": CATALOG_VERSION,
        "template": {"id": resolved.spec.identifier, "version": resolved.spec.version},
        "project": {"name": name, "package": package, "kind": resolved.spec.kind},
        "runtime": {
            "framework": resolved.framework,
            "accelerator": resolved.accelerator,
            "profile": resolved.profile,
        },
        "resources": resources,
        "capabilities": list(resolved.spec.capabilities),
        "required_services": list(resolved.spec.required_services),
    }


def _ensure_empty(target: Path) -> None:
    if target.exists() and (not target.is_dir() or any(target.iterdir())):
        raise RuntimeError(f"target already exists and is not empty: {target}")


def _populate_scaffold(
    target: Path,
    name: str,
    prompt: str,
    resolved: ResolvedTemplate,
    initialize_git: bool,
) -> None:
    """Write a resolved scaffold into a private, empty staging directory."""
    package = package_name(name)
    template = resolved.spec.identifier

    write_file(target / "README.md", _readme(name, prompt, resolved))
    write_file(
        target / ".gitignore",
        ".env\n.venv/\n__pycache__/\n.pytest_cache/\n.ruff_cache/\n*.py[cod]\nartifacts/\n",
    )
    write_file(target / ".dockerignore", ".git\n.env\n.venv\n__pycache__\n.pytest_cache\nartifacts\n")
    write_file(target / ".env.example", _environment_example(resolved))
    write_file(target / "pyproject.toml", _pyproject(name, package, resolved))
    write_file(target / "Dockerfile", _dockerfile(package, resolved))
    write_file(target / ".github/workflows/ci.yml", _ci_workflow())
    write_file(target / "platform/project.yaml", _project_manifest(name, resolved))
    project_request = {
        "name": name,
        "description": prompt.strip() or resolved.spec.description,
        "template": resolved.spec.identifier,
        "template_version": resolved.spec.version,
        "framework": resolved.framework,
        "accelerator": resolved.accelerator,
        "requested_profile": resolved.profile,
    }
    write_file(
        target / "platform/project.json",
        json.dumps(project_request, indent=2, sort_keys=True),
    )
    write_file(
        target / ".kionga/template.json",
        json.dumps(_template_metadata(name, package, resolved), indent=2, sort_keys=True),
    )
    write_file(target / f"src/{package}/__init__.py", f'"""The {name} project."""\n')
    write_file(
        target / "tests/test_smoke.py",
        f"import {package}\n\n\ndef test_package_imports():\n    assert {package} is not None\n",
    )
    for relative_path, content in _template_sources(package, resolved).items():
        write_file(target / "src" / package / relative_path, content)

    if template in {"production-ml", "distributed-training"} or (
        template == "fullstack-ai" and resolved.framework == "fastapi-ml"
    ):
        write_file(target / "platform/pipeline.yaml", _pipeline_manifest(name, resolved))
        write_file(
            target / "platform/pipeline.json",
            json.dumps(_pipeline_request(name, resolved), indent=2, sort_keys=True),
        )
    if template == "production-agent" or (
        template == "fullstack-ai" and resolved.framework == "fastapi-agent"
    ):
        write_file(target / "platform/agent.yaml", _agent_manifest(name, package, resolved))
        write_file(
            target / "platform/agent.json",
            json.dumps(_agent_request(name, package, resolved), indent=2, sort_keys=True),
        )
    if template == "fullstack-ai":
        function_request = {
            "project_id": "replace-with-project-id",
            "name": derived_name(name, "events"),
            "image": f"ghcr.io/your-org/{name}-events:latest",
            "env_vars": {},
            "labels": {"kionga.io/project": name},
            "annotations": {"topic": f"{name}.events"},
            "cpu": "500m",
            "memory": "512Mi",
        }
        write_file(
            target / "platform/function.json",
            json.dumps(function_request, indent=2, sort_keys=True),
        )

    if initialize_git and shutil.which("git"):
        subprocess.run(["git", "init", "-q"], cwd=target, check=True)


def create_base(
    target: Path,
    name: str,
    template: str,
    prompt: str = "",
    *,
    version: str = "",
    framework: str = "",
    accelerator: str = "",
    profile: str = "",
    initialize_git: bool = True,
) -> ResolvedTemplate:
    """Generate one complete starter and atomically publish it at ``target``."""
    name = slug(name)
    resolved = resolve_template(
        template,
        version=version,
        framework=framework,
        accelerator=accelerator,
        profile=profile,
    )
    destination = target.resolve()
    _ensure_empty(destination)
    destination_existed = destination.exists()
    destination.parent.mkdir(parents=True, exist_ok=True)
    staging = Path(
        tempfile.mkdtemp(
            prefix=f".{destination.name}.kionga-staging-",
            dir=destination.parent,
        )
    )
    removed_destination = False
    try:
        _populate_scaffold(staging, name, prompt, resolved, initialize_git)
        if destination_existed:
            # Preserve the caller's empty directory until generation has succeeded.
            destination.rmdir()
            removed_destination = True
        elif destination.exists():
            raise RuntimeError(f"target appeared while generating project: {destination}")
        staging.replace(destination)
    except BaseException:
        if staging.exists():
            shutil.rmtree(staging)
        if removed_destination and not destination.exists():
            try:
                destination.mkdir()
            except OSError:
                pass
        raise
    return resolved


def agent_command(agent: str, target: Path, prompt: str) -> list[str]:
    instruction = (
        f"{prompt}\n\nWork only inside {target}. Build on the generated starter, add tests, "
        "and leave the project runnable. Do not access credentials or other workspace projects."
    )
    if agent == "codex":
        return [
            "codex",
            "exec",
            "--sandbox",
            "workspace-write",
            "--skip-git-repo-check",
            "-C",
            str(target),
            instruction,
        ]
    if agent == "claude":
        return ["claude", "-p", "--permission-mode", "acceptEdits", instruction]
    if agent == "custom":
        configured = os.getenv("KIONGA_CUSTOM_AGENT_COMMAND", "")
        if not configured:
            raise RuntimeError("KIONGA_CUSTOM_AGENT_COMMAND is not configured")
        return [*shlex.split(configured), instruction]
    raise ValueError(f"unsupported agent: {agent}")


def scaffold(args: argparse.Namespace) -> None:
    name = slug(args.name)
    target = (WORKSPACE / name).resolve()
    if WORKSPACE not in target.parents:
        raise ValueError("project must be created inside the workspace")
    create_base(
        target,
        name,
        args.template,
        args.prompt,
        version=args.template_version,
        framework=args.framework,
        accelerator=args.accelerator,
        profile=args.profile,
    )
    print(f"Created {target}")
    if args.agent == "none":
        print("Starter generated without an agent. Open it in JupyterLab or the IDE.")
        return
    if args.agent != "custom" and not shutil.which(args.agent):
        raise RuntimeError(f"{args.agent} CLI is not installed")
    prompt = args.prompt or f"Complete the {name} {args.template} starter"
    subprocess.run(agent_command(args.agent, target, prompt), cwd=target, check=True)


def list_templates(args: argparse.Namespace) -> None:
    document = catalog_document()
    if args.json:
        print(json.dumps(document, indent=2))
        return
    for item in document["templates"]:
        print(
            f"{item['id']:24} {item['version']:8} {item['name']} "
            f"(default: {item['frameworks'][0]}/{item['recommended_profile']})"
        )


def agents(_: argparse.Namespace) -> None:
    for name, key in (("codex", "OPENAI_API_KEY"), ("claude", "ANTHROPIC_API_KEY")):
        installed = "installed" if shutil.which(name) else "missing"
        authenticated = "configured" if os.getenv(key) else "login or API key required"
        print(f"{name:8} {installed:10} {authenticated}")
    custom = "configured" if os.getenv("KIONGA_CUSTOM_AGENT_COMMAND") else "not configured"
    print(f"{'custom':8} {'external':10} {custom}")


def api_get(path: str) -> dict[str, Any]:
    request = urllib.request.Request(KIONGA_URL + path, headers={"Accept": "application/json"})
    if token := os.getenv("MLAIOPS_TOKEN"):
        request.add_header("Authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", "replace")
        raise RuntimeError(f"control plane returned {error.code}: {detail}") from error
    except urllib.error.URLError as error:
        raise RuntimeError(f"control plane is unavailable: {error.reason}") from error


def project_sync(args: argparse.Namespace) -> None:
    project = api_get(f"/api/v1/projects/{urllib.parse.quote(args.project_id, safe='')}")
    repository = project.get("repository")
    if not repository:
        raise RuntimeError("project has no Git repository; connect one in the Kionga console")
    target = (WORKSPACE / project["namespace"]).resolve()
    if WORKSPACE not in target.parents:
        raise RuntimeError("project target escaped the workspace")
    if not shutil.which("git"):
        raise RuntimeError("git is not installed in this workspace")
    url, branch = repository["url"], repository["default_branch"]
    if (target / ".git").exists():
        origin = subprocess.run(
            ["git", "remote", "get-url", "origin"],
            cwd=target,
            check=True,
            capture_output=True,
            text=True,
        ).stdout.strip()
        if origin != url:
            raise RuntimeError(f"workspace origin is {origin!r}, expected {url!r}")
        subprocess.run(["git", "fetch", "--prune", "origin"], cwd=target, check=True)
        subprocess.run(["git", "checkout", branch], cwd=target, check=True)
        subprocess.run(["git", "pull", "--ff-only", "origin", branch], cwd=target, check=True)
        print(f"Updated {target} from {url} ({branch})")
        return
    if target.exists() and any(target.iterdir()):
        raise RuntimeError(f"target already exists and is not a Git checkout: {target}")
    target.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(
        ["git", "clone", "--single-branch", "--branch", branch, url, str(target)],
        check=True,
    )
    print(f"Cloned {url} to {target} ({branch})")


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser(prog="kionga", description="Kionga AI workspace tools")
    commands = root.add_subparsers(dest="command", required=True)

    make = commands.add_parser("scaffold", help="generate a production-shaped codebase")
    make.add_argument("name")
    make.add_argument("--prompt", default="")
    make.add_argument("--template", choices=tuple(TEMPLATE_CATALOG), default="blank-python")
    make.add_argument("--template-version", default="")
    make.add_argument("--framework", default="")
    make.add_argument("--accelerator", choices=("", *ACCELERATOR_RESOURCES), default="")
    make.add_argument("--profile", choices=("", *RESOURCE_PROFILES), default="")
    make.add_argument("--agent", choices=("none", "codex", "claude", "custom"), default="none")
    make.set_defaults(run=scaffold)

    templates = commands.add_parser("templates", help="show the canonical template catalog")
    templates.add_argument("--json", action="store_true")
    templates.set_defaults(run=list_templates)

    available = commands.add_parser("agents", help="show coding-agent availability")
    available.set_defaults(run=agents)

    projects = commands.add_parser("project", help="work with Git-native Kionga projects")
    project_commands = projects.add_subparsers(dest="project_command", required=True)
    sync = project_commands.add_parser(
        "sync", help="clone or fast-forward a project's connected repository"
    )
    sync.add_argument("project_id")
    sync.set_defaults(run=project_sync)
    return root


if __name__ == "__main__":
    arguments = parser().parse_args()
    try:
        arguments.run(arguments)
    except (RuntimeError, ValueError, subprocess.CalledProcessError) as error:
        raise SystemExit(f"kionga: {error}") from error
