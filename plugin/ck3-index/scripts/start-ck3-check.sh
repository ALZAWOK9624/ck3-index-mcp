#!/bin/sh
set -eu
plugin_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$plugin_root/.codex-plugin/plugin.json" | head -n 1)
binary_version=${version%%+*}
case "$binary_version" in
  ''|*[!0-9A-Za-z.-]*) echo "invalid plugin version" >&2; exit 1 ;;
esac
exe="$plugin_root/bin/ck3-index-v$binary_version"
[ -x "$exe" ] || { echo "bundled checker executable is missing" >&2; exit 1; }
exec "$exe" check --serve
