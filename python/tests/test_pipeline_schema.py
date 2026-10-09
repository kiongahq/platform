"""The published JSON Schema accepts the shared example and rejects
structural mistakes; Go parses the same example (pipelinespec tests)."""

import copy
import json
from pathlib import Path

import pytest

jsonschema = pytest.importorskip("jsonschema")
yaml = pytest.importorskip("yaml")

ROOT = Path(__file__).resolve().parents[2]
SCHEMA = json.loads((ROOT / "contracts/pipeline/v1.schema.json").read_text())
EXAMPLE = yaml.safe_load((ROOT / "contracts/pipeline/examples/branched.kionga.yaml").read_text())


def errors(document):
    return list(jsonschema.Draft202012Validator(SCHEMA).iter_errors(document))


def test_example_is_valid():
    assert errors(EXAMPLE) == []


@pytest.mark.parametrize("mutate, fragment", [
    (lambda d: d["spec"]["nodes"][0].pop("image"), "image"),
    (lambda d: d["spec"]["nodes"][0].update(id="Bad_Id"), "Bad_Id"),
    (lambda d: d["spec"]["nodes"][0].update(dependson=["x"]), "dependson"),
    (lambda d: d["spec"]["triggers"][0].pop("cron"), "cron"),
    (lambda d: d["spec"]["triggers"][0].update(cron="@daily"), "@daily"),
    (lambda d: d["spec"]["nodes"][3]["retry"].update(max=11), "11"),
    (lambda d: d.update(kind="Job"), "Pipeline"),
])
def test_structural_mistakes_are_rejected(mutate, fragment):
    document = copy.deepcopy(EXAMPLE)
    mutate(document)
    found = errors(document)
    assert found, "invalid document accepted"
    assert any(fragment in error.message or fragment in json.dumps(list(error.path)) for error in found)
