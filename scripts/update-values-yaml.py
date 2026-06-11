#!/usr/bin/env python3
import argparse
import json
import re
from pathlib import Path


KEY_RE = re.compile(r"^(\s*)([A-Za-z0-9_]+):(?:\s.*)?$")


def yaml_string(value: str) -> str:
    return json.dumps(value)


def main() -> None:
    parser = argparse.ArgumentParser(description="Update existing scalar keys in k8s/values.yaml")
    parser.add_argument("--file", required=True)
    parser.add_argument("--set", action="append", default=[], dest="assignments")
    args = parser.parse_args()

    replacements: dict[tuple[str, ...], str] = {}
    for assignment in args.assignments:
        if "=" not in assignment:
            raise SystemExit(f"--set expects path=value, got {assignment!r}")
        path, value = assignment.split("=", 1)
        parts = tuple(part for part in path.split(".") if part)
        if not parts:
            raise SystemExit(f"empty path in assignment {assignment!r}")
        replacements[parts] = value

    file_path = Path(args.file)
    lines = file_path.read_text(encoding="utf-8").splitlines()
    stack: list[tuple[int, str]] = []
    seen: set[tuple[str, ...]] = set()
    out: list[str] = []

    for line in lines:
        match = KEY_RE.match(line)
        if not match:
            out.append(line)
            continue

        indent = len(match.group(1))
        key = match.group(2)
        while stack and stack[-1][0] >= indent:
            stack.pop()
        path = tuple(item for _, item in stack) + (key,)

        if path in replacements:
            out.append(f"{match.group(1)}{key}: {yaml_string(replacements[path])}")
            seen.add(path)
        else:
            out.append(line)
        stack.append((indent, key))

    missing = sorted(".".join(path) for path in replacements if path not in seen)
    if missing:
        raise SystemExit("missing values.yaml paths: " + ", ".join(missing))

    file_path.write_text("\n".join(out) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
