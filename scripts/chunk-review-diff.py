#!/usr/bin/env python3
"""Split a unified diff into bounded, file-aligned review chunks."""
import argparse
import re
import sys
from pathlib import Path


def split_diff(raw: bytes, limit: int) -> list[bytes]:
    if not raw:
        return []
    starts = [m.start() for m in re.finditer(rb"(?m)^diff --git ", raw)]
    if not starts or starts[0] != 0:
        raise ValueError("diff does not begin with a unified file header")
    files = [raw[a:b] for a, b in zip(starts, starts[1:] + [len(raw)])]
    chunks: list[bytes] = []
    current = bytearray()
    for file_diff in files:
        if len(file_diff) > limit:
            raise ValueError(f"single file diff exceeds {limit} bytes; cannot safely split it")
        if current and len(current) + len(file_diff) > limit:
            chunks.append(bytes(current))
            current.clear()
        current.extend(file_diff)
    if current:
        chunks.append(bytes(current))
    return chunks


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("diff_file")
    parser.add_argument("output_dir")
    parser.add_argument("--limit", type=int, default=90000)
    args = parser.parse_args()
    if args.limit <= 0:
        parser.error("--limit must be positive")
    try:
        chunks = split_diff(Path(args.diff_file).read_bytes(), args.limit)
        out = Path(args.output_dir)
        out.mkdir(parents=True, exist_ok=True)
        manifest = []
        for index, chunk in enumerate(chunks, 1):
            name = f"chunk-{index:04d}.diff"
            (out / name).write_bytes(chunk)
            manifest.append(f"{name}\t{len(chunk)} bytes")
        (out / "MANIFEST.txt").write_text("\n".join(manifest) + "\n", encoding="utf-8")
        print(f"{len(chunks)} chunks, {sum(map(len, chunks))} bytes")
    except (OSError, ValueError) as exc:
        print(f"FATAL: cannot stage complete review diff: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
