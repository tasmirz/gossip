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
  local pid response_pid
  for _ in {1..15}; do
    case "$(uname -s)" in
      Linux) pid="$(systemctl --user show -p MainPID --value gossip-dashboard.service)" ;;
      Darwin) pid="$(launchctl print "$domain/$label" 2>/dev/null | awk '$1 == "pid" && $2 == "=" { print $3; exit }')" ;;
    esac
    response_pid="$(curl --noproxy '*' -fsS --max-time 1 -D - -o /dev/null http://127.0.0.1:8081/healthz 2>/dev/null | awk 'tolower($1) == "gossip-service-pid:" { gsub(/\r/, "", $2); print $2 }' || true)"
    if [[ "$pid" =~ ^[1-9][0-9]*$ && "$pid" == "$response_pid" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "Gossip service did not become healthy on port 8081. Check the service logs and whether the port is occupied." >&2
  return 1
}

install -m 0755 "$tmp/gossip" "$root/bin/gossip"
install -m 0644 "$tmp/skills/gossip/SKILL.md" "$root/skills/gossip/SKILL.md"
bin="$root/bin/gossip"

echo ""
echo "Installed binary: $bin"
echo "Choose an agent to configure:"
echo "  1) Codex"
echo "  2) Antigravity"
echo "  3) OpenCode"
echo "  4) Claude Code"
echo "  5) Gemini CLI"
echo "  6) Install the skill only"
read -r -p "Selection [1-6]: " choice
case "$choice" in
  1) client=codex; skill="$HOME/.agents/skills/gossip" ;;
  2) client=antigravity; skill="$HOME/.gemini/config/skills/gossip" ;;
  3) client=opencode; skill="${XDG_CONFIG_HOME:-$HOME/.config}/opencode/skills/gossip" ;;
  4) client=claude; skill="$HOME/.claude/skills/gossip" ;;
  5) client=gemini; skill="$HOME/.gemini/skills/gossip" ;;
  6) client=none; skill="$HOME/.agents/skills/gossip" ;;
  *) echo "Choose 1, 2, 3, 4, 5, or 6." >&2; exit 2 ;;
esac
mkdir -p "$skill"
install -m 0644 "$root/skills/gossip/SKILL.md" "$skill/SKILL.md"
echo "Installed Gossip skill at $skill/SKILL.md"

if [ "$client" != none ]; then
  read -r -p "Local Gossip identity profile [default]: " profile
  profile="${profile:-default}"
    case "$client" in
      codex)
        command -v codex >/dev/null || { echo "Install Codex CLI, then rerun this setup." >&2; exit 1; }
        codex_home="${CODEX_HOME:-$HOME/.codex}"
        mkdir -p "$codex_home"
        codex_config="$codex_home/config.toml"
        if [ -f "$codex_config" ] && grep -Eq '^\[mcp_servers\.gossip\]' "$codex_config"; then
          backup="$codex_config.backup-$(date +%Y%m%d-%H%M%S)"
          cp -p "$codex_config" "$backup"
          echo "Backed up the previous Codex MCP config to $backup"
        fi
        codex mcp add gossip -- "$bin" coord-mcp --profile "$profile"
        ;;
      antigravity)
        config="${GEMINI_CONFIG_DIR:-$HOME/.gemini/config}/mcp_config.json"
        python3 - "$config" "$bin" "$profile" <<'PY'
import json, os, shutil, sys, tempfile, time
path, command, profile = sys.argv[1:]
os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
data = {}
if os.path.exists(path):
    with open(path, encoding="utf-8") as source:
        data = json.load(source)
if not isinstance(data, dict):
    raise SystemExit("Antigravity MCP config must contain a JSON object; left unchanged.")
servers = data.setdefault("mcpServers", {})
if not isinstance(servers, dict):
    raise SystemExit("Antigravity mcpServers must be an object; left unchanged.")
if "gossip" in servers:
    backup = f"{path}.backup-{time.strftime('%Y%m%d-%H%M%S')}"
    shutil.copy2(path, backup)
    os.chmod(backup, 0o600)
    print(f"Backed up the previous Gossip entry to {backup}")
servers["gossip"] = {"command": command, "args": ["coord-mcp", "--profile", profile]}
mode = (os.stat(path).st_mode & 0o777) if os.path.exists(path) else 0o600
fd, temporary = tempfile.mkstemp(prefix=".mcp_config.", dir=os.path.dirname(path))
try:
    with os.fdopen(fd, "w", encoding="utf-8") as target:
        json.dump(data, target, indent=2)
        target.write("\n")
    os.chmod(temporary, mode)
    os.replace(temporary, path)
except BaseException:
    try: os.unlink(temporary)
    except FileNotFoundError: pass
    raise
print(f"Configured Antigravity MCP at {path}")
PY
        ;;
      opencode)
        command -v opencode >/dev/null || { echo "Install OpenCode, then rerun this setup." >&2; exit 1; }
        opencode_config="${XDG_CONFIG_HOME:-$HOME/.config}/opencode/opencode.json"
        opencode_helper="$tmp/configure-opencode.py"
        if [ -n "${GOSSIP_OPENCODE_MERGE_HELPER:-}" ] && [ -f "$GOSSIP_OPENCODE_MERGE_HELPER" ]; then
          cp "$GOSSIP_OPENCODE_MERGE_HELPER" "$opencode_helper"
        else
          curl -fsSL "https://raw.githubusercontent.com/${OWNER_REPO}/main/scripts/configure-opencode.py" -o "$opencode_helper"
        fi
        python3 "$opencode_helper" "$opencode_config" "$bin" "$profile"
        ;;
      claude) command -v claude >/dev/null || { echo "Install Claude Code, then rerun this setup." >&2; exit 1; }; claude mcp add --scope user gossip -- "$bin" coord-mcp --profile "$profile" ;;
      gemini) command -v gemini >/dev/null || { echo "Install Gemini CLI, then rerun this setup." >&2; exit 1; }; gemini mcp add --scope user gossip "$bin" coord-mcp --profile "$profile" ;;
    esac
    echo "Configured Gossip MCP for $client. Restart its app and verify the connection."
fi

echo ""
if [ "$service_capability" = gossip-inbox-v1 ] && start_dashboard_service; then
  echo "Gossip starts at sign-in. Open http://127.0.0.1:8081 to add a project."
else
  echo "Automatic startup is unavailable. Start Gossip with: '$bin' coord-ui"
fi
echo "Manual agent steps: https://github.com/${OWNER_REPO}/blob/main/docs/installation/README.md"
