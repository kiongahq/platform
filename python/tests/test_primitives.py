import pytest

from mlaiops_sdk.pipelines import FunctionStep, Pipeline, PipelineStep
from mlaiops_sdk.tools import register_tool, registered_tools


def test_pipeline_compiles_dependency_order() -> None:
    pipeline = Pipeline("train").add(PipelineStep("prepare", "python:3.11", ["prepare.py"]))
    pipeline.add(
        PipelineStep("train", "trainer:1", ["train.py"], depends_on=["prepare"], retries=2)
    )
    document = pipeline.compile()
    assert document["kind"] == "KiongaPipeline"
    assert document["spec"]["steps"][1]["depends_on"] == ["prepare"]


def test_pipeline_rejects_unknown_dependency() -> None:
    with pytest.raises(ValueError, match="unknown dependencies"):
        Pipeline("bad").add(PipelineStep("train", "trainer:1", [], depends_on=["missing"]))


def test_function_jobs_compile_to_control_plane_definition() -> None:
    pipeline = Pipeline("event-score").add(FunctionStep("extract", "extract-fn"))
    pipeline.add(FunctionStep("score", "score-fn", depends_on=["extract"]))
    definition = pipeline.definition("prj-1", version="3", commit_sha="abc123")
    assert definition["execution_mode"] == "functions"
    assert definition["jobs"][1]["kind"] == "function"
    assert definition["jobs"][1]["depends_on"] == ["extract"]


def test_tool_registration_derives_json_schema() -> None:
    @register_tool(
        name="lookup_test_unique",
        version="1",
        description="Look up an entity",
        tags=["data"],
    )
    def lookup(entity_id: str, limit: int = 3) -> dict:
        return {"id": entity_id, "limit": limit}

    definition = [tool for tool in registered_tools() if tool.name == "lookup_test_unique"][0]
    assert definition.input_schema["required"] == ["entity_id"]
    assert definition.input_schema["properties"]["limit"]["type"] == "integer"
    assert lookup("u1")["limit"] == 3
