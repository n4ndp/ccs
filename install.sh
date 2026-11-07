#!/usr/bin/env bash
# Build ccs and install the binary into ~/.local/bin (or $CCS_BIN_DIR). Safe to re-run.
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bin_dir="${CCS_BIN_DIR:-$HOME/.local/bin}"
target="$bin_dir/ccs"

if ! command -v go >/dev/null 2>&1; then
  echo "ccs: Go >= 1.24 is required to build (https://go.dev/dl/)." >&2
  exit 1
fi

version="$(git -C "$repo_dir" describe --tags --always --dirty 2>/dev/null || echo dev)"
mkdir -p "$bin_dir"
# An older install linked the Python script here; never build through a symlink.
if [ -L "$target" ]; then
  rm -f "$target"
fi

(cd "$repo_dir" && go build -trimpath \
  -ldflags "-s -w -X github.com/n4ndp/ccs/internal/cli.Version=$version" \
  -o "$target" .)
echo "Installed $target ($version)"

case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "Warning: $bin_dir is not in PATH. Add this to your shell rc file:"
     echo "  export PATH=\"$bin_dir:\$PATH\"" ;;
esac

shell_name="$(basename "${SHELL:-}")"
case "$shell_name" in
  zsh|bash|fish) echo "Tab completion: ccs completion $shell_name --help" ;;
esac

"$target" --version
