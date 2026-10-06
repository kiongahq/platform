from __future__ import annotations

import os

import httpx

from .models import (
    Agent,
    AgentSession,
    AgentTrace,
    AuditEvent,
    Connection,
    Function,
    Model,
    PipelineRun,
    PipelineDefinition,
    Project,
    ProjectTemplate,
    Readiness,
    Tool,
)


class MLAIOpsClient:
    """Typed client used from notebooks, training jobs, and automation."""

    def __init__(
        self,
        base_url: str = "http://localhost:8080",
        *,
        token: str | None = None,
        actor: str | None = None,
        timeout: float = 10.0,
        transport: httpx.BaseTransport | None = None,
    ) -> None:
        token = token or os.getenv("MLAIOPS_TOKEN")
        headers = {"Authorization": f"Bearer {token}"} if token else {}
        if actor:
            headers["X-MLAIOps-Actor"] = actor
        self._client = httpx.Client(
            base_url=base_url.rstrip("/"),
            headers=headers,
            timeout=timeout,
            transport=transport,
        )

    def __enter__(self) -> MLAIOpsClient:
        return self

    def __exit__(self, *_: object) -> None:
        self.close()

    def close(self) -> None:
        self._client.close()

    def health(self) -> dict[str, str]:
        return self._request("GET", "/api/v1/health")

    def list_projects(self) -> list[Project]:
        return [Project.model_validate(item) for item in self._request("GET", "/api/v1/projects")]

    def list_project_templates(self) -> list[ProjectTemplate]:
        return [
            ProjectTemplate.model_validate(item)
            for item in self._page("/api/v1/project-templates")
        ]

    def get_project_template(
        self, template_id: str, *, version: str = ""
    ) -> ProjectTemplate:
        suffix = f"?version={version}" if version else ""
        return ProjectTemplate.model_validate(
            self._request("GET", f"/api/v1/project-templates/{template_id}{suffix}")
        )

    def create_project(
        self,
        name: str,
        *,
        description: str = "",
        template: str = "production-ml",
        template_version: str = "",
        framework: str = "",
        accelerator: str = "",
        requested_profile: str = "",
        repository_url: str = "",
        default_branch: str = "main",
    ) -> Project:
        data = self._request(
            "POST",
            "/api/v1/projects",
            json={
                "name": name,
                "description": description,
                "template": template,
                "template_version": template_version,
                "framework": framework,
                "accelerator": accelerator,
                "requested_profile": requested_profile,
                "repository_url": repository_url,
                "default_branch": default_branch,
            },
        )
        return Project.model_validate(data)

    def get_project(self, project_id: str) -> Project:
        return Project.model_validate(self._request("GET", f"/api/v1/projects/{project_id}"))

    def connect_repository(self, project_id: str, url: str, *, default_branch: str = "main") -> Project:
        return Project.model_validate(self._request("PUT", f"/api/v1/projects/{project_id}/repository", json={"url": url, "default_branch": default_branch}))

    def list_pipeline_runs(self) -> list[PipelineRun]:
        return [
            PipelineRun.model_validate(item)
            for item in self._request("GET", "/api/v1/pipelines/runs")
        ]

    def get_pipeline_run(self, run_id: str) -> PipelineRun:
        return PipelineRun.model_validate(self._request("GET", f"/api/v1/pipelines/runs/{run_id}"))

    def cancel_pipeline_run(self, run_id: str) -> PipelineRun:
        return PipelineRun.model_validate(
            self._request("POST", f"/api/v1/pipelines/runs/{run_id}/cancel", json={})
        )

    def retry_pipeline_run(self, run_id: str) -> PipelineRun:
        return PipelineRun.model_validate(
            self._request("POST", f"/api/v1/pipelines/runs/{run_id}/retry", json={})
        )

    def submit_pipeline(self, project_id: str, name: str = "training-pipeline", *, definition_id: str = "", parameters: dict | None = None) -> PipelineRun:
        data = self._request(
            "POST",
            "/api/v1/pipelines/submit",
            json={"project_id": project_id, "name": name, "definition_id": definition_id, "parameters": parameters or {}},
        )
        return PipelineRun.model_validate(data)

    def list_pipeline_definitions(self) -> list[PipelineDefinition]:
        return [PipelineDefinition.model_validate(item) for item in self._page("/api/v1/pipelines/definitions")]

    def create_pipeline_definition(self, project_id: str, name: str, version: str, jobs: list[dict], *, execution_mode: str = "prefect", repository_url: str = "", commit_sha: str = "") -> PipelineDefinition:
        data = self._request("POST", "/api/v1/pipelines/definitions", json={"project_id": project_id, "name": name, "version": version, "execution_mode": execution_mode, "jobs": jobs, "repository_url": repository_url, "commit_sha": commit_sha})
        return PipelineDefinition.model_validate(data)

    def list_functions(self) -> list[Function]:
        return [Function.model_validate(item) for item in self._page("/api/v1/functions")]

    def deploy_function(self, project_id: str, name: str, image: str, *, cpu: str = "500m", memory: str = "512Mi", env_vars: dict[str, str] | None = None, annotations: dict[str, str] | None = None) -> Function:
        data = self._request("POST", "/api/v1/functions", json={"project_id": project_id, "name": name, "image": image, "cpu": cpu, "memory": memory, "env_vars": env_vars or {}, "annotations": annotations or {}})
        return Function.model_validate(data)

    def invoke_function(self, name: str, payload: dict) -> dict:
        return self._request("POST", f"/api/v1/functions/{name}/invoke", json=payload)

    def invoke_function_async(self, name: str, payload: dict) -> dict:
        return self._request("POST", f"/api/v1/functions/{name}/invoke-async", json=payload)

    def delete_function(self, name: str) -> None:
        self._request("DELETE", f"/api/v1/functions/{name}")

    def list_models(self) -> list[Model]:
        return [Model.model_validate(item) for item in self._page("/api/v1/models")]

    def search_huggingface_models(self, search: str = "", *, task: str = "") -> list[dict]:
        return self._request("GET", "/api/v1/models/huggingface",
                             params={"search": search, "task": task})["items"]

    def import_huggingface_model(
        self, project_id: str, repo_id: str, *, revision: str = "main"
    ) -> Model:
        """Register immutable provenance; weights are downloaded separately in the workspace."""
        return Model.model_validate(self._request(
            "POST", "/api/v1/models/huggingface/import",
            json={"project_id": project_id, "repo_id": repo_id, "revision": revision},
        ))

    def register_model(
        self,
        project_id: str,
        name: str,
        version: str,
        artifact_uri: str,
        *,
        metrics: dict[str, float] | None = None,
        serving_image: str | None = None,
    ) -> Model:
        data = self._request(
            "POST",
            "/api/v1/models",
            json={
                "project_id": project_id,
                "name": name,
                "version": version,
                "artifact_uri": artifact_uri,
                "metrics": metrics or {},
                **({"serving_image": serving_image} if serving_image else {}),
            },
        )
        return Model.model_validate(data)

    def promote_model(self, model_id: str, stage: str) -> Model:
        return Model.model_validate(
            self._request("POST", f"/api/v1/models/{model_id}/promote", json={"stage": stage})
        )

    def deploy_model(self, model_id: str, *, canary_weight: int = 0) -> Model:
        return Model.model_validate(
            self._request(
                "POST",
                f"/api/v1/models/{model_id}/deploy",
                json={"canary_weight": canary_weight},
            )
        )

    def rollback_model(self, model_id: str) -> Model:
        return Model.model_validate(
            self._request("POST", f"/api/v1/models/{model_id}/rollback", json={})
        )

    def list_agents(self) -> list[Agent]:
        return [Agent.model_validate(item) for item in self._page("/api/v1/agents")]

    def deploy_agent(
        self,
        project_id: str,
        name: str,
        version: str,
        image: str,
        graph_module: str,
        *,
        llm_backend: str = "mock",
        replicas: int = 1,
        max_replicas: int | None = None,
        cpu: str = "500m",
        memory: str = "1Gi",
        gpu: int = 0,
        gpu_type: str = "nvidia.com/gpu",
        tools: list[str] | None = None,
    ) -> Agent:
        data = self._request(
            "POST",
            "/api/v1/agents",
            json={
                "project_id": project_id,
                "name": name,
                "version": version,
                "image": image,
                "graph_module": graph_module,
                "llm_backend": llm_backend,
                "replicas": replicas,
                "autoscaling": {
                    "min_replicas": replicas,
                    "max_replicas": max_replicas or replicas,
                },
                "resources": {
                    "cpu": cpu,
                    "memory": memory,
                    "gpu": gpu,
                    "gpu_type": gpu_type if gpu else "",
                },
                "tools": tools or [],
            },
        )
        return Agent.model_validate(data)

    def invoke_agent(
        self,
        agent_id: str,
        message: str,
        *,
        session_id: str = "",
        user_id: str = "",
        timeout: float = 120.0,
    ) -> dict:
        """Run one agent turn through the gateway's runtime proxy."""
        response = self._client.post(
            f"/api/v1/agents/{agent_id}/invoke",
            json={"message": message, "session_id": session_id, "user_id": user_id},
            timeout=timeout,
        )
        response.raise_for_status()
        return response.json()

    def set_agent_traffic(self, agent_id: str, canary_weight: int) -> Agent:
        return Agent.model_validate(
            self._request(
                "PUT",
                f"/api/v1/agents/{agent_id}/traffic",
                json={"canary_weight": canary_weight},
            )
        )

    def agent_sessions(self, agent_id: str) -> list[AgentSession]:
        return [
            AgentSession.model_validate(item)
            for item in self._page(f"/api/v1/agents/{agent_id}/sessions")
        ]

    def agent_traces(self, agent_id: str) -> list[AgentTrace]:
        return [
            AgentTrace.model_validate(item)
            for item in self._page(f"/api/v1/agents/{agent_id}/traces")
        ]

    def list_tools(self) -> list[Tool]:
        return [Tool.model_validate(item) for item in self._page("/api/v1/tools")]

    def register_tool(
        self,
        name: str,
        version: str,
        description: str,
        input_schema: dict,
        *,
        tags: list[str] | None = None,
    ) -> Tool:
        data = self._request(
            "POST",
            "/api/v1/tools",
            json={
                "name": name,
                "version": version,
                "description": description,
                "input_schema": input_schema,
                "tags": tags or [],
            },
        )
        return Tool.model_validate(data)

    def list_connections(self) -> list[Connection]:
        return [Connection.model_validate(item) for item in self._page("/api/v1/connections")]

    def create_connection(
        self, name: str, type: str, secret_ref: str, *, endpoint: str = ""
    ) -> Connection:
        data = self._request(
            "POST",
            "/api/v1/connections",
            json={"name": name, "type": type, "secret_ref": secret_ref, "endpoint": endpoint},
        )
        return Connection.model_validate(data)

    def test_connection(self, connection_id: str) -> Connection:
        return Connection.model_validate(
            self._request("POST", f"/api/v1/connections/{connection_id}/test", json={})
        )

    def readiness(self) -> Readiness:
        return Readiness.model_validate(self._request("GET", "/api/v1/onboarding/readiness"))

    def audit_events(self) -> list[AuditEvent]:
        return [AuditEvent.model_validate(item) for item in self._page("/api/v1/audit")]

    def _page(self, path: str) -> list[dict]:
        return self._request("GET", path)["items"]

    def _request(self, method: str, path: str, **kwargs: object):
        response = self._client.request(method, path, **kwargs)
        response.raise_for_status()
        if response.status_code == 204:
            return None
        return response.json()
