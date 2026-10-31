#!/usr/bin/env python3
"""Fail closed unless Rakitsu successfully read the review manifest and chunks."""
import json
import re
import sys
from pathlib import Path


def normalize_path(path: str, workdir: Path):
    root = workdir.resolve()
    candidate = Path(path)
    if not candidate.is_absolute():
        candidate = root / candidate
    try:
        return candidate.resolve(strict=True).relative_to(root).as_posix()
    except (OSError, ValueError):
        return None


def complete_reads(manifest_path: Path, sessions_dir: Path, workdir: Path) -> bool:
    try:
        names = []
        for line in manifest_path.read_text(encoding="utf-8").splitlines():
            if not line:
                continue
            name, separator, _size = line.partition("\t")
            if not separator or not re.fullmatch(r"chunk-\d{4}\.diff", name):
                return False
            if name in names or not (manifest_path.parent / name).is_file():
                return False
            names.append(name)

        session_files = list(sessions_dir.glob("*.jsonl"))
        if len(session_files) != 1:
            return False

        started = {}
        successful_reads = set()
        final_event_type = ""
        final_status = ""
        with session_files[0].open(encoding="utf-8") as stream:
            for line in stream:
                event = json.loads(line)
                if not isinstance(event, dict):
                    return False
                event_type = event.get("event_type", "")
                final_event_type = event_type
                payload = event.get("payload", {})
                if not isinstance(payload, dict):
                    return False

                if event_type == "SESSION_END":
                    final_status = payload.get("status", "")
                    continue

                if event.get("agent_name") != "Reviewer":
                    continue
                call_id = payload.get("tool_call_id")
                tool_name = payload.get("tool_name")
                if event_type == "TOOL_CALL_START" and tool_name == "read_file":
                    arguments = payload.get("arguments", {})
                    path = arguments.get("path") if isinstance(arguments, dict) else None
                    if not isinstance(call_id, str) or not call_id or not isinstance(path, str):
                        return False
                    if call_id in started:
                        return False
                    started[call_id] = path
                elif event_type == "TOOL_CALL_END" and call_id in started:
                    if tool_name != "read_file":
                        return False
                    if payload.get("error", ""):
                        continue
                    if payload.get("output_truncated") is not False:
                        continue
                    if not isinstance(payload.get("output"), str) or not payload["output"]:
                        continue
                    normalized = normalize_path(started[call_id], workdir)
                    if normalized is not None:
                        successful_reads.add(normalized)

        required = {"review-diff/MANIFEST.txt"}
        required.update(f"review-diff/{name}" for name in names)
        return final_event_type == "SESSION_END" and final_status == "success" and required <= successful_reads
    except (OSError, UnicodeError, json.JSONDecodeError, TypeError, ValueError):
        return False


def main() -> int:
    if len(sys.argv) != 4:
        return 2
    return 0 if complete_reads(Path(sys.argv[1]), Path(sys.argv[2]), Path(sys.argv[3])) else 1


if __name__ == "__main__":
    raise SystemExit(main())
