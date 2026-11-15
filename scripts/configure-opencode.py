#!/usr/bin/env python3
"""Merge Gossip into an OpenCode global config without dropping existing MCPs."""

from __future__ import annotations

import json
import os
import shutil
import sys
import tempfile
import time
from pathlib import Path


def strip_jsonc(source: str) -> str:
    """Remove JSONC comments and trailing commas while respecting strings."""
    out: list[str] = []
    i = 0
    in_string = False
    escaped = False
    while i < len(source):
        ch = source[i]
        nxt = source[i + 1] if i + 1 < len(source) else ""
        if in_string:
            out.append(ch)
            if escaped:
                escaped = False
            elif ch == "\\":
                escaped = True
            elif ch == '"':
                in_string = False
            i += 1
            continue
        if ch == '"':
            in_string = True
            out.append(ch)
            i += 1
            continue
        if ch == "/" and nxt == "/":
            while i < len(source) and source[i] not in "\r\n":
                i += 1
            continue
        if ch == "/" and nxt == "*":
            i += 2
            while i + 1 < len(source) and source[i:i + 2] != "*/":
                if source[i] in "\r\n":
                    out.append(source[i])
                i += 1
            i += 2
            continue
        out.append(ch)
        i += 1
    uncommented = "".join(out)
    out.clear()
    i = 0
    in_string = False
    escaped = False
    while i < len(uncommented):
        ch = uncommented[i]
        if in_string:
            out.append(ch)
            if escaped:
                escaped = False
            elif ch == "\\":
                escaped = True
            elif ch == '"':
                in_string = False
            i += 1
            continue
        if ch == '"':
            in_string = True
            out.append(ch)
        elif ch == ",":
            lookahead = i + 1
            while lookahead < len(uncommented) and uncommented[lookahead].isspace():
                lookahead += 1
            if lookahead >= len(uncommented) or uncommented[lookahead] not in "}]":
                out.append(ch)
        else:
            out.append(ch)
        i += 1
    return "".join(out)


def main() -> int:
    if len(sys.argv) != 4:
        raise SystemExit("usage: configure-opencode.py CONFIG_PATH GOSSIP_BINARY PROFILE")
    path, binary = map(Path, sys.argv[1:3])
    profile = sys.argv[3]
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    data: dict = {"$schema": "https://opencode.ai/config.json"}
    if path.exists():
        try:
            data = json.loads(strip_jsonc(path.read_text(encoding="utf-8")))
        except (OSError, json.JSONDecodeError) as exc:
            raise SystemExit(f"Cannot safely read OpenCode config ({exc}); file left unchanged.")
    if not isinstance(data, dict):
        raise SystemExit("OpenCode config must contain a JSON object; file left unchanged.")
    mcp = data.get("mcp", {})
    if not isinstance(mcp, dict):
        raise SystemExit("OpenCode mcp config must be an object; file left unchanged.")
    current = mcp.get("servers", {})
    if not isinstance(current, dict):
        raise SystemExit("OpenCode mcp.servers must be an object; file left unchanged.")
    legacy = {key: value for key, value in mcp.items() if key != "servers"}
    servers = dict(current)
    previous_gossip = "gossip" in servers or "gossip" in legacy
    if previous_gossip:
        backup = path.with_name(f"{path.name}.backup-{time.strftime('%Y%m%d-%H%M%S')}")
        if path.exists():
            shutil.copy2(path, backup)
            os.chmod(backup, 0o600)
            print(f"Backed up the previous OpenCode config to {backup}")
    servers["gossip"] = {
        "type": "local",
        "command": [str(binary), "coord-mcp", "--profile", profile],
    }
    data["$schema"] = data.get("$schema", "https://opencode.ai/config.json")
    merged_mcp = dict(legacy)
    merged_mcp["servers"] = servers
    data["mcp"] = merged_mcp
    mode = (path.stat().st_mode & 0o777) if path.exists() else 0o600
    fd, temporary = tempfile.mkstemp(prefix=".opencode.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            json.dump(data, stream, indent=2)
            stream.write("\n")
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    except BaseException:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise
    print(f"Configured OpenCode MCP at {path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
