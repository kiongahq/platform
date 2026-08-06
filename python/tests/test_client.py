import json

import httpx

from mlaiops_sdk import MLAIOpsClient


def handler(request: httpx.Request) -> httpx.Response:
    if request.url.path == "/api/v1/health":
        return httpx.Response(200, json={"status": "ok", "service": "gateway"})
    if request.url.path == "/api/v1/projects" and request.method == "GET":
        return httpx.Response(
            200,
            json=[
                {
                    "id": "prj-1",
                    "name": "Demo",
                    "description": "Starter",
                    "template": "tabular-classification",
                    "namespace": "demo",
                    "status": "ready",
                    "created_at": "2026-01-01T00:00:00Z",
                }
            ],
        )
    if request.url.path == "/api/v1/project-templates":
        return httpx.Response(
            200,
            json={
                "items": [
                    {
                        "id": "production-agent",
                        "version": "1.0.0",
                        "name": "Production AI agent",
                        "category": "Agentic AI",
                        "description": "Stateful agent starter",
                        "frameworks": ["langgraph"],
                        "accelerators": ["cpu"],
                        "capabilities": ["agent-graph", "tools"],
                        "required_services": ["agents", "storage"],
                        "recommended_profile": "team",
                    }
                ],
                "total": 1,
            },
        )
    if request.url.path == "/api/v1/project-templates/production-agent":
        return httpx.Response(
            200,
            json={
                "id": "production-agent",
                "version": "1.0.0",
                "name": "Production AI agent",
                "category": "Agentic AI",
                "description": "Stateful agent starter",
                "frameworks": ["langgraph"],
                "accelerators": ["cpu"],
                "capabilities": ["agent-graph", "tools"],
                "required_services": ["agents", "storage"],
                "recommended_profile": "team",
            },
        )
    if request.url.path == "/api/v1/projects":
        payload = json.loads(request.content)
        return httpx.Response(
            201,
            json={
                "id": "prj-2",
                "namespace": "churn",
                "status": "ready",
                "created_at": "2026-01-01T00:00:00Z",
                **payload,
            },
        )
    if request.url.path == "/api/v1/agents" and request.method == "POST":
        payload = json.loads(request.content)
        return httpx.Response(
            202,
            json={
                "id": "agt-1",
                "status": "pending",
                "canary_weight": 0,
                "created_at": "2026-01-01T00:00:00Z",
                **payload,
            },
        )
    if request.url.path == "/api/v1/models" and request.method == "POST":
        payload = json.loads(request.content)
        return httpx.Response(
            201,
            json={
                "id": "mdl-1",
                "stage": "candidate",
                "gate_status": "passed",
                "deployment_status": "not_deployed",
                "canary_weight": 0,
                "created_at": "2026-01-01T00:00:00Z",
                **payload,
            },
        )
    raise AssertionError(f"Unexpected request: {request.method} {request.url}")


def test_client_health_and_projects() -> None:
    with MLAIOpsClient(transport=httpx.MockTransport(handler)) as client:
        assert client.health()["status"] == "ok"
        assert client.list_projects()[0].namespace == "demo"
        project = client.create_project(
            "Churn", description="Retention model", template_version="1.0.0"
        )
        assert project.id == "prj-2"
        assert project.template == "production-ml"
        assert project.template_version == "1.0.0"


def test_client_project_template_catalog() -> None:
    with MLAIOpsClient(transport=httpx.MockTransport(handler)) as client:
        templates = client.list_project_templates()
        assert templates[0].frameworks == ["langgraph"]
        assert (
            client.get_project_template("production-agent", version="1.0.0").recommended_profile
            == "team"
        )


def test_client_deploys_resource_bounded_autoscaling_agent() -> None:
    with MLAIOpsClient(transport=httpx.MockTransport(handler)) as client:
        agent = client.deploy_agent(
            "prj-1",
            "support",
            "1.0.0",
            "registry/support:1",
            "support.graph:build",
            replicas=2,
            max_replicas=5,
            cpu="1",
            memory="2Gi",
            gpu=1,
        )
        assert agent.autoscaling.max_replicas == 5
        assert agent.resources.gpu_type == "nvidia.com/gpu"


def test_client_registers_framework_compatible_serving_image() -> None:
    with MLAIOpsClient(transport=httpx.MockTransport(handler)) as client:
        model = client.register_model(
            "prj-1",
            "churn-xgboost",
            "3",
            "models:/churn-xgboost/3",
            metrics={"accuracy": 0.93},
            serving_image="ghcr.io/acme/churn-xgboost:3",
        )

    assert model.serving_image == "ghcr.io/acme/churn-xgboost:3"
