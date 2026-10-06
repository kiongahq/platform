"""Framework adapters for Kionga's existing graph_module deployment contract.

Only the boundary uses LangChain message types; framework code receives a plain
request. Factories create fresh agents per call, avoiding shared mutable sessions.
"""
from __future__ import annotations

import asyncio
import inspect
import json
from dataclasses import dataclass
from typing import Any, Callable


@dataclass(frozen=True)
class AgentRequest:
    message: str
    session_id: str
    user_id: str


@dataclass(frozen=True)
class AgentResult:
    reply: Any
    input_tokens: int | None = None
    output_tokens: int | None = None


def agent_factory(framework: str = "custom"):
    """Mark a module factory as framework-managed (no platform LLM/checkpointer).

    The factory must return an adapter with ainvoke/astream. Its dependencies,
    provider configuration, and state store belong to its deployment image.
    """
    def decorate(factory):
        factory.kionga_framework = framework
        return factory
    return decorate


def _text(value: Any) -> str:
    if isinstance(value, str):
        return value
    if hasattr(value, "model_dump"):
        value = value.model_dump(mode="json")
    return json.dumps(value, ensure_ascii=False)


class CallableAdapter:
    """Adapt async or sync run(AgentRequest) -> AgentResult to the platform.

    Streaming is buffered: one completed reply, not fake incremental tokens.
    Sync handlers run on a worker thread. Durable state/cleanup is the handler's
    responsibility; it must scope persistence by authenticated tenant/user/session.
    """
    streaming_mode = "buffered"
    framework = "custom"

    def __init__(self, run: Callable):
        self.run = run

    async def ainvoke(self, state: dict, config: dict | None = None) -> dict:
        from langchain_core.messages import AIMessage

        config = config or {}
        message = state["messages"][-1]
        request = AgentRequest(
            message=message.content,
            session_id=config.get("configurable", {}).get("thread_id", ""),
            user_id=config.get("metadata", {}).get("langfuse_user_id", ""),
        )
        if inspect.iscoroutinefunction(self.run):
            result = await self.run(request)
        else:
            result = await asyncio.to_thread(self.run, request)
            if inspect.isawaitable(result):
                result = await result
        if not isinstance(result, AgentResult):
            raise TypeError("Agent adapter must return AgentResult")
        known = result.input_tokens is not None and result.output_tokens is not None
        usage = None
        if known:
            for value in (result.input_tokens, result.output_tokens):
                if type(value) is not int or value < 0:
                    raise ValueError("Token counts must be non-negative integers")
            usage = dict(input_tokens=result.input_tokens, output_tokens=result.output_tokens,
                         total_tokens=result.input_tokens + result.output_tokens)
        return {"messages": [AIMessage(content=_text(result.reply), usage_metadata=usage)],
                "usage_available": known}

    async def astream(self, state: dict, config: dict | None = None, **_):
        result = await self.ainvoke(state, config)
        yield result["messages"][0], {"streaming_mode": "buffered"}


class AgnoAdapter(CallableAdapter):
    """factory(AgentRequest) -> a fresh Agno Agent or Team supporting arun."""
    framework = "agno"

    def __init__(self, factory: Callable):
        async def run(request):
            agent = factory(request)
            response = await agent.arun(request.message, session_id=request.session_id,
                                       user_id=request.user_id, stream=False)
            metrics = getattr(response, "metrics", None)
            def metric(name):
                return metrics.get(name) if isinstance(metrics, dict) else getattr(metrics, name, None)
            return AgentResult(response.content, metric("input_tokens"), metric("output_tokens"))
        super().__init__(run)


class NooaAdapter(CallableAdapter):
    """factory(AgentRequest) -> a fresh NOOA object with a selected async method.

    The selected method accepts one message argument. Wrap richer typed inputs in
    CallableAdapter. This bridge does not sandbox generated code or persist state.
    """
    framework = "nooa"

    def __init__(self, factory: Callable, method: str):
        if not method.isidentifier() or method.startswith("_"):
            raise ValueError("Choose a public NOOA method")

        async def run(request):
            agent = factory(request)
            fn = getattr(agent, method)
            result = fn(request.message)
            if not inspect.isawaitable(result):
                raise TypeError("NOOA entrypoint must be an async method")
            # NOOA result types do not provide a universal per-turn usage schema.
            return AgentResult(await result)
        super().__init__(run)
