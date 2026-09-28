#!/usr/bin/env python3
"""Voice bridge for rakitsu's voice-orchestrator example (see ../README.md).

Connects to OpenAI's Realtime API over WebSocket for live, streaming voice
conversation (mic in, speaker out). Completed user transcripts trigger a
background monitor dispatch to the rakitsu agent, except for obvious trivial
chit-chat. Dispatch runs as a background asyncio task — NOT awaited inline
in the WebSocket receive loop — so the conversation keeps going while the
worker runs. Results are delivered back into the realtime session to speak
naturally; replies starting with "no action needed" remain silent. The
realtime model has no tools and is not responsible for triggering dispatch.

Dispatch uses rakitsu's A2A JSON-RPC endpoint (POST /a2a). SendMessage
starts a fresh task and returns immediately while the agent runs in the
background; GetTask polls for its terminal status and result artifacts.
Unlike the old session-message wait=true path, this has no server-side
120s wait cap: the bridge owns its timeout. See cmd/rakitsu/a2a_serve.go.

Live testing exposed unreliable realtime function calling under audio-driven
conversation, so monitoring is triggered by transcripts instead. Headphones
are still required to avoid acoustic echo, and the worker needs tools to
act on requests (see this example's README).

The worker can now do real development work (read/write files, run a
whitelisted git/go/etc.). A2A does not accept this bridge's per-call
RAKITSU_WORKER_WORKDIR override; scope the worker on the server deliberately,
since a voice command is a much less careful trigger than a typed one.

Model/endpoint names live here, in env vars, never hardcoded as the only
option — same rule as any external media-generation script: a provider
renaming or deprecating a realtime model is a config change, not a rakitsu (or even this script's) rebuild.

Env vars:
    OPENAI_API_KEY            required — OpenAI account with Realtime access
    OPENAI_REALTIME_MODEL     optional, default "gpt-realtime-2.1"
    OPENAI_REALTIME_VOICE     optional, default "alloy"
    OPENAI_TRANSCRIBE_LANGUAGE optional, default "en"
    OPENAI_VAD_THRESHOLD      optional, default 0.4
    OPENAI_VAD_PREFIX_PADDING_MS optional, default 200
    OPENAI_VAD_SILENCE_DURATION_MS optional, default 500
    RAKITSU_SERVE_URL         optional, default "http://localhost:9100"
    RAKITSU_API_TOKEN         required if `rakitsu serve` was started with
                               RAKITSU_API_TOKEN set (it should be — see README);
                               the control-plane API token authenticates /a2a
    RAKITSU_WORKER_AGENT_NAME  optional, default "VoiceWorker" — A2A tenant;
                               must match worker-config.yaml's agents[].name
    RAKITSU_WORKER_CONFIG_NAME optional, default "Voice Orchestrator Worker"
                               — retained for the startup display only;
                               A2A routes by agent name, not config name
    RAKITSU_WORKER_WORKDIR     legacy setting, not sent by A2A dispatch.
                               Configure the worker's tool scope server-side;
                               startup messages still display this setting,
                               but it does not scope A2A tasks.
    DISPATCH_MAX_WAIT_SECONDS  optional, default 120 — this client's overall
                               dispatch deadline, not a server-side hard cap
    DISPATCH_POLL_INTERVAL_SECONDS optional, default 1.0 — seconds between
                               GetTask calls while the worker is running
    VERBOSE                    optional, default off — set to "1" to print every
                               realtime server event type, not just the ones this
                               script already handles (useful when nothing seems
                               to be happening and you need to see what the server
                               actually sent)

Requires: websockets, sounddevice, numpy, requests
    pip install websockets sounddevice numpy requests
"""

import asyncio
import base64
import json
import os
import subprocess
import sys
import threading
import time
import traceback
from collections import deque

import numpy as np
import sounddevice as sd
import websockets

from rakitsu_dispatch import (
    RakitsuDispatcher,
    _should_monitor_transcript,
    _http_headers,
    RAKITSU_SERVE_URL,
    RAKITSU_API_TOKEN,
    RAKITSU_WORKER_CONFIG_NAME,
    RAKITSU_WORKER_AGENT_NAME,
    RAKITSU_WORKER_WORKDIR,
    DISPATCH_MAX_WAIT_SECONDS,
    DISPATCH_POLL_INTERVAL_SECONDS,
    VERBOSE,
)

OPENAI_API_KEY = os.environ.get("OPENAI_API_KEY", "")
REALTIME_MODEL = os.environ.get("OPENAI_REALTIME_MODEL", "gpt-realtime-2.1")
REALTIME_VOICE = os.environ.get("OPENAI_REALTIME_VOICE", "alloy")
REALTIME_URL = f"wss://api.openai.com/v1/realtime?model={REALTIME_MODEL}"

SAMPLE_RATE = 24000  # PCM16 mono @ 24kHz is the Realtime API's documented audio format.
CHANNELS = 1

# Bumped by hand on every real change to this file — the reliable source of
# truth when deployed to /tmp (not a git repo, so the git-hash attempt below
# always prints "unknown" there).
BRIDGE_VERSION = "2026-09-27-01"

# HEADPHONES REQUIRED. This script has no acoustic echo cancellation (AEC)
# between the sounddevice input and output streams. With speakers, the mic
# picks up the assistant's own voice and the model ends up "hearing
# itself" — confirmed live: the transcribed [user] turns were literally
# the assistant's last reply played back through the mic. An earlier
# version of this script muted the mic while the assistant talked to work
# around that, but that also kills mid-sentence interruption — the mic
# isn't listening during exactly the window a real interruption would
# happen in. Headphones remove the speaker-to-mic path entirely, so there
# is no echo to guard against and no need to trade away interruption to
# get rid of it. There's no software fix here for open speakers without
# real AEC (a genuinely bigger addition, not built here) — use headphones.

SESSION_INSTRUCTIONS = (
    "You are a helpful voice assistant. Keep the conversation natural and "
    "helpful. Background tasks are handled automatically while you talk "
    "with the user. When background results arrive, incorporate them into "
    "the conversation and report them in your own words, spoken naturally."
)


class VoiceBridge:
    def __init__(self):
        self.dispatcher = RakitsuDispatcher()
        self.ws = None
        self.out_stream = None
        self._interrupted = False
        self._audio_playing = False
        self._pending_dispatches = set()  # keep references so tasks aren't GC'd mid-flight
        self._response_active = False
        self._response_queue = deque()
        self._last_event_time = time.monotonic()
        self._watchdog_warned = False

    async def connect(self):
        # GA Realtime API (the Beta API — OpenAI-Beta: realtime=v1 header,
        # flat session config — was removed 2026-05-12; confirmed live
        # against this exact script: it fails the handshake with
        # "invalid_request_error.beta_api_shape_disabled"). No auth header
        # beyond the bearer token is needed for GA.
        headers = {"Authorization": f"Bearer {OPENAI_API_KEY}"}
        self.ws = await websockets.connect(REALTIME_URL, additional_headers=headers, max_size=None)
        await self.ws.send(json.dumps({
            "type": "session.update",
            "session": {
                "type": "realtime",
                "instructions": SESSION_INSTRUCTIONS,
                "audio": {
                    "input": {
                        "format": {"type": "audio/pcm", "rate": SAMPLE_RATE},
                        # threshold is the main lever for speech_started latency:
                        # lower threshold = fires sooner. silence_duration_ms is
                        # end-of-turn only, unrelated to speech-start latency.
                        "turn_detection": {
                            "type": "server_vad",
                            "threshold": float(os.environ.get("OPENAI_VAD_THRESHOLD", "0.4")),
                            "prefix_padding_ms": int(os.environ.get("OPENAI_VAD_PREFIX_PADDING_MS", "200")),
                            "silence_duration_ms": int(os.environ.get("OPENAI_VAD_SILENCE_DURATION_MS", "500")),
                            # Explicit, not relying on the documented default —
                            # this is the server-side half of barge-in: cancel
                            # generation the instant VAD detects the user
                            # talking over the assistant. The client-side half
                            # (stopping audio already queued locally) is
                            # _interrupt_playback() below.
                            "interrupt_response": True,
                            "create_response": False,
                        },
                        # Live logs confirmed wrong-language auto-detection for short/noisy audio; pin language for reliable transcripts.
                        "transcription": {"model": "gpt-4o-transcribe", "language": os.environ.get("OPENAI_TRANSCRIBE_LANGUAGE", "en")},
                    },
                    "output": {
                        "format": {"type": "audio/pcm", "rate": SAMPLE_RATE},
                        "voice": REALTIME_VOICE,
                    },
                },
                "tools": [],
            },
        }))

    async def send_mic_audio(self):
        """Captures mic input and streams it as input_audio_buffer.append
        events. Server VAD (configured above) handles turn detection —
        this just keeps appending raw PCM16 continuously."""
        loop = asyncio.get_running_loop()
        q: asyncio.Queue = asyncio.Queue()
        last_level_print = 0.0

        def callback(indata, frames, time_info, status):
            nonlocal last_level_print
            if status:
                print(f"[mic] {status}", file=sys.stderr)
            now = time.monotonic()
            if now - last_level_print >= 1.0:
                arr = np.frombuffer(indata, dtype=np.int16)
                rms = float(np.sqrt(np.mean(arr.astype(np.float64) ** 2))) if len(arr) else 0.0
                print(f"[mic] level={rms:.0f}", file=sys.stderr)
                last_level_print = now
            loop.call_soon_threadsafe(q.put_nowait, bytes(indata))

        with sd.RawInputStream(
            samplerate=SAMPLE_RATE, channels=CHANNELS, dtype="int16",
            blocksize=int(SAMPLE_RATE * 0.1), callback=callback,
        ):
            while True:
                chunk = await q.get()
                b64 = base64.b64encode(chunk).decode("ascii")
                await self.ws.send(json.dumps({"type": "input_audio_buffer.append", "audio": b64}))

    def _play_audio_chunk(self, b64_audio: str):
        self._audio_playing = True
        pcm = base64.b64decode(b64_audio)
        arr = np.frombuffer(pcm, dtype=np.int16)
        if self.out_stream is None:
            self.out_stream = sd.RawOutputStream(samplerate=SAMPLE_RATE, channels=CHANNELS, dtype="int16")
            self.out_stream.start()
        elif not self.out_stream.active:
            # PortAudio requires start() again after abort() — see
            # _interrupt_playback. Without this, every response after the
            # first interruption would silently drop all its audio (write()
            # on a stopped stream does nothing audible).
            self.out_stream.start()
        self.out_stream.write(arr)

    async def _interrupt_playback(self):
        """Stops the assistant's audio immediately, mid-word if needed.

        With a WebSocket connection (this script), the CLIENT owns audio
        playback, not the server — server_vad's interrupt_response:true
        only stops the model from generating further audio once it detects
        the user talking over it; it does nothing about audio this script
        already handed to the local speaker buffer via _play_audio_chunk.
        Without this, a real barge-in still gets ignored from the user's
        perspective: the server did its part, but whatever was already
        queued for the speaker just keeps playing anyway. Called on every
        input_audio_buffer.speech_started — a no-op if nothing is playing.

        Also sends response.cancel explicitly rather than relying solely on
        turn_detection.interrupt_response — belt-and-suspenders in case the
        server-side auto-cancel doesn't fire for some reason; a response.cancel
        with no active response is a harmless no-op per the API.
        Skip the send when no response is active client-side to reduce log
        noise; barge-in playback interruption is unaffected.
        """
        was_playing = self._audio_playing
        self._audio_playing = False
        self._interrupted = True
        if self.out_stream is not None:
            was_active = self.out_stream.active
            self.out_stream.abort()  # discards queued audio; .stop() would wait for it to finish, which we don't want
            print(f"[interrupt] speech_started (out_stream.active={was_active}, audio_playing={was_playing})", file=sys.stderr)
        else:
            print("[interrupt] speech_started, no out_stream yet", file=sys.stderr)
        if self._response_active:
            await self.ws.send(json.dumps({"type": "response.cancel"}))

    async def _request_response(self, pre_send=None):
        """Send response.create, or queue it if a response is already active.

        pre_send, if given, is an async callable awaited immediately before
        response.create is sent (used by the monitor to send its
        conversation.item.create for the injected result) — keeping it
        paired with response.create in the queue preserves ordering: the
        injected system message always lands right before its response.create,
        even if it had to wait for a prior response to finish.
        """
        if self._response_active:
            self._response_queue.append(pre_send)
            return
        if pre_send is not None:
            await pre_send()
        await self.ws.send(json.dumps({"type": "response.create"}))
        self._response_active = True

    async def _monitor_transcript(self, transcript: str):
        """Dispatch in a background task so the receive loop stays responsive."""
        print(f"[dispatch] transcript: {transcript}")
        try:
            result = await self.dispatcher.dispatch(transcript)
            print(f"[dispatch] result: {result}")
        except Exception as exc:  # noqa: BLE001 — report failure into the conversation, don't crash the bridge
            print("[agent] worker call failed, composing reply anyway...")
            result = f"(dispatch failed: {exc})"
        if result.strip().lower().startswith("no action needed"):
            return
        async def _inject():
            await self.ws.send(json.dumps({
                "type": "conversation.item.create",
                "item": {
                    "type": "message",
                    "role": "system",
                    "content": [{
                        "type": "input_text",
                        "text": f"(background check on what the user just said returned:) {result}",
                    }],
                },
            }))
        await self._request_response(pre_send=_inject)

    async def receive_loop(self):
        try:
            async for raw in self.ws:
                event = json.loads(raw)
                etype = event.get("type", "")
                self._last_event_time = time.monotonic()
                self._watchdog_warned = False

                if etype == "response.output_audio.delta":
                    if not self._interrupted:
                        self._play_audio_chunk(event["delta"])

                elif etype == "response.output_audio.done":
                    self._audio_playing = False

                elif etype == "input_audio_buffer.speech_started":
                    await self._interrupt_playback()

                elif etype == "conversation.item.input_audio_transcription.completed":
                    transcript = event.get("transcript", "")
                    print(f"\n[user] {transcript}")
                    # With create_response:false, start the conversational reply manually.
                    await self._request_response()
                    # Monitoring runs independently of the realtime model's reply.
                    if _should_monitor_transcript(transcript):
                        t = asyncio.create_task(self._monitor_transcript(transcript))
                        self._pending_dispatches.add(t)
                        t.add_done_callback(self._pending_dispatches.discard)

                elif etype == "response.created":
                    print("[agent] thinking...")
                    self._interrupted = False

                elif etype == "response.done":
                    self._response_active = False
                    if self._response_queue:
                        pre_send = self._response_queue.popleft()
                        asyncio.create_task(self._request_response(pre_send))

                elif etype == "response.output_audio_transcript.done":
                    transcript = event.get("transcript", "")
                    print(f"[assistant] {transcript}")

                elif etype == "session.updated":
                    tool_names = [t.get("name") for t in event.get("session", {}).get("tools", [])]
                    print(f"[session] updated — tools registered: {tool_names}")

                elif etype == "error":
                    print(f"[realtime error] {event}", file=sys.stderr)

                elif VERBOSE and not etype.endswith(".delta"):
                    # Catch-all so an unexpected/renamed event type is visible
                    # instead of silently doing nothing — set VERBOSE=1 to see
                    # every event type the server sends (handy for diagnosing
                    # "nothing is happening" reports).
                    print(f"[event] {etype}")
        except Exception as exc:
            print("[receive_loop] CRASHED:", exc, file=sys.stderr)
            traceback.print_exc()
            raise

    async def _watchdog(self):
        while True:
            await asyncio.sleep(2)
            elapsed = time.monotonic() - self._last_event_time
            if self._response_active and elapsed > 10 and not self._watchdog_warned:
                print(
                    f"[watchdog] no realtime events received for {elapsed:.0f}s while a response is active — connection may be stalled",
                    file=sys.stderr,
                )
                self._watchdog_warned = True

    async def run(self):
        await self.connect()
        results = await asyncio.gather(
            self.send_mic_audio(), self.receive_loop(), self._watchdog(),
            return_exceptions=True,
        )
        for result in results:
            if isinstance(result, Exception):
                print(f"[run] task failed: {result!r}", file=sys.stderr)
                raise result


def main():
    print(f"Voice bridge version: {BRIDGE_VERSION}")
    try:
        script_dir = os.path.dirname(os.path.abspath(__file__))
        short_hash = subprocess.check_output(
            ["git", "rev-parse", "--short", "HEAD"],
            cwd=script_dir, stderr=subprocess.DEVNULL, text=True, timeout=2,
        ).strip()
        porcelain_output = subprocess.check_output(
            ["git", "status", "--porcelain"],
            cwd=script_dir, stderr=subprocess.DEVNULL, text=True, timeout=2,
        )
        print(f"Voice bridge build: {short_hash}{'+dirty' if porcelain_output.strip() else ''}")
    except Exception:
        print("Voice bridge build: unknown")
    if not OPENAI_API_KEY:
        print("OPENAI_API_KEY is not set", file=sys.stderr)
        sys.exit(1)
    print(f"Connecting to {REALTIME_MODEL} realtime session...")
    print(f"Dispatching to rakitsu worker {RAKITSU_WORKER_CONFIG_NAME!r} via {RAKITSU_SERVE_URL}")
    if RAKITSU_WORKER_WORKDIR:
        print(f"Worker file/command tools scoped to: {RAKITSU_WORKER_WORKDIR}")
    else:
        print(
            "RAKITSU_WORKER_WORKDIR is not set — the worker's file/command "
            "tools (if worker-config.yaml grants any) resolve against "
            "`rakitsu serve`'s own working directory. Set "
            "RAKITSU_WORKER_WORKDIR to scope this deliberately.",
            file=sys.stderr,
        )
    bridge = VoiceBridge()
    try:
        asyncio.run(bridge.run())
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
