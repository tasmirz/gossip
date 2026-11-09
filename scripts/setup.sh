#!/usr/bin/env bash
set -euo pipefail

OWNER_REPO="tasmirz/gossip"
API="https://api.github.com/repos/${OWNER_REPO}/releases?per_page=10"
BASE="https://github.com/${OWNER_REPO}/releases/download"
command -v curl >/dev/null || { echo "Install curl, then retry." >&2; exit 1; }
command -v tar >/dev/null || { echo "Install tar, then retry." >&2; exit 1; }
command -v python3 >/dev/null || { echo "Python 3 is required to select a matching release asset." >&2; exit 1; }
if [ ! -t 0 ]; then
  if [ -r /dev/tty ]; then
    exec </dev/tty
  else
    echo "Run this installer in an interactive terminal so you can choose an agent and credential." >&2
    exit 1
  fi
fi

case "$(uname -s):$(uname -m)" in
  Linux:x86_64|Linux:amd64) target=linux_amd64; archive=tar.gz ;;
  Linux:aarch64|Linux:arm64) target=linux_arm64; archive=tar.gz ;;
  Darwin:x86_64) target=darwin_amd64; archive=tar.gz ;;
  Darwin:arm64|Darwin:aarch64) target=darwin_arm64; archive=tar.gz ;;
  *) echo "Unsupported system: $(uname -s) $(uname -m)" >&2; exit 1 ;;
esac

release_json="$(curl -fsSL "$API")"
asset="$(python3 -c 'import json,sys; target=sys.argv[1]; releases=json.load(sys.stdin); print(next(("gossip_"+r["tag_name"]+"_"+target+".tar.gz" for r in releases if any(a["name"]=="gossip_"+r["tag_name"]+"_"+target+".tar.gz" for a in r.get("assets",[]))), ""))' "$target" <<<"$release_json")"
test -n "$asset" || { echo "No Gossip binary release is available for $target." >&2; exit 1; }
tag="${asset#gossip_}"
tag="${tag%_${target}.${archive}}"
root="${XDG_DATA_HOME:-$HOME/.local/share}/gossip"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
echo "Downloading Gossip $tag for $target..."
curl -fsSL "$BASE/$tag/$asset" -o "$tmp/$asset"
curl -fsSL "$BASE/$tag/$asset.sha256" -o "$tmp/$asset.sha256"
if command -v sha256sum >/dev/null; then
  (cd "$tmp" && sha256sum -c "$asset.sha256")
elif command -v shasum >/dev/null; then
  expected="$(awk '{print $1}' "$tmp/$asset.sha256")"
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
  test "$expected" = "$actual" || { echo "Release archive SHA-256 verification failed." >&2; exit 1; }
else
  echo "Install sha256sum or shasum, then retry." >&2; exit 1
fi
mkdir -p "$root/bin" "$root/skills/gossip"
tar -xzf "$tmp/$asset" -C "$tmp"
install -m 0755 "$tmp/gossip" "$root/bin/gossip"
install -m 0644 "$tmp/skills/gossip/SKILL.md" "$root/skills/gossip/SKILL.md"
bin="$root/bin/gossip"

echo ""
echo "Installed binary: $bin"
echo "Choose an agent to configure:"
echo "  1) Codex"
echo "  2) Claude Code"
echo "  3) Gemini CLI"
echo "  4) Install the skill only"
read -r -p "Selection [1-4]: " choice
case "$choice" in
  1) client=codex; skill="$HOME/.agents/skills/gossip" ;;
  2) client=claude; skill="$HOME/.claude/skills/gossip" ;;
  3) client=gemini; skill="$HOME/.gemini/skills/gossip" ;;
  4) client=none; skill="$HOME/.agents/skills/gossip" ;;
  *) echo "Choose 1, 2, 3, or 4." >&2; exit 2 ;;
esac
mkdir -p "$skill"
install -m 0644 "$root/skills/gossip/SKILL.md" "$skill/SKILL.md"
echo "Installed Gossip skill at $skill/SKILL.md"

if [ "$client" != none ]; then
  read -r -p "Path to this project's private credential JSON (leave blank to finish MCP setup later): " credential
  if [ -n "$credential" ]; then
    test -f "$credential" || { echo "Credential file not found: $credential" >&2; exit 1; }
    credential="$(python3 -c 'import os,sys; print(os.path.abspath(sys.argv[1]))' "$credential")"
    case "$client" in
      codex) command -v codex >/dev/null || { echo "Install Codex CLI, then rerun this setup." >&2; exit 1; }; codex mcp add gossip -- "$bin" coord-mcp --credential-file "$credential" ;;
      claude) command -v claude >/dev/null || { echo "Install Claude Code, then rerun this setup." >&2; exit 1; }; claude mcp add --scope user gossip -- "$bin" coord-mcp --credential-file "$credential" ;;
      gemini) command -v gemini >/dev/null || { echo "Install Gemini CLI, then rerun this setup." >&2; exit 1; }; gemini mcp add --scope user gossip "$bin" coord-mcp --credential-file "$credential" ;;
    esac
    echo "Configured Gossip MCP for $client. Restart its app and verify the connection."
  else
    echo "Create/connect your project first, then rerun this installer with its downloaded credential file."
  fi
fi

echo ""
echo "Next: run '$bin coord-ui', open http://127.0.0.1:8081, and choose Create local project."
echo "Manual agent steps: https://github.com/${OWNER_REPO}/blob/main/docs/installation/README.md"
