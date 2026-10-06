"""Download pinned Hub models in notebooks, IDEs, and training jobs, not the gateway."""
from __future__ import annotations

import re
from pathlib import Path

from .models import Model


def _hub():
    try:
        import huggingface_hub
    except ImportError as exc:
        raise ImportError('Install "mlaiops-sdk[huggingface]" to download Hub models') from exc
    return huggingface_hub


def connect(token: str | None = None) -> dict:
    """Log into this workspace (interactive if omitted); never add a Git credential.

    Credentials saved by the official Hub client are local to the current OS user.
    This is separate from the encrypted console connection. Use HF_TOKEN instead
    for noninteractive jobs; never embed credentials in notebooks or Git.
    """
    hub = _hub()
    hub.login(token=token, add_to_git_credential=False)
    return hub.whoami()


def download_model(
    model: Model,
    *,
    token: str | bool | None = None,
    cache_dir: str | Path | None = None,
    allow_patterns: list[str] | None = None,
    local_files_only: bool = False,
) -> Path:
    """Return the cached snapshot directory, without importing or executing model code.

    Uses the immutable registry commit. token=None uses HF_TOKEN or the workspace's
    Hub login; token=False explicitly downloads anonymously. Downloads may be large;
    select file patterns and provision storage before pulling large checkpoints.
    """
    match = re.fullmatch(
        r"hf://([A-Za-z0-9][A-Za-z0-9._-]{0,95}(?:/[A-Za-z0-9][A-Za-z0-9._-]{0,95})?)@([a-fA-F0-9]{40})",
        model.artifact_uri,
    )
    if not match or ".." in match[1]:
        raise ValueError("Expected a commit-pinned hf:// repository URI from the registry")
    return Path(_hub().snapshot_download(
        repo_id=match[1], revision=match[2], token=token,
        cache_dir=str(cache_dir) if cache_dir is not None else None,
        allow_patterns=allow_patterns, local_files_only=local_files_only,
    ))
