"""Shared transcript filtering and A2A dispatch for the voice bridges.

Dispatch-side configuration lives here; realtime provider configuration stays
in each bridge. Blocking HTTP/polling runs off the audio event loop.
"""

import asyncio
import os
import time
import uuid
from concurrent.futures import ThreadPoolExecutor

import requests

RAKITSU_SERVE_URL = os.environ.get("RAKITSU_SERVE_URL", "http://localhost:9100").rstrip("/")
RAKITSU_API_TOKEN = os.environ.get("RAKITSU_API_TOKEN", "")
RAKITSU_WORKER_CONFIG_NAME = os.environ.get("RAKITSU_WORKER_CONFIG_NAME", "Voice Orchestrator Worker")
RAKITSU_WORKER_AGENT_NAME = os.environ.get("RAKITSU_WORKER_AGENT_NAME", "VoiceWorker")
RAKITSU_WORKER_WORKDIR = os.environ.get("RAKITSU_WORKER_WORKDIR", "")
DISPATCH_MAX_WAIT_SECONDS = float(os.environ.get("DISPATCH_MAX_WAIT_SECONDS", "120"))
DISPATCH_POLL_INTERVAL_SECONDS = float(os.environ.get("DISPATCH_POLL_INTERVAL_SECONDS", "1.0"))
VERBOSE = os.environ.get("VERBOSE", "") == "1"


def _should_monitor_transcript(transcript: str) -> bool:
    """Skip only obvious short chit-chat; let the worker assess everything else."""
    normalized = " ".join(transcript.strip().lower().split()).strip(".,!?;:")
    if not normalized:
        return False
    trivial_phrases = {
        "hello", "hi", "hey", "hello there", "hi there", "hey there",
        "ok", "okay", "thanks", "thank you", "cool", "yeah", "yes", "no",
        "bye", "goodbye", "bye bye", "yep", "nope", "ok thanks", "okay thanks",
    }
    return not (len(normalized.split()) < 3 and normalized in trivial_phrases)


def _http_headers():
    h = {"Content-Type": "application/json"}
    if RAKITSU_API_TOKEN:
        h["Authorization"] = f"Bearer {RAKITSU_API_TOKEN}"
    return h


class RakitsuDispatcher:
    """Dispatch fresh tasks through `rakitsu serve`'s A2A JSON-RPC API.

    SendMessage returns a task immediately; the server runs the agent in
    the background and GetTask exposes completion artifacts or failure
    details. This replaces session-message wait=true (and its server-side
    120s cap), not inbox polling: no send_message tool call is needed to
    retrieve the worker's answer. The tenant is the configured agent name.

    Blocking HTTP and polling stay in the executor so voice streaming can
    continue. DISPATCH_MAX_WAIT_SECONDS is purely a client deadline; on
    timeout we stop polling and report it, without canceling the server task.
    A2A does not carry the old chat-start workdir override.
    """

    def __init__(self):
        self._executor = ThreadPoolExecutor(max_workers=4)

    def dispatch_sync(self, task: str) -> str:
        """Submit and poll within this client's DISPATCH_MAX_WAIT_SECONDS.

        Call via run_in_executor from the asyncio side — running this on
        the event loop would block the WebSocket receive loop.
        """
        deadline = time.monotonic() + DISPATCH_MAX_WAIT_SECONDS
        timeout_reply = (
            "(worker did not respond within the time limit — stopped waiting; "
            "the task may still be running on the server)"
        )

        def rpc(method, params):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise requests.Timeout("dispatch deadline elapsed")
            resp = requests.post(
                f"{RAKITSU_SERVE_URL}/a2a",
                headers=_http_headers(),
                json={
                    "jsonrpc": "2.0",
                    "id": str(uuid.uuid4()),
                    "method": method,
                    "params": params,
                },
                timeout=remaining,
            )
            resp.raise_for_status()
            body = resp.json()
            if "error" in body:
                raise RuntimeError(body["error"].get("message", "A2A request failed"))
            return body["result"]

        def first_text(container):
            parts = container.get("parts") or []
            return parts[0].get("text") if parts else None

        try:
            # Every dispatch is fresh: continuing taskId/contextId and
            # multiple message parts are not supported by this endpoint.
            submitted = rpc("SendMessage", {
                "tenant": RAKITSU_WORKER_AGENT_NAME,
                "message": {
                    "messageId": str(uuid.uuid4()),
                    "role": "ROLE_USER",
                    "parts": [{"text": task}],
                },
            })
            task_id = submitted["task"]["id"]
            while time.monotonic() < deadline:
                # GetTask returns the task directly, unlike SendMessage's
                # result.task wrapper.
                current = rpc("GetTask", {"id": task_id})
                if time.monotonic() >= deadline:
                    return timeout_reply
                status = current.get("status") or {}
                state = status.get("state")
                if state == "TASK_STATE_COMPLETED":
                    artifacts = current.get("artifacts") or []
                    text = first_text(artifacts[0]) if artifacts else None
                    return text or "(worker responded but sent no reply text)"
                if state == "TASK_STATE_FAILED":
                    text = first_text(status.get("message") or {})
                    return f"(worker failed: {text})" if text else "(worker failed without providing an error message)"
                if state == "TASK_STATE_CANCELED":
                    return "(worker task was canceled before it could finish)"
                if state == "TASK_STATE_REJECTED":
                    return "(worker rejected the task and did not run it)"
                remaining = deadline - time.monotonic()
                if remaining > 0:
                    time.sleep(min(DISPATCH_POLL_INTERVAL_SECONDS, remaining))
        except requests.Timeout:
            return timeout_reply
        return timeout_reply

    async def dispatch(self, task: str) -> str:
        loop = asyncio.get_running_loop()
        return await loop.run_in_executor(self._executor, self.dispatch_sync, task)
