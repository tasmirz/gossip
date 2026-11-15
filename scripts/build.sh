#!/usr/bin/env bash
set -euo pipefail

# --restart [--no-frp] builds for this machine, updates the installed
# executable(s), and restarts the dashboard service.
if [[ "${1:-}" == --restart && ( $# == 1 || ( $# == 2 && "${2:-}" == --no-frp ) ) ]]; then
    with_frp=1
    if [[ "${2:-}" == --no-frp ]]; then
        with_frp=0
    fi
    cd "$(dirname "${BASH_SOURCE[0]}")/.."
    destination="${GOSSIP_DEV_BIN:-$HOME/.local/bin/gossip}"
    if [[ -L "$destination" ]]; then
        destination="$(python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$destination")"
    fi
    mkdir -p "$(dirname "$destination")"
    staged="$(mktemp "$destination.XXXXXX")"
    frp_destination="$(dirname "$destination")/gossip-frp"
    frp_staged="$(mktemp "$frp_destination.XXXXXX")"
    trap 'rm -f "$staged" "$frp_staged"' EXIT
    go build -o "$staged" ./cmd/gossip
    chmod 0755 "$staged"
    mv -f "$staged" "$destination"
    if [[ "$with_frp" == 1 && -d frp ]]; then
        (cd frp && GOTMPDIR="${GOTMPDIR:-/extra/.cache/gotmp}" go build -o "$frp_staged" .)
        chmod 0755 "$frp_staged"
        mv -f "$frp_staged" "$frp_destination"
    elif [[ "$with_frp" == 0 ]]; then
        rm -f "$frp_destination"
    fi
    platform="$(uname -s)"
    case "$platform" in
        Linux)
            if ! systemctl --user cat gossip-dashboard.service >/dev/null 2>&1; then
                config="${XDG_CONFIG_HOME:-$HOME/.config}"
                mkdir -p "$config/systemd/user"
                python3 - "$destination" "$config" <<'SERVICE'
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
                systemctl --user daemon-reload
                systemctl --user enable "$config/systemd/user/gossip-dashboard.service"
            fi
            systemctl --user restart gossip-dashboard.service
            ;;
        Darwin) launchctl kickstart -k "gui/$(id -u)/com.gossip.dashboard" ;;
        *) echo "Restart the Gossip dashboard manually." >&2; exit 1 ;;
    esac
    dashboard_port="${GOSSIP_DASHBOARD_PORT:-}"
    if [[ -z "$dashboard_port" ]]; then
        dashboard_port="$(python3 - <<'PORT' 2>/dev/null || echo 47281
import json, os, pathlib
for candidate in (
    pathlib.Path(os.environ.get("XDG_CONFIG_HOME") or pathlib.Path.home() / ".config") / "gossip" / "peer-access.json",
    pathlib.Path.home() / "Library" / "Application Support" / "gossip" / "peer-access.json",
):
    if candidate.is_file():
        try:
            p = int(json.loads(candidate.read_text()).get("dashboard_port") or 0)
            if 1 <= p <= 65535:
                print(p)
                raise SystemExit(0)
        except Exception:
            pass
print(47281)
PORT
)"
    fi
    for _ in {1..15}; do
        case "$platform" in
            Linux) pid="$(systemctl --user show -p MainPID --value gossip-dashboard.service)" ;;
            Darwin) pid="$(launchctl print "gui/$(id -u)/com.gossip.dashboard" 2>/dev/null | awk '$1 == "pid" && $2 == "=" { print $3; exit }' || true)" ;;
        esac
        response_pid="$(curl --noproxy '*' -fsS --max-time 1 -D - -o /dev/null "http://127.0.0.1:${dashboard_port}/healthz" 2>/dev/null | awk 'tolower($1) == "gossip-service-pid:" { gsub(/\r/, "", $2); print $2 }' || true)"
        if [[ "$pid" =~ ^[1-9][0-9]*$ && "$pid" == "$response_pid" ]]; then
            echo "Updated $destination and restarted Gossip on http://127.0.0.1:${dashboard_port}."
            exit 0
        fi
        sleep 1
    done
    echo "Updated $destination, but the dashboard did not become healthy on port ${dashboard_port}. Check the service logs." >&2
    exit 1
fi
if (( $# )); then
    echo "Usage: $0 [--restart [--no-frp]]" >&2
    exit 2
fi

# Gossip Cross-Platform Build Script
VERSION="1.0.0"
OUTPUT_DIR="bin"
mkdir -p "${OUTPUT_DIR}"

echo "Building Gossip v${VERSION}..."

TARGETS=(
    "linux/amd64/gossip-linux-amd64"
    "linux/arm64/gossip-linux-arm64"
    "darwin/amd64/gossip-darwin-amd64"
    "darwin/arm64/gossip-darwin-arm64"
    "windows/amd64/gossip-windows-amd64.exe"
)

for target in "${TARGETS[@]}"; do
    IFS="/" read -r GOOS GOARCH OUTNAME <<< "${target}"
    echo "  -> Compiling for ${GOOS}/${GOARCH}..."
    GOOS="${GOOS}" GOARCH="${GOARCH}" CGO_ENABLED=0 go build \
        -ldflags="-s -w -X main.Version=${VERSION}" \
        -o "${OUTPUT_DIR}/${OUTNAME}" ./cmd/gossip
done

# Create default local binary symlinks/copies
cp -f "${OUTPUT_DIR}/gossip-linux-amd64" "${OUTPUT_DIR}/gossip-mcp"
cp -f "${OUTPUT_DIR}/gossip-linux-amd64" "${OUTPUT_DIR}/gossip"
chmod +x "${OUTPUT_DIR}/gossip-mcp" "${OUTPUT_DIR}/gossip"

echo "Build complete. Artifacts in ${OUTPUT_DIR}/"
ls -lh "${OUTPUT_DIR}"
