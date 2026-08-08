# Builds the local MCP server into bin/ck3-index.exe with the version and the
# source commit stamped in.
#
# This exists because the binary the MCP client launches is a hand-built file
# with no link back to the tree it came from. A plain `go build` stamps neither,
# so a server can silently sit several commits behind its own source and still
# report a plausible version. After running this, ck3_health reports both
# binary_version and binary_revision, which is enough to spot the gap.
#
# The MCP client holds the exe open while it is running, so a rebuild fails
# until the client is stopped. That failure is the intended behaviour: it is
# better than replacing the binary underneath a live session.

[CmdletBinding()]
param(
    [string]$Output = "bin/ck3-index.exe"
)

$ErrorActionPreference = "Stop"
$repo = Split-Path -Parent $PSScriptRoot
Set-Location $repo

$version = (Get-Content (Join-Path $repo "VERSION") -Raw).Trim()

# git writes benign notices (CRLF conversion, for one) to stderr, and under
# ErrorActionPreference=Stop Windows PowerShell turns any native stderr line
# into a terminating NativeCommandError. Relax it around the git calls only.
$previousPreference = $ErrorActionPreference
$ErrorActionPreference = "Continue"
$revision = (& git rev-parse --short HEAD | Select-Object -First 1).Trim()
# A binary built from uncommitted edits is not the commit it names, and that is
# exactly the case where knowing so matters.
$dirty = & git status --porcelain
$ErrorActionPreference = $previousPreference

if ($dirty) { $revision = "$revision-dirty" }

$target = Join-Path $repo $Output
New-Item -ItemType Directory -Force (Split-Path -Parent $target) | Out-Null

Write-Host "building $version ($revision) -> $Output"
& go build -trimpath -buildvcs=false `
    -ldflags "-s -w -X ck3-index/internal/buildinfo.Version=$version -X ck3-index/internal/buildinfo.Revision=$revision" `
    -o $target .
if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }

Write-Host "ok: $target"
Write-Host "restart the MCP client so it picks up the new binary."
