#!/usr/bin/env bash
set -euo pipefail

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
