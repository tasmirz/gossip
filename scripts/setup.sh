#!/usr/bin/env bash
set -euo pipefail

OWNER_REPO="tasmirz/gossip"
API="https://api.github.com/repos/${OWNER_REPO}/releases?per_page=10"
BASE="https://github.com/${OWNER_REPO}/releases/download"
BINARY_ONLY="${GOSSIP_BINARY_ONLY:-0}"
for arg in "$@"; do
  case "$arg" in
    --binary-only) BINARY_ONLY=1 ;;
    *) echo "Unknown option: $arg" >&2; exit 2 ;;
  esac
done

command -v curl >/dev/null || { echo "Install curl, then retry." >&2; exit 1; }
command -v tar >/dev/null || { echo "Install tar, then retry." >&2; exit 1; }
command -v python3 >/dev/null || { echo "Python 3 is required to select a matching release asset." >&2; exit 1; }
if [ "$BINARY_ONLY" != "1" ] && [ ! -t 0 ]; then
  if [ -r /dev/tty ]; then
    exec </dev/tty
  else
    echo "No interactive terminal detected; installing binary only. Run 'gossip install' afterwards to configure coding agents."
    BINARY_ONLY=1
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
service_capability="$("$tmp/gossip" coord-ui --service-capability 2>/dev/null || true)"
start_dashboard_service() {
  local config="${XDG_CONFIG_HOME:-$HOME/.config}"
  case "$(uname -s)" in
    Linux)
      command -v systemctl >/dev/null || { echo "systemctl is unavailable." >&2; return 1; }
      mkdir -p "$config/systemd/user" || return
      python3 - "$bin" "$config" <<'SERVICE' || return
import pathlib
import sys
bin_path, config = sys.argv[1:]
def quote(value):
    return '"' + value.replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%').replace('$', '$$') + '"'
path = pathlib.Path(config) / "systemd/user/gossip-dashboard.service"
path.write_text("[Unit]\nDescription=Gossip dashboard and inbox worker\n[Service]\nExecStart=" + quote(bin_path) +
                " coord-ui --listen 127.0.0.1\nEnvironment=" + quote("XDG_CONFIG_HOME=" + config) +
                "\nRestart=on-failure\nRestartSec=5\n[Install]\nWantedBy=default.target\n")
SERVICE
      systemctl --user daemon-reload &&
        systemctl --user enable "$config/systemd/user/gossip-dashboard.service" &&
        systemctl --user restart gossip-dashboard.service || return
      ;;
    Darwin)
      local domain="gui/$(id -u)" label="com.gossip.dashboard" plist="$HOME/Library/LaunchAgents/com.gossip.dashboard.plist"
      mkdir -p "$(dirname "$plist")" || return
      python3 - "$bin" "$plist" <<'SERVICE' || return
import os
import plistlib
import sys
binary, path = sys.argv[1:]
service = dict(Label="com.gossip.dashboard", RunAtLoad=True, KeepAlive={"SuccessfulExit": False},
               ProgramArguments=[binary, "coord-ui", "--listen", "127.0.0.1"])
if os.getenv("XDG_CONFIG_HOME"):
    service["EnvironmentVariables"] = {"XDG_CONFIG_HOME": os.environ["XDG_CONFIG_HOME"]}
with open(path, "wb") as output:
    plistlib.dump(service, output)
SERVICE
      if launchctl print "$domain/$label" >/dev/null 2>&1; then
        launchctl bootout "$domain/$label" || return
      fi
      launchctl bootstrap "$domain" "$plist" || return
      ;;
  esac
  dashboard_port="${GOSSIP_DASHBOARD_PORT:-47281}"
  local pid response_pid
  for _ in {1..15}; do
    case "$(uname -s)" in
      Linux) pid="$(systemctl --user show -p MainPID --value gossip-dashboard.service)" ;;
      Darwin) pid="$(launchctl print "$domain/$label" 2>/dev/null | awk '$1 == "pid" && $2 == "=" { print $3; exit }')" ;;
    esac
    response_pid="$(curl --noproxy '*' -fsS --max-time 1 -D - -o /dev/null "http://127.0.0.1:${dashboard_port}/healthz" 2>/dev/null | awk 'tolower($1) == "gossip-service-pid:" { gsub(/\r/, "", $2); print $2 }' || true)"
    if [[ "$pid" =~ ^[1-9][0-9]*$ && "$pid" == "$response_pid" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "Gossip service did not become healthy on port ${dashboard_port}. Check the service logs and whether the port is occupied." >&2
  return 1
}

install -m 0755 "$tmp/gossip" "$root/bin/gossip"
if [ -f "$tmp/gossip-frp" ]; then
  install -m 0755 "$tmp/gossip-frp" "$root/bin/gossip-frp"
fi
install -m 0644 "$tmp/skills/gossip/SKILL.md" "$root/skills/gossip/SKILL.md"
bin="$root/bin/gossip"
local_bin="$HOME/.local/bin"
if mkdir -p "$local_bin" 2>/dev/null; then
  ln -sf "$bin" "$local_bin/gossip"
  if [ -f "$root/bin/gossip-frp" ]; then
    ln -sf "$root/bin/gossip-frp" "$local_bin/gossip-frp"
  fi
fi

echo ""
echo "Installed binary: $bin"
if [ "$BINARY_ONLY" = "1" ]; then
  echo "Binary-only mode complete."
  echo "Next: auto-configure all detected coding agents with:"
  echo "  gossip install"
  echo "(or '$bin' install if $local_bin is not yet in PATH)"
else
  echo "Choose an agent to configure:"
  echo "  0) Auto-configure all detected coding agents (recommended)"
  echo "  1) Codex"
  echo "  2) Antigravity"
  echo "  3) OpenCode"
  echo "  4) Claude Code"
  echo "  5) Gemini CLI"
  echo "  6) Install the skill only"
  read -r -p "Selection [0-6, default 0]: " choice
  choice="${choice:-0}"
  if [ "$choice" = "0" ]; then
    read -r -p "Local Gossip identity profile [default]: " profile
    profile="${profile:-default}"
    "$bin" install --all --profile "$profile"
  else
    case "$choice" in
      1) client=codex; skill="$HOME/.agents/skills/gossip" ;;
      2) client=antigravity; skill="$HOME/.gemini/config/skills/gossip" ;;
      3) client=opencode; skill="${XDG_CONFIG_HOME:-$HOME/.config}/opencode/skills/gossip" ;;
      4) client=claude; skill="$HOME/.claude/skills/gossip" ;;
      5) client=gemini; skill="$HOME/.gemini/skills/gossip" ;;
      6) client=none; skill="$HOME/.agents/skills/gossip" ;;
      *) echo "Choose 0, 1, 2, 3, 4, 5, or 6." >&2; exit 2 ;;
    esac
    mkdir -p "$skill"
    install -m 0644 "$root/skills/gossip/SKILL.md" "$skill/SKILL.md"
    echo "Installed Gossip skill at $skill/SKILL.md"

    if [ "$client" != none ]; then
      read -r -p "Local Gossip identity profile [default]: " profile
      profile="${profile:-default}"
      "$bin" install --client "$client" --profile "$profile"
      echo "Configured Gossip MCP for $client. Restart its app and verify the connection."
    fi
  fi
fi

echo ""
dashboard_port="${GOSSIP_DASHBOARD_PORT:-47281}"
if [ "$service_capability" = gossip-inbox-v1 ] && start_dashboard_service; then
  echo "Gossip starts at sign-in. Open http://127.0.0.1:${dashboard_port} to add a project."
else
  echo "Automatic startup is unavailable. Start Gossip with: '$bin' coord-ui"
fi
echo "Manual agent steps: https://github.com/${OWNER_REPO}/blob/main/docs/installation/README.md"
