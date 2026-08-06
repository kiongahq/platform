"""Typed notebook and automation entry point for the Kionga platform."""

from .client import MLAIOpsClient
from .models import (
    Agent,
    AgentAutoscaling,
    AgentResources,
    AgentSession,
    AgentTrace,
    AuditEvent,
    Connection,
    Function,
    GitRepository,
    Model,
    PipelineRun,
    PipelineDefinition,
    Project,
    ProjectTemplate,
    Readiness,
    Tool,
)

__all__ = [
    "Agent",
    "AgentAutoscaling",
    "AgentResources",
    "AgentMemoryClient",
    "AgentSession",
    "AgentTrace",
    "AuditEvent",
    "Connection",
    "Function",
    "GitRepository",
    "LangfuseContext",
    "MLAIOpsClient",
    "Model",
    "PipelineRun",
    "PipelineDefinition",
    "Project",
    "ProjectTemplate",
    "Readiness",
    "Tool",
    "build_chat_model",
    "get_langgraph_checkpointer",
    "register_tool",
    "trace_llm",
]

_LAZY = {
    "AgentMemoryClient": ("mlaiops_sdk.agents", "AgentMemoryClient"),
    "get_langgraph_checkpointer": ("mlaiops_sdk.agents", "get_langgraph_checkpointer"),
    "build_chat_model": ("mlaiops_sdk.llm", "build_chat_model"),
    "LangfuseContext": ("mlaiops_sdk.tracing", "LangfuseContext"),
    "trace_llm": ("mlaiops_sdk.tracing", "trace_llm"),
    "register_tool": ("mlaiops_sdk.tools", "register_tool"),
}


def __getattr__(name: str):
    """Lazy exports keep the core import light: heavy agent dependencies load
    only when the corresponding symbol is used."""
    try:
        module_name, attribute = _LAZY[name]
    except KeyError:
        raise AttributeError(f"module 'mlaiops_sdk' has no attribute {name!r}") from None
    import importlib

    return getattr(importlib.import_module(module_name), attribute)
