#!/usr/bin/env bash
# Gossip root install wrapper
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "${DIR}/scripts/install.sh" "$@"
