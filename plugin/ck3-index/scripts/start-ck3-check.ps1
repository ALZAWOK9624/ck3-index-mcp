$ErrorActionPreference = 'Stop'
$pluginRoot = Split-Path -Parent $PSScriptRoot
$manifest = Get-Content -LiteralPath (Join-Path $pluginRoot '.codex-plugin\plugin.json') -Raw -Encoding UTF8 | ConvertFrom-Json
$version = ([string]$manifest.version -split '\+', 2)[0]
if ($version -notmatch '^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') { throw 'Invalid plugin version.' }
$exe = Join-Path $pluginRoot "bin\ck3-index-v$version.exe"
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw 'Bundled checker executable is missing.' }
# The check dispatcher runs before config loading. No settings, database,
# source root, engine-log path, or GIS sidecar is passed to this process.
& $exe check --serve
exit $LASTEXITCODE
