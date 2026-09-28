#!/usr/bin/env python3
"""Local turn-based voice bridge for rakitsu's voice-orchestrator example
(see ../README.md).

Unlike `voice_bridge.py` (OpenAI Realtime) and `voice_bridge_gemini.py`
(Gemini Live), this bridge is NOT a realtime duplex conversation — there is
no live WebSocket connection and no word-by-word streaming transcript. It
stays turn-based: a full utterance is recorded, then transcribed and
dispatched as one unit. What matters here is how a turn's boundaries are
found: instead of a manual Enter-to-start/Enter-to-stop key press, the mic
is monitored continuously and turn boundaries are detected automatically
with voice-activity detection (VAD) — see "Auto turn detection" below.

Both the speech-to-text and text-to-speech steps run entirely on this
machine — no cloud voice API and no per-minute realtime audio billing:

    - STT: faster-whisper (local Whisper via CTranslate2, CPU or GPU)
    - TTS: Piper (small, fast, local neural TTS, cross-platform — chosen
      over macOS `say` specifically so this example isn't tied to macOS)
    - VAD: Silero VAD, ONNX variant (via the `silero-vad` package), for
      turn-start/turn-end detection and playback barge-in — see below

This is a DIFFERENT axis from the dispatch target: the dispatched
`VoiceWorker` agent (`worker-config.yaml`) still uses its own separately
configured LLM provider (`default_provider: openai` / `gpt-4o-mini` by
default) exactly as with the other two bridges — only the voice layer
(microphone -> text, text -> speaker) is local here. Dispatch reuses
`bridge/rakitsu_dispatch.py` UNCHANGED: `RakitsuDispatcher().dispatch_sync()`
called directly, synchronously, once per completed turn. There is no
realtime audio event loop to protect here (unlike the other two bridges,
which must run dispatch off to the side so mic streaming keeps going), so
no asyncio is needed anywhere in this script.

Auto turn detection (default mode): the mic is opened once, for the whole
run, in fixed-size 512-sample (32ms @ 16kHz) blocks — the exact window
size Silero's model takes per inference call, so no re-chunking is needed.
A background thread classifies every block as speech/silence. Once speech
starts, audio is buffered; the turn ends (and gets transcribed + dispatched)
after VAD_SILENCE_MS of trailing silence. A turn shorter than
VAD_MIN_SPEECH_MS of actual speech is treated as a noise blip and discarded
rather than dispatched. Every dispatched turn still passes through
`_should_monitor_transcript` from `rakitsu_dispatch` to skip one- or
two-word chit-chat ("hi", "thanks", "ok") so a stray short utterance doesn't
burn a worker dispatch.

Playback barge-in: while Piper is speaking a dispatch result back, the same
background VAD thread keeps monitoring the mic. The instant it detects
speech, it calls `sounddevice.stop()` (safe to call from another thread —
this is the documented, non-destructive way to end an `sd.play()` stream;
no raw thread kill involved) to end playback immediately, and the audio
frame that triggered the interrupt is NOT discarded — it becomes the first
frame of the next turn's buffer, so nothing the user says as they interrupt
is lost.

Push-to-talk fallback (VAD_ENABLED=0): the original manual Enter-to-start/
Enter-to-stop loop is kept as a cheap opt-out — no VAD, no
barge-in, one turn at a time, exactly as before. It's a single `if` branch
in `main()`, not a maintained second code path, so keeping it cost little;
if the two paths ever diverge enough to become a real maintenance burden,
drop this one first.

Env vars:
    WHISPER_MODEL_SIZE        optional, default "small" — faster-whisper
                               model size (tiny/base/small/medium/large-v3
                               etc.). "small" is a reasonable balance of
                               accuracy vs. speed/memory on a laptop CPU;
                               drop to "base" or "tiny" for faster turns on
                               weaker hardware, or go up to "medium"/
                               "large-v3" (needs more RAM/VRAM and a GPU to
                               stay fast) for better accuracy.
    WHISPER_DEVICE             optional, default "cpu" — "cpu" or "cuda"
    WHISPER_COMPUTE_TYPE        optional, default "int8" — CTranslate2
                               compute type; "int8" is fastest/lowest-memory
                               on CPU, "float16" is typical for "cuda"
    WHISPER_LANGUAGE           optional, default "en" — forced transcription
                               language; unset (empty string) to auto-detect
    RECORD_MAX_SECONDS         optional, default 300 — a push-to-talk
                               recording auto-stops capturing after this
                               many seconds even without a second Enter
                               press, so a forgotten/unattended recording
                               can't grow memory unbounded (~1.9MB/minute
                               at 16kHz mono int16). The turn still only
                               proceeds once Enter is pressed.
    PIPER_MODEL_PATH            required — path to a Piper `.onnx` voice
                               model file (the matching `.onnx.json` must
                               sit alongside it). Download voices from
                               https://github.com/rhasspy/piper/blob/master/VOICES.md
    PIPER_BINARY                optional, default "piper" — the Piper CLI
                               executable on PATH (or a full path to it)
    VAD_ENABLED                 optional, default "1" — set to "0" to fall
                               back to the old manual push-to-talk loop
                               (Enter to start, Enter to stop) with no VAD
                               and no playback barge-in. Useful if Silero
                               VAD misbehaves on a given mic/room, or to
                               compare behavior against the original push-to-talk mode.
    VAD_THRESHOLD               optional, default "0.5" — Silero speech
                               probability (0.0-1.0) above which a 32ms
                               frame counts as speech. Lower catches quieter
                               speech but triggers more on background noise;
                               higher is stricter but risks clipping soft
                               speech onsets/offsets.
    VAD_SILENCE_MS               optional, default "800" — trailing silence
                               (in ms) required after speech before a turn
                               is considered finished and gets transcribed.
                               Shorter feels snappier but risks cutting off
                               a turn during a mid-sentence pause; longer is
                               more forgiving of pauses but adds latency
                               before every dispatch.
    VAD_MIN_SPEECH_MS            optional, default "200" — a completed turn
                               with less total speech than this is treated
                               as a noise blip (cough, chair creak) and
                               discarded instead of transcribed/dispatched.
                               Raise it if short real utterances ("no",
                               "stop") matter to you less than filtering
                               noise; lower it if short real replies are
                               being dropped.
    RAKITSU_SERVE_URL          optional, default "http://localhost:9100"
    RAKITSU_API_TOKEN          required if `rakitsu serve` was started with
                               RAKITSU_API_TOKEN set (it should be — see
                               README); the control-plane API token
                               authenticates /a2a
    RAKITSU_WORKER_AGENT_NAME  optional, default "VoiceWorker" — A2A tenant;
                               must match worker-config.yaml's agents[].name
    RAKITSU_WORKER_CONFIG_NAME optional, default "Voice Orchestrator Worker"
                               — retained for the startup display only
    DISPATCH_MAX_WAIT_SECONDS  optional, default 120 — this client's overall
                               dispatch deadline, not a server-side hard cap
    DISPATCH_POLL_INTERVAL_SECONDS optional, default 1.0 — seconds between
                               GetTask calls while the worker is running
    VERBOSE                    optional, default off — set to "1" for extra
                               diagnostic prints (recording duration, sample
                               counts, VAD frame probabilities, etc.)

Setup:
    pip install faster-whisper sounddevice numpy requests silero-vad
    # Piper is used as a CLI subprocess, not a Python import — install it
    # separately (a prebuilt binary, or `pip install piper-tts` which also
    # installs a `piper` console script) and download at least one voice
    # model (.onnx + .onnx.json pair) from the VOICES.md link above.
    export PIPER_MODEL_PATH=/path/to/en_US-lessac-medium.onnx

Run:
    python3 examples/single/13-voice-orchestrator/bridge/voice_bridge_local.py

`--help` works without faster-whisper, silero-vad, or Piper installed — all
are deferred imports (faster-whisper, silero-vad) / a subprocess call
(Piper), matching this directory's existing convention (see voice_bridge.py,
voice_bridge_gemini.py).
"""

import argparse
import os
import queue
import subprocess
import sys
import tempfile
import threading
import time
import wave

import numpy as np
import sounddevice as sd

from rakitsu_dispatch import (
    RakitsuDispatcher,
    _should_monitor_transcript,
    RAKITSU_SERVE_URL,
    RAKITSU_WORKER_CONFIG_NAME,
    RAKITSU_WORKER_AGENT_NAME,
)

WHISPER_MODEL_SIZE = os.environ.get("WHISPER_MODEL_SIZE", "small")
WHISPER_DEVICE = os.environ.get("WHISPER_DEVICE", "cpu")
WHISPER_COMPUTE_TYPE = os.environ.get("WHISPER_COMPUTE_TYPE", "int8")
WHISPER_LANGUAGE = os.environ.get("WHISPER_LANGUAGE", "en")

PIPER_MODEL_PATH = os.environ.get("PIPER_MODEL_PATH", "")
PIPER_BINARY = os.environ.get("PIPER_BINARY", "piper")

VAD_ENABLED = os.environ.get("VAD_ENABLED", "1") != "0"
VAD_THRESHOLD = float(os.environ.get("VAD_THRESHOLD", "0.5"))
VAD_SILENCE_MS = int(os.environ.get("VAD_SILENCE_MS", "800"))
VAD_MIN_SPEECH_MS = int(os.environ.get("VAD_MIN_SPEECH_MS", "200"))

# Whisper models are trained on 16kHz mono PCM16 — this is the format both
# recording and the .wav file handed to faster-whisper use. Piper's output
# sample rate is fixed per-voice-model (declared in the model's .onnx.json)
# and is read back from the WAV file Piper writes, not assumed here.
RECORD_SAMPLE_RATE = 16000
RECORD_CHANNELS = 1
# At 16kHz mono int16, ~1.9MB/minute of accumulated `frames` — an
# unattended or forgotten-to-stop recording would otherwise grow this
# unbounded. This caps a single push-to-talk turn; it is not a realtime
# duplex session, so a long dispatch-worthy request should still fit.
RECORD_MAX_SECONDS = int(os.environ.get("RECORD_MAX_SECONDS", "300"))

# Silero's model takes a FIXED-size window per inference call, not an
# arbitrary chunk: 512 samples (32ms) at 16kHz, 256 samples at 8kHz. This
# is a model-architecture constraint (see the `silero-vad` package's own
# VADIterator), not a tunable knob, which is why it's a constant here and
# not an env var. The mic InputStream below is opened with this exact
# blocksize so every callback delivers one ready-to-classify frame.
VAD_FRAME_SAMPLES = 512


class SileroVAD:
    """Thin wrapper around the official `silero-vad` package's ONNX model
    (`load_silero_vad(onnx=True)`). Deferred import — matches this file's
    existing convention for optional heavy dependencies (see transcribe()'s
    faster_whisper import).

    The package's OnnxWrapper still takes `torch` to build the input tensor
    and hold per-instance recurrent state between calls; no PyTorch
    JIT/checkpoint model is loaded, it's an onnxruntime.InferenceSession
    under the hood. torch ships as CPU-only prebuilt wheels with no native
    compile step on any of Windows/macOS/Linux/ARM, so this doesn't
    reintroduce the cross-platform build problem this bridge specifically
    avoided by choosing Silero's ONNX variant over `webrtcvad`.
    """

    def __init__(self):
        from silero_vad import load_silero_vad  # deferred: optional dependency

        self._model = load_silero_vad(onnx=True)

    def speech_prob(self, frame: np.ndarray) -> float:
        """`frame` must be exactly VAD_FRAME_SAMPLES int16 mono samples at
        RECORD_SAMPLE_RATE. Returns Silero's speech probability, 0.0-1.0."""
        import torch  # deferred: pulled in by silero-vad, not imported at module load

        audio = torch.from_numpy(frame.astype(np.float32) / 32768.0)
        with torch.no_grad():
            return float(self._model(audio, RECORD_SAMPLE_RATE).item())

    def reset(self) -> None:
        """Clear recurrent state between turns so silence/noise after one
        utterance doesn't bias the next utterance's classification."""
        self._model.reset_states()


# Set while `speak()` has an active sd.play() stream; the VAD thread checks
# this to decide whether detected speech should trigger a barge-in.
playback_active = threading.Event()
# Set by the VAD thread right before it stops playback, so `speak()` can
# tell an interrupt apart from normal end-of-audio completion.
interrupted_event = threading.Event()


def record_turn(verbose: bool = False) -> np.ndarray:
    """Push-to-talk fallback (VAD_ENABLED=0): block on Enter to start,
    record, block on Enter to stop.

    Recording auto-stops after RECORD_MAX_SECONDS even without a second
    Enter press, so a forgotten or unattended session can't grow memory
    unbounded.

    Returns the recorded audio as int16 PCM samples (mono, RECORD_SAMPLE_RATE).
    """
    input("Press Enter to start recording, then Enter again to stop...")
    frames = []
    total_samples = 0
    max_samples = RECORD_MAX_SECONDS * RECORD_SAMPLE_RATE
    stopped_early = threading.Event()

    def callback(indata, frame_count, time_info, status):
        nonlocal total_samples
        if status and verbose:
            print(f"[mic] {status}", file=sys.stderr)
        frames.append(indata.copy())
        total_samples += frame_count
        if total_samples >= max_samples:
            # Stop capturing further audio (bounds memory), but the mic
            # stream callback can't cancel the blocking input() call below
            # — the user still needs to press Enter to advance the turn.
            # That's fine: an unattended/forgotten recording now stops
            # growing memory instead of running away, even though the turn
            # itself only proceeds once a human presses Enter regardless.
            stopped_early.set()
            raise sd.CallbackStop()

    stream = sd.InputStream(
        samplerate=RECORD_SAMPLE_RATE, channels=RECORD_CHANNELS,
        dtype="int16", callback=callback,
    )
    stream.start()
    print(f"Recording... press Enter to stop (auto-stops capturing after {RECORD_MAX_SECONDS}s).")
    input()
    if stream.active:
        stream.stop()
    stream.close()
    if stopped_early.is_set():
        print(f"[record] hit RECORD_MAX_SECONDS={RECORD_MAX_SECONDS}, stopped capturing early", file=sys.stderr)

    if not frames:
        return np.zeros(0, dtype=np.int16)
    audio = np.concatenate(frames, axis=0).flatten()
    if verbose:
        duration_s = len(audio) / RECORD_SAMPLE_RATE
        print(f"[record] {len(audio)} samples, {duration_s:.1f}s", file=sys.stderr)
    return audio


def vad_listen_loop(
    vad: "SileroVAD",
    frame_q: "queue.Queue[np.ndarray]",
    turn_q: "queue.Queue[np.ndarray]",
    verbose: bool = False,
) -> None:
    """Runs forever in a background thread (daemon). Pulls fixed-size
    frames pushed by the mic's InputStream callback, classifies each with
    Silero VAD, and accumulates a turn buffer across a speech segment.
    Once VAD_SILENCE_MS of trailing silence follows speech, the completed
    turn (if long enough — see VAD_MIN_SPEECH_MS) is pushed onto `turn_q`
    for the main thread to transcribe and dispatch.

    Also implements playback barge-in: if speech is detected while
    `playback_active` is set, this immediately calls `sounddevice.stop()`
    (safe to call from a non-main thread — that's the documented way to
    end an in-progress `sd.play()` stream, no raw thread kill involved)
    and sets `interrupted_event` so `speak()` can tell the difference from
    normal completion. The frame that triggered the interrupt is NOT
    dropped: it's already the first frame appended to the new turn's
    buffer below, so nothing the user says while interrupting is lost.
    """
    frame_duration_ms = VAD_FRAME_SAMPLES / RECORD_SAMPLE_RATE * 1000
    silence_frame_limit = max(1, round(VAD_SILENCE_MS / frame_duration_ms))
    min_speech_frames = max(1, round(VAD_MIN_SPEECH_MS / frame_duration_ms))

    in_speech = False
    silence_frames = 0
    speech_frame_count = 0
    buffer = []

    while True:
        frame = frame_q.get()
        mono = frame.reshape(-1)
        prob = vad.speech_prob(mono)
        is_speech = prob >= VAD_THRESHOLD
        if verbose and is_speech:
            print(f"[vad] speech prob={prob:.2f}", file=sys.stderr)

        if is_speech:
            if not in_speech and playback_active.is_set():
                interrupted_event.set()
                sd.stop()
                if verbose:
                    print("[vad] barge-in: stopping playback", file=sys.stderr)
            in_speech = True
            silence_frames = 0
            speech_frame_count += 1
            buffer.append(mono)
        elif in_speech:
            silence_frames += 1
            buffer.append(mono)  # keep trailing silence; whisper handles it fine
            if silence_frames >= silence_frame_limit:
                if speech_frame_count >= min_speech_frames:
                    turn_q.put(np.concatenate(buffer, axis=0))
                elif verbose:
                    print("[vad] discarded short blip", file=sys.stderr)
                buffer = []
                in_speech = False
                silence_frames = 0
                speech_frame_count = 0
                vad.reset()
        # else: silence outside of a speech segment — nothing to do


def transcribe(audio: np.ndarray, verbose: bool = False) -> str:
    """Transcribe recorded audio with faster-whisper. Deferred import."""
    from faster_whisper import WhisperModel  # deferred: optional dependency

    if audio.size == 0:
        return ""

    with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as f:
        wav_path = f.name
    try:
        with wave.open(wav_path, "wb") as wf:
            wf.setnchannels(RECORD_CHANNELS)
            wf.setsampwidth(2)  # int16
            wf.setframerate(RECORD_SAMPLE_RATE)
            wf.writeframes(audio.tobytes())

        model = WhisperModel(WHISPER_MODEL_SIZE, device=WHISPER_DEVICE, compute_type=WHISPER_COMPUTE_TYPE)
        segments, info = model.transcribe(
            wav_path,
            language=WHISPER_LANGUAGE or None,
        )
        text = " ".join(seg.text.strip() for seg in segments).strip()
        if verbose:
            print(f"[whisper] detected_language={info.language} text={text!r}", file=sys.stderr)
        return text
    finally:
        try:
            os.unlink(wav_path)
        except OSError:
            pass


def speak(text: str, verbose: bool = False) -> None:
    """Synthesize `text` with Piper (subprocess) and play it back.

    Playback can be interrupted mid-way by the VAD thread (barge-in): this
    polls the stream's `.active` flag instead of a blocking `sd.wait()` so
    it notices an interrupt promptly and returns instead of waiting for the
    full clip to finish.
    """
    if not text.strip():
        return
    if not PIPER_MODEL_PATH:
        print(
            "[piper] PIPER_MODEL_PATH is not set — cannot speak the result. "
            f"Result was: {text}",
            file=sys.stderr,
        )
        return

    with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as f:
        out_wav = f.name
    try:
        cmd = [PIPER_BINARY, "--model", PIPER_MODEL_PATH, "--output_file", out_wav]
        if verbose:
            print(f"[piper] {' '.join(cmd)}", file=sys.stderr)
        proc = subprocess.run(
            cmd, input=text.encode("utf-8"),
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        if proc.returncode != 0:
            print(
                f"[piper] synthesis failed (exit {proc.returncode}): "
                f"{proc.stderr.decode('utf-8', errors='replace')}",
                file=sys.stderr,
            )
            return

        with wave.open(out_wav, "rb") as wf:
            sample_rate = wf.getframerate()
            channels = wf.getnchannels()
            raw = wf.readframes(wf.getnframes())
        pcm = np.frombuffer(raw, dtype=np.int16)
        if channels > 1:
            pcm = pcm.reshape(-1, channels)

        interrupted_event.clear()
        sd.play(pcm, samplerate=sample_rate)
        if VAD_ENABLED:
            playback_active.set()
            try:
                while True:
                    stream = sd.get_stream()
                    if stream is None or not stream.active:
                        break
                    time.sleep(0.02)
            finally:
                playback_active.clear()
            if interrupted_event.is_set() and verbose:
                print("[piper] playback interrupted by user speech", file=sys.stderr)
        else:
            sd.wait()
    finally:
        try:
            os.unlink(out_wav)
        except OSError:
            pass


def list_piper_voices() -> None:
    """Best-effort listing: Piper itself has no built-in `--list-voices`
    flag (unlike gemini_tts_test.py's Gemini voice listing) — voices are
    just .onnx model files you download yourself. This lists any .onnx
    files found in PIPER_MODEL_PATH's directory (if set) as a convenience,
    and otherwise points at the upstream voice catalog."""
    if PIPER_MODEL_PATH:
        voice_dir = os.path.dirname(os.path.abspath(PIPER_MODEL_PATH)) or "."
        found = sorted(f for f in os.listdir(voice_dir) if f.endswith(".onnx"))
        if found:
            print(f"Piper voice models found in {voice_dir}:")
            for name in found:
                print(f"  {name}")
            return
    print(
        "No PIPER_MODEL_PATH set (or no .onnx files found next to it).\n"
        "Piper voices are downloaded individually, not listed by the binary "
        "itself — see https://github.com/rhasspy/piper/blob/master/VOICES.md"
    )


def run_vad_loop(dispatcher: "RakitsuDispatcher", verbose: bool = False) -> None:
    """Auto turn-detection main loop (default, VAD_ENABLED != "0"): mic
    monitoring runs continuously in a background thread; this loop just
    consumes completed turns and drives transcribe -> dispatch -> speak."""
    vad = SileroVAD()
    frame_q: "queue.Queue[np.ndarray]" = queue.Queue()
    turn_q: "queue.Queue[np.ndarray]" = queue.Queue()

    def mic_callback(indata, frame_count, time_info, status):
        if status and verbose:
            print(f"[mic] {status}", file=sys.stderr)
        frame_q.put(indata.copy())

    mic_stream = sd.InputStream(
        samplerate=RECORD_SAMPLE_RATE, channels=RECORD_CHANNELS,
        dtype="int16", blocksize=VAD_FRAME_SAMPLES, callback=mic_callback,
    )
    mic_stream.start()

    listener = threading.Thread(
        target=vad_listen_loop, args=(vad, frame_q, turn_q, verbose), daemon=True,
    )
    listener.start()

    print(
        f"Listening (auto VAD, {VAD_SILENCE_MS}ms trailing silence ends a turn; "
        "speak any time, including while it's replying, to interrupt). Ctrl+C to quit."
    )
    try:
        while True:
            audio = turn_q.get()
            transcript = transcribe(audio, verbose=verbose)
            if not transcript:
                if verbose:
                    print("[vad] (nothing transcribed, listening again)", file=sys.stderr)
                continue
            print(f"[user] {transcript}")

            if not _should_monitor_transcript(transcript):
                print("(trivial chit-chat, skipping dispatch)")
                continue

            print("[dispatch] sending to worker...")
            result = dispatcher.dispatch_sync(transcript)
            print(f"[worker] {result}")
            speak(result, verbose=verbose)
    except KeyboardInterrupt:
        print("\nBye.")
    finally:
        mic_stream.stop()
        mic_stream.close()


def run_push_to_talk_loop(dispatcher: "RakitsuDispatcher", verbose: bool = False) -> None:
    """Push-to-talk fallback (VAD_ENABLED=0), unchanged from the original mode: one
    manual Enter-to-start/Enter-to-stop turn at a time, no VAD, no
    barge-in interrupt."""
    print("Ready (push-to-talk, VAD_ENABLED=0). Ctrl+C to quit.")
    try:
        while True:
            audio = record_turn(verbose=verbose)
            if audio.size == 0:
                print("(no audio captured, try again)")
                continue

            transcript = transcribe(audio, verbose=verbose)
            if not transcript:
                print("(nothing transcribed, try again)")
                continue
            print(f"[user] {transcript}")

            if not _should_monitor_transcript(transcript):
                print("(trivial chit-chat, skipping dispatch)")
                continue

            print("[dispatch] sending to worker...")
            result = dispatcher.dispatch_sync(transcript)
            print(f"[worker] {result}")
            speak(result, verbose=verbose)
    except KeyboardInterrupt:
        print("\nBye.")


def main():
    parser = argparse.ArgumentParser(
        description="Local, turn-based voice bridge: faster-whisper STT -> rakitsu A2A "
                    "dispatch -> Piper TTS, with automatic Silero-VAD turn detection and "
                    "playback barge-in by default (VAD_ENABLED=0 for the old push-to-talk "
                    "mode). No realtime/cloud voice API.",
    )
    parser.add_argument(
        "--list-voices", action="store_true",
        help="List Piper voice models found next to PIPER_MODEL_PATH, then exit.",
    )
    parser.add_argument(
        "--verbose", action="store_true", default=os.environ.get("VERBOSE", "") == "1",
        help="Print extra diagnostic output (also enabled by VERBOSE=1).",
    )
    args = parser.parse_args()

    if args.list_voices:
        list_piper_voices()
        return

    print(f"Dispatching to rakitsu worker {RAKITSU_WORKER_CONFIG_NAME!r} via {RAKITSU_SERVE_URL}")
    print(f"A2A tenant: {RAKITSU_WORKER_AGENT_NAME}")
    print(f"Whisper model: {WHISPER_MODEL_SIZE} (device={WHISPER_DEVICE}, compute_type={WHISPER_COMPUTE_TYPE})")
    if VAD_ENABLED:
        print(f"VAD: Silero (threshold={VAD_THRESHOLD}, silence_ms={VAD_SILENCE_MS}, min_speech_ms={VAD_MIN_SPEECH_MS})")
    else:
        print("VAD: disabled (VAD_ENABLED=0) — push-to-talk mode")
    if not PIPER_MODEL_PATH:
        print(
            "PIPER_MODEL_PATH is not set — dispatch results will be printed "
            "to stdout but not spoken aloud.",
            file=sys.stderr,
        )

    dispatcher = RakitsuDispatcher()
    if VAD_ENABLED:
        run_vad_loop(dispatcher, verbose=args.verbose)
    else:
        run_push_to_talk_loop(dispatcher, verbose=args.verbose)


if __name__ == "__main__":
    main()
