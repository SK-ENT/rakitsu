#!/usr/bin/env python3
"""Standalone probe for Gemini's dedicated TTS models (see ../README.md).

This is NOT part of the voice-orchestrator's realtime bridge — it does not
touch voice_bridge_gemini.py, the Live API, or rakitsu at all. It is a
one-shot script to explore Gemini's text-to-speech models: single-speaker
style control and multi-speaker dialogue, outside any conversational/
realtime context.

Uses the official `google-genai` SDK (the same one Google AI Studio's own
"get code" export generates), not hand-rolled REST calls — this is what
that SDK's Client abstracts either backend behind:
  --auth api-key (default)  Gemini Developer API, client = genai.Client(api_key=...).
      Requires GEMINI_API_KEY.
  --auth vertex              Vertex AI (GCP), client = genai.Client(vertexai=True,
      project=..., location=...), using Application Default Credentials —
      no API key, no manual gcloud token shell-out needed. Requires
      `gcloud auth application-default login` to have been run once, and
      GCP_PROJECT_ID (or --project, or an active `gcloud config` project).

Streams the response (generate_content_stream) and assembles the raw audio
bytes, mirroring Google's own generated sample rather than a single
non-streaming call — matches how the model is actually meant to be driven.

Model/voice availability on Vertex specifically is unverified for the 3.8
generation as of this writing — Google Cloud's Cloud Text-to-Speech
"Gemini-TTS" docs list only up to gemini-3.1-flash-tts-preview as
confirmed-available there. Try the default 3.8 model on Vertex and fall
back to --model gemini-3.1-flash-tts-preview if it's rejected.

Usage:
    # Gemini Developer API (API key), single speaker
    GEMINI_API_KEY=$(secret GEMINI_API_KEY) python gemini_tts_test.py "Have a wonderful day!" --voice Kore --style "cheerful and friendly"

    # Vertex AI (GCP), single speaker
    gcloud auth application-default login   # once
    python gemini_tts_test.py "Have a wonderful day!" --auth vertex --project sakaki-ai --voice Kore

    # Multi-speaker dialogue: one --line SPEAKER:TEXT[:STYLE] per turn,
    # one --speaker NAME=VOICE per participant
    python gemini_tts_test.py \\
        --speaker Joe=Puck --speaker Jane=Kore \\
        --line "Joe:How's it going today Jane?:cheerful and friendly" \\
        --line "Jane:Not too bad, how about you?:calm and relaxed"

    # Use the higher-fidelity (non-lite) model instead
    python gemini_tts_test.py "Testing quality" --model gemini-3.8-flash-tts

Env vars:
    GEMINI_API_KEY   required for --auth api-key (default)
    GCP_PROJECT_ID   required for --auth vertex (or pass --project, or have
                     an active `gcloud config` default project)
    GCP_LOCATION     optional for --auth vertex, default "us-central1"

Output: writes an audio file (extension inferred from the returned mime
type, e.g. .wav) to --out (default out.wav) and prints how many bytes
came back.

Requires: google-genai
    pip install google-genai
"""

import argparse
import mimetypes
import os
import struct
import subprocess
import sys

GEMINI_API_KEY = os.environ.get("GEMINI_API_KEY", "")
GCP_PROJECT_ID = os.environ.get("GCP_PROJECT_ID", "")
GCP_LOCATION = os.environ.get("GCP_LOCATION", "us-central1")

# Documented prebuilt voices as of 2026-09, plus "Fola" seen in a live
# AI-Studio-generated sample — Google's Extended Voice Library may have
# grown beyond either list; check GET /v1beta/voices (Gemini API) for the
# current set.
PREBUILT_VOICES = [
    "Zephyr", "Puck", "Charon", "Kore", "Fenrir", "Leda", "Orus", "Aoede",
    "Callirrhoe", "Autonoe", "Enceladus", "Iapetus", "Umbriel", "Algieba",
    "Despina", "Erinome", "Algenib", "Rasalgethi", "Laomedeia", "Achernar",
    "Alnilam", "Schedar", "Gacrux", "Pulcherrima", "Achird", "Zubenelgenubi",
    "Vindemiatrix", "Sadachbia", "Sadaltager", "Sulafat", "Fola",
]


def _gcloud_default_project() -> str:
    # gcloud crashes with AttributeError: module 'google._upb._message' has
    # no attribute 'MessageMapContainer' when invoked from a shell where a
    # Python venv shadows PATH (e.g. this repo's own .venv) — it picks up
    # that venv's mismatched protobuf instead of its own vendored copy.
    # Forcing the macOS system Python sidesteps it; CLOUDSDK_PYTHON_SITEPACKAGES=0
    # does NOT fix it (confirmed by prior debugging in this project).
    env = dict(os.environ)
    env.setdefault("CLOUDSDK_PYTHON", "/usr/bin/python3")
    try:
        out = subprocess.check_output(
            ["gcloud", "config", "get-value", "project"],
            stderr=subprocess.DEVNULL, text=True, timeout=15, env=env,
        ).strip()
        return out if out and out != "(unset)" else ""
    except (FileNotFoundError, subprocess.CalledProcessError):
        return ""


def _convert_to_wav(audio_data: bytes, mime_type: str) -> bytes:
    """Header-less PCM (e.g. audio/L16;rate=24000) -> a playable .wav file."""
    bits_per_sample = 16
    rate = 24000
    for param in mime_type.split(";"):
        param = param.strip()
        if param.lower().startswith("rate="):
            try:
                rate = int(param.split("=", 1)[1])
            except (ValueError, IndexError):
                pass
        elif param.startswith("audio/L"):
            try:
                bits_per_sample = int(param.split("L", 1)[1])
            except (ValueError, IndexError):
                pass
    num_channels = 1
    data_size = len(audio_data)
    bytes_per_sample = bits_per_sample // 8
    block_align = num_channels * bytes_per_sample
    byte_rate = rate * block_align
    chunk_size = 36 + data_size
    header = struct.pack(
        "<4sI4s4sIHHIIHH4sI",
        b"RIFF", chunk_size, b"WAVE", b"fmt ", 16, 1, num_channels,
        rate, byte_rate, block_align, bits_per_sample, b"data", data_size,
    )
    return header + audio_data


def _parse_line(raw: str, types):
    """SPEAKER:TEXT[:STYLE] -> a types.Part with speech_metadata.speaker set.

    The API requires every part in a multi-speaker request to carry
    speech_metadata.speaker (confirmed live: 400 INVALID_ARGUMENT
    "Multi-speaker generation requests must specify speech_metadata.speaker
    for each text part in the contents" otherwise). types.SpeechMetadata
    exposes speaker/style fields for exactly this.
    """
    parts = raw.split(":", 2)
    if len(parts) < 2:
        raise ValueError(f"--line must be SPEAKER:TEXT[:STYLE], got: {raw!r}")
    speaker, text = parts[0], parts[1]
    style = parts[2] if len(parts) == 3 else None
    part = types.Part.from_text(text=text)
    part.speech_metadata = types.SpeechMetadata(speaker=speaker, style=style)
    return part


def build_contents(args, types):
    if args.line:
        if not args.speaker:
            raise ValueError("multi-speaker mode (--line) requires at least one --speaker NAME=VOICE")
        parts = [_parse_line(l, types) for l in args.line]
    else:
        if not args.text:
            raise ValueError("provide TEXT as a positional arg, or use --line for multi-speaker mode")
        text = f"({args.style}) {args.text}" if args.style else args.text
        parts = [types.Part.from_text(text=text)]
    return [types.Content(role="user", parts=parts)]


def build_speech_config(args, types):
    if args.line:
        speaker_configs = []
        for s in args.speaker:
            if "=" not in s:
                raise ValueError(f"--speaker must be NAME=VOICE, got: {s!r}")
            name, voice = s.split("=", 1)
            speaker_configs.append(types.SpeakerVoiceConfig(
                speaker=name,
                voice_config=types.VoiceConfig(prebuilt_voice_config=types.PrebuiltVoiceConfig(voice_name=voice)),
            ))
        return types.SpeechConfig(multi_speaker_voice_config=types.MultiSpeakerVoiceConfig(speaker_voice_configs=speaker_configs))
    return types.SpeechConfig(voice_config=types.VoiceConfig(prebuilt_voice_config=types.PrebuiltVoiceConfig(voice_name=args.voice)))


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("text", nargs="?", help="text to speak (single-speaker mode)")
    parser.add_argument("--voice", default="Kore", help="prebuilt voice name (single-speaker mode, default Kore)")
    parser.add_argument("--style", help="style direction, e.g. 'cheerful and friendly' (single-speaker mode)")
    parser.add_argument("--line", action="append", help="SPEAKER:TEXT[:STYLE] turn (repeatable, multi-speaker mode)")
    parser.add_argument("--speaker", action="append", help="NAME=VOICE participant (repeatable, multi-speaker mode)")
    parser.add_argument("--model", default="gemini-3.8-flash-lite-tts",
                         help="gemini-3.8-flash-lite-tts (default, fast/cheap) or gemini-3.8-flash-tts (higher fidelity); "
                              "for --auth vertex, gemini-3.1-flash-tts-preview is the confirmed-available fallback")
    parser.add_argument("--out", default="out.wav", help="output audio file path (default out.wav)")
    parser.add_argument("--list-voices", action="store_true", help="print known prebuilt voice names and exit")
    parser.add_argument("--auth", choices=["api-key", "vertex"], default="api-key",
                         help="api-key = Gemini Developer API with GEMINI_API_KEY (default); "
                              "vertex = Vertex AI (GCP) with Application Default Credentials")
    parser.add_argument("--project", default="", help="GCP project id (--auth vertex; default $GCP_PROJECT_ID, else `gcloud config get-value project`)")
    parser.add_argument("--location", default=GCP_LOCATION, help="GCP location (--auth vertex; default us-central1 or $GCP_LOCATION)")
    args = parser.parse_args()

    if args.list_voices:
        print("\n".join(PREBUILT_VOICES))
        return

    from google import genai  # deferred so --help/--list-voices work without the dependency installed
    from google.genai import types

    if args.auth == "vertex":
        project = args.project or GCP_PROJECT_ID or _gcloud_default_project()
        if not project:
            print("error: --auth vertex requires --project, GCP_PROJECT_ID, or a `gcloud config` default project", file=sys.stderr)
            sys.exit(1)
        client = genai.Client(vertexai=True, project=project, location=args.location)
    else:
        if not GEMINI_API_KEY:
            print("GEMINI_API_KEY is not set", file=sys.stderr)
            sys.exit(1)
        client = genai.Client(api_key=GEMINI_API_KEY)

    try:
        contents = build_contents(args, types)
        speech_config = build_speech_config(args, types)
    except ValueError as exc:
        print(f"error: {exc}", file=sys.stderr)
        sys.exit(1)

    generate_content_config = types.GenerateContentConfig(
        temperature=1,
        response_modalities=["audio"],
        speech_config=speech_config,
    )

    print(f"Requesting {args.model} via {args.auth} ({'multi-speaker' if args.line else 'single-speaker'})...")
    audio_data = bytearray()
    mime_type = ""
    for chunk in client.models.generate_content_stream(model=args.model, contents=contents, config=generate_content_config):
        if not chunk.parts:
            continue
        for part in chunk.parts:
            inline_data = part.inline_data
            if inline_data and inline_data.data:
                audio_data.extend(inline_data.data)
                mime_type = inline_data.mime_type
            elif part.text:
                print(part.text)

    if not audio_data:
        print("error: no audio content in response", file=sys.stderr)
        sys.exit(1)

    out_path = args.out
    ext = mimetypes.guess_extension(mime_type) if mime_type else None
    data_buffer = bytes(audio_data)
    if ext is None:
        data_buffer = _convert_to_wav(data_buffer, mime_type)
        if not out_path.endswith(".wav"):
            out_path = os.path.splitext(out_path)[0] + ".wav"

    with open(out_path, "wb") as f:
        f.write(data_buffer)
    print(f"wrote {len(data_buffer)} bytes to {out_path}")


if __name__ == "__main__":
    main()
