#!/usr/bin/env bash
# Install ccs by linking it into ~/.local/bin (or $CCS_BIN_DIR). Safe to re-run.
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bin_dir="${CCS_BIN_DIR:-$HOME/.local/bin}"

if ! command -v python3 >/dev/null 2>&1; then
  echo "ccs: python3 is required but was not found." >&2
  exit 1
fi
if ! python3 -c 'import sys; sys.exit(0 if sys.version_info >= (3, 8) else 1)'; then
  echo "ccs: python3 >= 3.8 is required." >&2
  exit 1
fi

chmod +x "$repo_dir/ccs"
mkdir -p "$bin_dir"
ln -sfn "$repo_dir/ccs" "$bin_dir/ccs"
echo "Linked $bin_dir/ccs -> $repo_dir/ccs"

case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "Warning: $bin_dir is not in PATH. Add this to your shell rc file:"
     echo "  export PATH=\"$bin_dir:\$PATH\"" ;;
esac

"$bin_dir/ccs" --version
