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
