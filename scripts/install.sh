#!/usr/bin/env bash
# ==============================================================================
# GOSSIP UNIVERSAL AUTO-INSTALLER (Linux & macOS)
# Multi-Agent Collaboration Mesh & Shared RAG Space (AGPLv3)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Defaults
TARGET_ALL=true
TARGET_CLIENT=""
DRY_RUN=false
CUSTOM_BIN_DIR=""
WITH_FRP=false
FRP_SERVER=""
FRP_USER=""
FRP_PASS=""

# Parse flags
while [[ $# -gt 0 ]]; do
  case "$1" in
    --all)
      TARGET_ALL=true
      shift
      ;;
    --client)
      TARGET_CLIENT="${2:-}"
      TARGET_ALL=false
      shift 2
      ;;
    --bin-dir)
      CUSTOM_BIN_DIR="${2:-}"
      shift 2
      ;;
    --dry-run)
      DRY_RUN=true
      shift
      ;;
    --with-frp)
      WITH_FRP=true
      shift
      ;;
    --frp-server)
      FRP_SERVER="${2:-}"
      shift 2
      ;;
    --user)
      FRP_USER="${2:-}"
      shift 2
      ;;
    --pass)
      FRP_PASS="${2:-}"
      shift 2
      ;;
    -h|--help)
      echo "Gossip Universal Auto-Installer"
      echo "Usage: ./scripts/install.sh [options]"
      echo ""
      echo "Options:"
      echo "  --all                 Configure all detected AI agents (Antigravity, OpenCode, Claude, Cursor, VS Code)"
      echo "  --client <name>       Configure specific client only (antigravity, opencode, claude, cursor, vscode)"
      echo "  --bin-dir <dir>       Target binary install path (default: ~/.local/bin or /usr/local/bin)"
      echo "  --dry-run             Preview actions without writing configuration files"
      echo "  --with-frp            Initialize protected FRP tunnel credentials"
      echo "  --frp-server <addr>   FRP server hostname or IP"
      echo "  --user <name>         HTTP BasicAuth username for protected ingress"
      echo "  --pass <pass>         HTTP BasicAuth password (min 6 characters)"
      echo "  -h, --help            Show this help message"
      exit 0
      ;;
    *)
      echo "[ERROR] Unknown option: $1"
      exit 1
      ;;
  esac
done

echo "+--------------------------------------------------------+"
echo "| GOSSIP UNIVERSAL AUTO-INSTALLER                        |"
echo "| OS: $(uname -s) // ARCH: $(uname -m)                              |"
echo "+--------------------------------------------------------+"

# 1. Determine Binary Directory
BIN_DIR="${CUSTOM_BIN_DIR}"
if [[ -z "${BIN_DIR}" ]]; then
  if [[ -w "/usr/local/bin" ]]; then
    BIN_DIR="/usr/local/bin"
  else
    BIN_DIR="${HOME}/.local/bin"
  fi
fi

echo "[1/6] Installing binaries to ${BIN_DIR}..."
if [[ "${DRY_RUN}" = false ]]; then
  mkdir -p "${BIN_DIR}"
  
  # Build binaries if in source repo, or link existing binaries
  if [[ -f "${REPO_ROOT}/go.mod" ]] && command -v go >/dev/null 2>&1; then
    echo "  -> Compiling gossip and gossip-mcp from source..."
    (cd "${REPO_ROOT}" && go build -o "${BIN_DIR}/gossip" ./cmd/gossip)
    (cd "${REPO_ROOT}" && go build -o "${BIN_DIR}/gossip-mcp" ./cmd/gossip)
  elif [[ -f "${REPO_ROOT}/bin/gossip" ]]; then
    cp "${REPO_ROOT}/bin/gossip" "${BIN_DIR}/gossip"
    chmod +x "${BIN_DIR}/gossip"
    if [[ -f "${REPO_ROOT}/bin/gossip-mcp" ]]; then
      cp "${REPO_ROOT}/bin/gossip-mcp" "${BIN_DIR}/gossip-mcp"
      chmod +x "${BIN_DIR}/gossip-mcp"
    fi
  else
    echo "[WARN] No prebuilt binary found and Go compiler not available in PATH."
  fi
  chmod +x "${BIN_DIR}/gossip" 2>/dev/null || true
  echo "  [OK] Binaries installed at ${BIN_DIR}/gossip"
else
  echo "  [DRY-RUN] Would install binaries to ${BIN_DIR}"
fi

# PATH check
if [[ ":$PATH:" != *":${BIN_DIR}:"* ]]; then
  echo "  [NOTE] ${BIN_DIR} is not currently in your PATH."
  echo "         Add it: export PATH=\"${BIN_DIR}:\$PATH\""
fi

# Helper function to register JSON MCP servers cleanly with python3/node/sed
register_mcp_json() {
  local cfg_file="$1"
  local srv_name="$2"
  local bin_cmd="$3"
  
  if [[ "${DRY_RUN}" = true ]]; then
    echo "  [DRY-RUN] Would register '${srv_name}' in ${cfg_file}"
    return 0
  fi

  mkdir -p "$(dirname "${cfg_file}")"
  
  if command -v python3 >/dev/null 2>&1; then
    python3 -c "
import json, os, sys

p = sys.argv[1]
srv = sys.argv[2]
cmd = sys.argv[3]

data = {}
if os.path.exists(p):
    try:
        with open(p, 'r', encoding='utf-8') as f:
            data = json.load(f)
    except Exception:
        data = {}

if 'mcpServers' not in data or not isinstance(data['mcpServers'], dict):
    data['mcpServers'] = {}

data['mcpServers'][srv] = {
    'command': cmd,
    'args': ['mcp']
}

with open(p, 'w', encoding='utf-8') as f:
    json.dump(data, f, indent=2)
" "${cfg_file}" "${srv_name}" "${bin_cmd}"
    echo "  [OK] Registered MCP in ${cfg_file}"
  else
    echo "  [WARN] python3 not found; skipping automatic JSON update for ${cfg_file}"
  fi
}

# 2. Configure Google Antigravity
if [[ "${TARGET_ALL}" = true ]] || [[ "${TARGET_CLIENT}" == *"antigravity"* ]]; then
  echo "[2/6] Configuring Antigravity Agent & MCP plugin..."
  AGY_PLUGIN_DIR="${HOME}/.gemini/config/plugins/gossip"
  AGY_MCP_DIR="${HOME}/.gemini/antigravity/mcp/gossip"
  
  if [[ "${DRY_RUN}" = false ]]; then
    mkdir -p "$(dirname "${AGY_PLUGIN_DIR}")"
    mkdir -p "${AGY_MCP_DIR}"
    
    # Symlink plugin directory to development repo if available
    if [[ -d "${REPO_ROOT}" ]]; then
      ln -sfn "${REPO_ROOT}" "${AGY_PLUGIN_DIR}"
      echo "  [OK] Antigravity plugin linked: ${AGY_PLUGIN_DIR} -> ${REPO_ROOT}"
    fi

    # Register in config.json
    AGY_CFG="${HOME}/.gemini/config/config.json"
    register_mcp_json "${AGY_CFG}" "gossip" "${BIN_DIR}/gossip"

    # Register in mcp_config.json
    AGY_MCP_CFG="${HOME}/.gemini/config/mcp_config.json"
    register_mcp_json "${AGY_MCP_CFG}" "gossip" "${BIN_DIR}/gossip"
  else
    echo "  [DRY-RUN] Would link ${AGY_PLUGIN_DIR} and configure Antigravity MCP"
  fi
fi

# 3. Configure OpenCode
if [[ "${TARGET_ALL}" = true ]] || [[ "${TARGET_CLIENT}" == *"opencode"* ]]; then
  echo "[3/6] Configuring OpenCode MCP & Skill..."
  OC_DIR="${HOME}/.config/opencode"
  OC_CFG="${OC_DIR}/opencode.json"
  OC_SKILLS="${OC_DIR}/skills/gossip"
  
  if [[ "${DRY_RUN}" = false ]]; then
    mkdir -p "${OC_DIR}"
    register_mcp_json "${OC_CFG}" "gossip" "${BIN_DIR}/gossip"
    
    # Link skills
    mkdir -p "$(dirname "${OC_SKILLS}")"
    if [[ -d "${REPO_ROOT}/skills/gossip" ]]; then
      ln -sfn "${REPO_ROOT}/skills/gossip" "${OC_SKILLS}"
      echo "  [OK] OpenCode skill linked at ${OC_SKILLS}"
    fi
  else
    echo "  [DRY-RUN] Would register OpenCode MCP in ${OC_CFG} and link skills"
  fi
fi

# 4. Configure Claude Desktop
if [[ "${TARGET_ALL}" = true ]] || [[ "${TARGET_CLIENT}" == *"claude"* ]]; then
  echo "[4/6] Configuring Claude Desktop MCP..."
  CLAUDE_CFG=""
  if [[ "$(uname -s)" == "Darwin" ]]; then
    CLAUDE_CFG="${HOME}/Library/Application Support/Claude/claude_desktop_config.json"
  else
    CLAUDE_CFG="${HOME}/.config/Claude/claude_desktop_config.json"
  fi
  register_mcp_json "${CLAUDE_CFG}" "gossip" "${BIN_DIR}/gossip"
fi

# 5. Configure Cursor
if [[ "${TARGET_ALL}" = true ]] || [[ "${TARGET_CLIENT}" == *"cursor"* ]]; then
  echo "[5/6] Configuring Cursor MCP..."
  CURSOR_GLOBAL="${HOME}/.cursor/mcp.json"
  register_mcp_json "${CURSOR_GLOBAL}" "gossip" "${BIN_DIR}/gossip"
  if [[ -d ".cursor" ]]; then
    register_mcp_json ".cursor/mcp.json" "gossip" "${BIN_DIR}/gossip"
  fi
fi

# 6. Configure VS Code
if [[ "${TARGET_ALL}" = true ]] || [[ "${TARGET_CLIENT}" == *"vscode"* ]]; then
  echo "[6/6] Configuring VS Code Workspace MCP..."
  VSCODE_CFG=".vscode/mcp.json"
  if [[ -d ".vscode" ]] || [[ "${TARGET_CLIENT}" == *"vscode"* ]]; then
    register_mcp_json "${VSCODE_CFG}" "gossip" "${BIN_DIR}/gossip"
  else
    echo "  [SKIP] No .vscode directory found in current working directory"
  fi
fi

# Optional Protected FRP Tunnel Configuration
if [[ "${WITH_FRP}" = true ]]; then
  echo ""
  echo "[TUNNEL] Configuring Protected FRP Reverse-Proxy Tunnel..."
  if [[ -n "${FRP_SERVER}" ]]; then
    "${BIN_DIR}/gossip" tunnel init \
      --server "${FRP_SERVER}" \
      --user "${FRP_USER:-admin}" \
      --pass "${FRP_PASS:-SecureMeshPass123}"
  else
    echo "  [NOTE] FRP Server not specified. Run 'gossip tunnel init --server <addr> --user <user> --pass <pass>' later."
  fi
fi

echo ""
echo "+--------------------------------------------------------+"
echo "| [SUCCESS] GOSSIP INSTALLATION COMPLETED                |"
echo "+--------------------------------------------------------+"
echo "Verify status:          gossip status"
echo "Start MCP stdio mode:   gossip mcp"
echo "Launch Web Control:     gossip ui --port 8080"
echo "Periodic cron check:    gossip cron --interval 30s"
echo "Protected FRP tunnel:   gossip tunnel status"
echo "+--------------------------------------------------------+"
