#!/bin/sh
set -eu

plugin_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$plugin_root/.codex-plugin/plugin.json" | head -n 1)
binary_version=${version%%+*}
case "$binary_version" in
  ''|*[!0-9A-Za-z.-]*) echo "invalid plugin version" >&2; exit 1 ;;
esac
exe="$plugin_root/bin/ck3-index-v$binary_version"
[ -x "$exe" ] || { echo "bundled ck3-index executable is missing" >&2; exit 1; }

gis="$plugin_root/sidecar/whitebox_tools"
manifest="$plugin_root/third_party/whitebox-tools-v2.4.0.json"
if { [ -e "$gis" ] && [ ! -f "$manifest" ]; } || { [ ! -e "$gis" ] && [ -f "$manifest" ]; }; then
  echo "bundled GIS sidecar is incomplete: the manifest and executable must be present together" >&2
  exit 1
fi
if [ -e "$gis" ] && [ -f "$manifest" ]; then
  [ -x "$gis" ] || { echo "bundled WhiteboxTools executable is not executable" >&2; exit 1; }
  expected=$(sed -n '/"linux-x64"/,/^[[:space:]]*}/s/.*"binary_sha256"[[:space:]]*:[[:space:]]*"\([0-9a-f]*\)".*/\1/p' "$manifest" | head -n 1)
  actual=$(sha256sum "$gis" | awk '{print $1}')
  case "$expected" in ''|*[!0-9a-f]*) echo "invalid WhiteboxTools hash in release manifest" >&2; exit 1 ;; esac
  [ "${#expected}" -eq 64 ] || { echo "invalid WhiteboxTools hash in release manifest" >&2; exit 1; }
  [ "$actual" = "$expected" ] || { echo "bundled WhiteboxTools SHA-256 mismatch" >&2; exit 1; }
  export CK3_INDEX_GIS_SIDECAR_PATH="$gis"
  export CK3_INDEX_GIS_SIDECAR_SHA256="$expected"
fi

if [ -z "${CK3_INDEX_MAP_FONT:-}" ]; then
  for font in \
    /usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc \
    /usr/share/fonts/truetype/noto/NotoSansCJK-Regular.ttc \
    /usr/share/fonts/opentype/noto/NotoSansSC-Regular.otf \
    /usr/share/fonts/truetype/droid/DroidSansFallbackFull.ttf \
    /usr/share/fonts/truetype/wqy/wqy-zenhei.ttc
  do
    if [ -f "$font" ]; then
      export CK3_INDEX_MAP_FONT="$font"
      break
    fi
  done
fi

config=${CK3_INDEX_CONFIG:-}
if [ -z "$config" ]; then
  config=$(sed -n 's/.*"config_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$plugin_root/config/settings.json" | head -n 1)
fi
[ -n "$config" ] && [ -f "$config" ] || { echo "ck3-index config was not found" >&2; exit 1; }
exec "$exe" --config "$config" mcp
