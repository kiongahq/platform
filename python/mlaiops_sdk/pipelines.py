from __future__ import annotations

from dataclasses import asdict, dataclass, field
from typing import Any


@dataclass(frozen=True)
class ResourceRequirements:
    cpu: str = "500m"
    memory: str = "1Gi"
    gpu: int = 0


@dataclass(frozen=True)
class PipelineStep:
    name: str
    image: str
    command: list[str]
    depends_on: list[str] = field(default_factory=list)
    environment: dict[str, str] = field(default_factory=dict)
    resources: ResourceRequirements = field(default_factory=ResourceRequirements)
    retries: int = 1


@dataclass(frozen=True)
class FunctionStep:
    name: str
    function: str
    depends_on: list[str] = field(default_factory=list)
    environment: dict[str, str] = field(default_factory=dict)
    resources: ResourceRequirements = field(default_factory=ResourceRequirements)
    retries: int = 1


@dataclass
class Pipeline:
    name: str
    steps: list[PipelineStep | FunctionStep] = field(default_factory=list)

    def add(self, step: PipelineStep | FunctionStep) -> Pipeline:
        if any(existing.name == step.name for existing in self.steps):
            raise ValueError(f"duplicate step: {step.name}")
        known = {existing.name for existing in self.steps}
        missing = set(step.depends_on) - known
        if missing:
            raise ValueError(f"unknown dependencies: {', '.join(sorted(missing))}")
        self.steps.append(step)
        return self

    def compile(self) -> dict[str, Any]:
        if not self.steps:
            raise ValueError("pipeline must contain at least one step")
        return {
            "apiVersion": "mlaiops.io/v1alpha1",
            "kind": "KiongaPipeline",
            "metadata": {"name": self.name},
            "spec": {"steps": [asdict(step) for step in self.steps]},
        }

    def definition(
        self,
        project_id: str,
        *,
        version: str = "1",
        execution_mode: str | None = None,
        repository_url: str = "",
        commit_sha: str = "",
    ) -> dict[str, Any]:
        """Compile to the control-plane pipeline-definition contract.

        Pure function flows default to OpenFaaS execution; any container step
        selects Prefect/KFP unless an explicit mode is supplied.
        """
        if not self.steps:
            raise ValueError("pipeline must contain at least one step")
        jobs: list[dict[str, Any]] = []
        for step in self.steps:
            job = asdict(step)
            if isinstance(step, FunctionStep):
                job["kind"] = "function"
            else:
                job["kind"] = "container"
            jobs.append(job)
        inferred = "functions" if all(isinstance(step, FunctionStep) for step in self.steps) else "prefect"
        return {
            "project_id": project_id,
            "name": self.name,
            "version": version,
            "execution_mode": execution_mode or inferred,
            "jobs": jobs,
            "repository_url": repository_url,
            "commit_sha": commit_sha,
        }
