import json
import sys
from types import SimpleNamespace

import httpx
import pytest

from mlaiops_sdk import MLAIOpsClient
from mlaiops_sdk.huggingface import connect, download_model
from mlaiops_sdk.models import Model


def model_data():
    return dict(id="mdl-hf", project_id="p1", name="org/model", version="a" * 40,
                stage="candidate", artifact_uri="hf://org/model@" + "a" * 40,
                metrics={}, created_at="2026-01-01T00:00:00Z", gate_status="needs_evaluation")


def test_download_uses_pinned_revision_and_does_not_execute_code(monkeypatch, tmp_path):
    calls = []

    def download(**kwargs):
        calls.append(kwargs)
        return str(tmp_path / "snapshot")

    monkeypatch.setitem(sys.modules, "huggingface_hub", SimpleNamespace(snapshot_download=download))
    path = download_model(Model(**model_data()), token=False, cache_dir=tmp_path,
                          allow_patterns=["*.safetensors", "*.json"], local_files_only=True)
    assert path == tmp_path / "snapshot"
    assert calls == [dict(repo_id="org/model", revision="a" * 40, token=False,
                          cache_dir=str(tmp_path), allow_patterns=["*.safetensors", "*.json"],
                          local_files_only=True)]


@pytest.mark.parametrize("uri", ["hf://org/model@main", "https://evil.test/model",
                                 "hf://../model@" + "a" * 40, "s3://bucket/model"])
def test_download_rejects_unpinned_or_unsafe_sources(uri):
    data = model_data()
    data["artifact_uri"] = uri
    with pytest.raises(ValueError, match="commit-pinned"):
        download_model(Model(**data))


def test_connect_uses_official_login_without_git_credentials(monkeypatch):
    calls = []
    monkeypatch.setitem(sys.modules, "huggingface_hub", SimpleNamespace(
        login=lambda **kwargs: calls.append(kwargs), whoami=lambda: {"name": "alice"}))
    assert connect("hf_test") == {"name": "alice"}
    assert calls == [{"token": "hf_test", "add_to_git_credential": False}]


def test_sdk_search_and_import():
    def handler(request):
        assert request.headers["Authorization"] == "Bearer platform-token"
        if request.method == "GET":
            assert request.url.params["search"] == "org/model"
            return httpx.Response(200, json={"items": [{"id": "org/model"}]})
        assert json.loads(request.content) == {
            "project_id": "p1", "repo_id": "org/model", "revision": "v1"}
        return httpx.Response(201, json=model_data())

    with MLAIOpsClient(token="platform-token", transport=httpx.MockTransport(handler)) as client:
        assert client.search_huggingface_models("org/model") == [{"id": "org/model"}]
        assert client.import_huggingface_model("p1", "org/model", revision="v1").version == "a" * 40
