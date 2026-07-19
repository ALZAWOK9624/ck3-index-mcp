#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=$(tr -d '\r\n' < "$repo/VERSION")
python3 -c '
import re, sys
if re.fullmatch(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", sys.argv[1]) is None:
    raise SystemExit("VERSION must contain one release semver without build metadata")
' "$version"

manifest_version=$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["version"])
' "$repo/plugin/ck3-index/.codex-plugin/plugin.json")
[ "$manifest_version" = "$version" ] || {
  echo "plugin manifest version '$manifest_version' does not match VERSION '$version'" >&2
  exit 1
}

config=${1:-"$repo/ck3-index.toml"}
[ -f "$config" ] || { echo "config not found: $config" >&2; exit 1; }
config=$(python3 -c 'import os, sys; print(os.path.abspath(sys.argv[1]))' "$config")

project_license=
for candidate in LICENSE LICENSE.txt LICENSE.md COPYING COPYING.txt; do
  if [ -f "$repo/$candidate" ]; then
    project_license=$repo/$candidate
    break
  fi
done
allow_unlicensed=${CK3_INDEX_ALLOW_UNLICENSED_RC:-0}
if [ -z "$project_license" ] && [ "$allow_unlicensed" != 1 ]; then
  echo "PROJECT_LICENSE_MISSING: add the ck3-index project license, or set CK3_INDEX_ALLOW_UNLICENSED_RC=1 for a local non-public RC" >&2
  exit 1
fi

export GOCACHE="$repo/cache/go-build-linux"
cd "$repo"
go run ./cmd/mcp-docgen -check
python3 "$repo/tools/test_release_bundle.py"
go mod verify
go test ./...
go vet ./...

binary="$repo/bin/ck3-index-v$version"
verify_binary="$repo/bin/ck3-index-v$version.repro-check"
mkdir -p "$repo/bin" "$repo/cache/third-party" "$repo/cache/plugin-stage-linux" "$repo/cache/release"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X ck3-index/internal/buildinfo.Version=$version" \
  -o "$binary" .
trap 'rm -f -- "$verify_binary"' 0 HUP INT TERM
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X ck3-index/internal/buildinfo.Version=$version" \
  -o "$verify_binary" .
cmp -s "$binary" "$verify_binary" || {
  echo "REPRODUCIBLE_BUILD_MISMATCH: repeated ck3-index Linux builds differ" >&2
  exit 1
}
rm -f -- "$verify_binary"
trap - 0 HUP INT TERM

wbt_manifest="$repo/third_party/whitebox-tools-v2.4.0.json"
archive_url=$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["platforms"]["linux-x64"]["archive_url"])
' "$wbt_manifest")
archive_sha=$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["platforms"]["linux-x64"]["archive_sha256"])
' "$wbt_manifest")
archive_root=$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["platforms"]["linux-x64"]["archive_root"])
' "$wbt_manifest")
sidecar_name=$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["platforms"]["linux-x64"]["binary"])
' "$wbt_manifest")
binary_sha=$(python3 -c '
import json, sys
print(json.load(open(sys.argv[1], encoding="utf-8"))["platforms"]["linux-x64"]["binary_sha256"])
' "$wbt_manifest")
case "$archive_sha$binary_sha" in ''|*[!0-9a-f]*) echo "invalid WhiteboxTools hashes" >&2; exit 1;; esac
[ "${#archive_sha}" -eq 64 ] && [ "${#binary_sha}" -eq 64 ] || {
  echo "invalid WhiteboxTools hash length" >&2
  exit 1
}

archive="$repo/cache/third-party/WhiteboxTools_linux_amd64.zip"
if [ ! -f "$archive" ] || [ "$(sha256sum "$archive" | awk '{print $1}')" != "$archive_sha" ]; then
  curl --fail --location --output "$archive" "$archive_url"
fi
[ "$(sha256sum "$archive" | awk '{print $1}')" = "$archive_sha" ] || {
  echo "WhiteboxTools archive hash mismatch" >&2
  exit 1
}

stage="$repo/cache/plugin-stage-linux/ck3-index"
extract="$repo/cache/plugin-stage-linux/whitebox-extract"
case "$stage" in "$repo"/cache/plugin-stage-linux/ck3-index) ;; *) echo "unsafe stage path" >&2; exit 1;; esac
case "$extract" in "$repo"/cache/plugin-stage-linux/whitebox-extract) ;; *) echo "unsafe extract path" >&2; exit 1;; esac
rm -rf -- "$stage" "$extract"
cp -R "$repo/plugin/ck3-index" "$stage"
mkdir -p "$extract" "$stage/bin" "$stage/sidecar" "$stage/third_party"
unzip -q "$archive" -d "$extract"
wbt_source="$extract/$archive_root"
[ -d "$wbt_source" ] || { echo "WhiteboxTools archive root is missing" >&2; exit 1; }
[ -f "$wbt_source/$sidecar_name" ] || { echo "WhiteboxTools executable is missing" >&2; exit 1; }
cp "$wbt_source/$sidecar_name" "$stage/sidecar/whitebox_tools"
[ "$(sha256sum "$stage/sidecar/whitebox_tools" | awk '{print $1}')" = "$binary_sha" ] || {
  echo "WhiteboxTools binary hash mismatch" >&2
  exit 1
}
chmod +x "$stage/sidecar/whitebox_tools" "$stage/scripts/start-ck3-index.sh" "$binary"
"$stage/sidecar/whitebox_tools" --version | grep -q 'v2\.4\.0' || {
  echo "WhiteboxTools executable version check failed" >&2
  exit 1
}
find "$stage/bin" -maxdepth 1 -type f -name 'ck3-index-v*' -delete
cp "$binary" "$stage/bin/ck3-index-v$version"
chmod +x "$stage/bin/ck3-index-v$version"
cp "$repo/skill/ck3-coding/SKILL.md" "$stage/skills/ck3-coding/SKILL.md"
cp "$wbt_manifest" "$stage/third_party/"
cp "$repo/third_party/WHITEBOXTOOLS_LICENSE.txt" "$stage/third_party/"
printf '{"config_path":"","version":1}\n' > "$stage/config/settings.json"
cat > "$stage/.mcp.json" <<'EOF'
{
  "mcpServers": {
    "ck3_index": {
      "command": "/bin/sh",
      "args": ["./scripts/start-ck3-index.sh"],
      "cwd": ".",
      "startup_timeout_sec": 60
    }
  }
}
EOF
rm -rf -- "$extract"

python3 "$repo/tools/verify_release_mcp.py" \
  --stage "$stage" \
  --platform linux-x64 \
  --config "$config" \
  --expected-standard-tools 29 \
  --expected-expert-tools 57

if [ "$allow_unlicensed" = 1 ] && [ -z "$project_license" ]; then
  archive_suffix=-unlicensed-local-rc
else
  archive_suffix=
fi
output=${2:-"$repo/cache/release/ck3-index-plugin-v$version-linux-x64$archive_suffix.zip"}
output=$(python3 -c 'import os, sys; print(os.path.abspath(sys.argv[1]))' "$output")
case "$output" in
  "$repo"/*) ;;
  *) echo "release archive must stay inside the repository for this local release workflow" >&2; exit 1 ;;
esac
set -- \
  --repo "$repo" \
  --stage "$stage" \
  --platform linux-x64 \
  --output "$output"
if [ "$allow_unlicensed" = 1 ]; then
  set -- "$@" --allow-missing-project-license
fi
python3 "$repo/tools/build_release_bundle.py" "$@"
