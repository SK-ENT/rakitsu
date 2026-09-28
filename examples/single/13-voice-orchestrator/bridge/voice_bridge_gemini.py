#!/usr/bin/env python3
"""Gemini Live voice bridge for rakitsu's voice-orchestrator example (see ../README.md).

Connects to Gemini's Live API over WebSocket for live, streaming voice
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

UNTESTED / first draft: not live-tested like the OpenAI bridge. That bridge
exposed unreliable realtime function calling, so both monitor transcripts. Headphones
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
    GEMINI_API_KEY            required — Google account with Live API access
    GEMINI_LIVE_MODEL         optional, default
                               "gemini-2.5-flash-native-audio-preview-09-2025"
    GEMINI_LIVE_VOICE         optional, default "Puck"
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
import time
from urllib.parse import urlencode

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

# GEMINI_API_KEY is also the convention in examples/providers/gemini.yaml
# and internal/scaffold/presets.go; provider.go accepts the resolved config key.
GEMINI_API_KEY = os.environ.get("GEMINI_API_KEY", "")
# Preview ids get superseded: check https://ai.google.dev/gemini-api/docs/live-api
# before relying on this exact model id long-term.
REALTIME_MODEL = os.environ.get("GEMINI_LIVE_MODEL", "gemini-2.5-flash-native-audio-preview-09-2025")
# Puck is a documented prebuilt voice; Google's full voice list may have grown.
REALTIME_VOICE = os.environ.get("GEMINI_LIVE_VOICE", "Puck")
REALTIME_URL = (
    "wss://generativelanguage.googleapis.com/ws/"
    "google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?"
    + urlencode({"key": GEMINI_API_KEY})
)

SAMPLE_RATE_IN = 16000  # Gemini Live input: PCM16LE mono @ 16kHz.
SAMPLE_RATE_OUT = 24000  # Gemini Live output: PCM16LE mono @ 24kHz.
CHANNELS = 1

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
        self._audio_playing = False
        self._pending_dispatches = set()  # keep references so tasks aren't GC'd mid-flight
        self._input_transcript = []
        self._output_transcript = []

    async def connect(self):
        # Unlike OpenAI, authentication is a URL query key, not a bearer header.
        self.ws = await websockets.connect(REALTIME_URL, max_size=None)
        await self.ws.send(json.dumps({
            "setup": {
                "model": REALTIME_MODEL if REALTIME_MODEL.startswith("models/") else f"models/{REALTIME_MODEL}",
                "generationConfig": {
                    "responseModalities": ["AUDIO"],
                    "speechConfig": {
                        "voiceConfig": {
                            "prebuiltVoiceConfig": {"voiceName": REALTIME_VOICE},
                        },
                    },
                },
                # Content/Part shape, as in internal/llm/gemini/provider.go;
                # systemInstruction is NOT a bare string.
                "systemInstruction": {"parts": [{"text": SESSION_INSTRUCTIONS}]},
                # Deliberately zero tools, just like the OpenAI bridge. Live
                # tools cannot be updated mid-session (setup is one-shot), but
                # older worries about tool-forcing are irrelevant now: neither
                # realtime session uses tool-calling to trigger dispatch.
                "tools": [],
                "realtimeInputConfig": {
                    "automaticActivityDetection": {"disabled": False},
                },
                # Presence of these objects enables transcription.
                "inputAudioTranscription": {},
                "outputAudioTranscription": {},
            },
        }))
        # Wait for setup acknowledgement before streaming microphone input.
        async for raw in self.ws:
            event = json.loads(raw)
            if "setupComplete" in event:
                print("[session] setup complete — tools registered: []")
                return
            if "error" in event:
                raise RuntimeError(f"Gemini setup failed: {event['error']}")
            if VERBOSE:
                print(f"[event] {', '.join(event)}")
        raise RuntimeError("Gemini connection closed before setupComplete")

    async def send_mic_audio(self):
        """Captures mic input and streams it as realtimeInput.audio
        messages. Server VAD (configured above) handles turn detection —
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
            samplerate=SAMPLE_RATE_IN, channels=CHANNELS, dtype="int16",
            blocksize=int(SAMPLE_RATE_IN * 0.1), callback=callback,
        ):
            while True:
                chunk = await q.get()
                # sounddevice uses native int16; the wire format is little-endian.
                pcm = np.frombuffer(chunk, dtype=np.int16).astype("<i2").tobytes()
                b64 = base64.b64encode(pcm).decode("ascii")
                await self.ws.send(json.dumps({
                    "realtimeInput": {"audio": {
                        "data": b64, "mimeType": "audio/pcm;rate=16000",
                    }},
                }))

    def _play_audio_chunk(self, b64_audio: str):
        self._audio_playing = True
        pcm = base64.b64decode(b64_audio)
        arr = np.frombuffer(pcm, dtype="<i2").astype(np.int16)
        if self.out_stream is None:
            self.out_stream = sd.RawOutputStream(samplerate=SAMPLE_RATE_OUT, channels=CHANNELS, dtype="int16")
            self.out_stream.start()
        elif not self.out_stream.active:
            # PortAudio requires start() again after abort() — see
            # _interrupt_playback. Without this, every response after the
            # first interruption would silently drop all its audio (write()
            # on a stopped stream does nothing audible).
            self.out_stream.start()
        self.out_stream.write(arr)

    async def _interrupt_playback(self):
        """Discard locally queued audio when Gemini reports interrupted:true.

        The server already interrupted generation; no response.cancel-style
        client message is needed. As with OpenAI, stopping generation cannot
        discard audio already handed to the local speaker buffer.
        """
        was_playing = self._audio_playing
        self._audio_playing = False
        if self.out_stream is not None:
            was_active = self.out_stream.active
            self.out_stream.abort()  # stop() would wait for queued audio to finish
            print(f"[interrupt] interrupted (out_stream.active={was_active}, audio_playing={was_playing})", file=sys.stderr)
        else:
            print("[interrupt] interrupted, no out_stream yet", file=sys.stderr)

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
        # Gemini has no distinct mid-session "system" role like OpenAI's
        # conversation.item.create. A framed user-turn injection is the closest
        # approach; ASSUMPTION: verify live that results are incorporated well.
        await self.ws.send(json.dumps({
            "clientContent": {
                "turns": [{"role": "user", "parts": [{
                    "text": f"(background check on what the user just said returned:) {result}",
                }]}],
                "turnComplete": True,
            },
        }))

    async def receive_loop(self):
        # Gemini automatic VAD detects end-of-turn AND triggers barge-in; no
        # documented interrupt_response/create_response boolean pair or manual
        # create_response:false mode exists. Responses are model-driven, so do
        # not port OpenAI's response.create / _response_active / _response_queue.
        # If live tests show odd overlapping responses, that queueing pattern
        # in voice_bridge.py is the fallback to adapt for result injections.
        async for raw in self.ws:
            event = json.loads(raw)
            if "error" in event:
                print(f"[realtime error] {event['error']}", file=sys.stderr)
            content = event.get("serverContent", {})
            if VERBOSE:
                print(f"[event] {', '.join(event)} / {', '.join(content)}")

            interrupted = content.get("interrupted", False)
            if interrupted:
                await self._interrupt_playback()
            # A subsequent modelTurn is new server-driven audio; no explicit
            # response.created event or client-side cancellation gate is needed.
            if not interrupted:
                for part in content.get("modelTurn", {}).get("parts", []):
                    audio = part.get("inlineData", {})
                    if audio.get("mimeType", "").startswith("audio/pcm") and audio.get("data"):
                        self._play_audio_chunk(audio["data"])

            text = content.get("inputTranscription", {}).get("text", "")
            if text:
                self._input_transcript.append(text)
            text = content.get("outputTranscription", {}).get("text", "")
            if text:
                self._output_transcript.append(text)

            if content.get("generationComplete") or content.get("turnComplete"):
                self._audio_playing = False
                # INFERENCE, NOT a documented input-transcription-complete
                # event: Sept 2026 docs show chunks but no dedicated completed
                # boolean. Model generation/turn completion is the closest
                # exchange-settled boundary. This is the biggest unverified
                # assumption in this port: late chunks or barge-in may change
                # which utterance gets dispatched. Verify with live audio.
                # Drain buffers so generationComplete followed by turnComplete
                # does not print/dispatch the same accumulated text twice.
                transcript = "".join(self._input_transcript).strip()
                self._input_transcript.clear()
                if transcript:
                    print(f"\n[user] {transcript}")
                    if _should_monitor_transcript(transcript):
                        t = asyncio.create_task(self._monitor_transcript(transcript))
                        self._pending_dispatches.add(t)
                        t.add_done_callback(self._pending_dispatches.discard)
                transcript = "".join(self._output_transcript).strip()
                self._output_transcript.clear()
                if transcript:
                    print(f"[assistant] {transcript}")

    async def run(self):
        await self.connect()
        await asyncio.gather(self.send_mic_audio(), self.receive_loop())


def main():
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
    if not GEMINI_API_KEY:
        print("GEMINI_API_KEY is not set", file=sys.stderr)
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
