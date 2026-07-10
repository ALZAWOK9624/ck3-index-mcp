$ErrorActionPreference = 'Stop'

$pluginRoot = Split-Path -Parent $PSScriptRoot
$exe = Join-Path $pluginRoot 'bin\ck3-index-v0.2.1.exe'
$settingsPath = Join-Path $pluginRoot 'config\settings.json'
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw 'Bundled ck3-index v0.2.1 executable is missing.' }
if (-not (Test-Path -LiteralPath $settingsPath -PathType Leaf)) { throw 'UTF-8 plugin settings are missing.' }

$settings = Get-Content -LiteralPath $settingsPath -Raw -Encoding UTF8 | ConvertFrom-Json
$config = $env:CK3_INDEX_CONFIG
if ([string]::IsNullOrWhiteSpace($config)) { $config = [string]$settings.config_path }
if ([string]::IsNullOrWhiteSpace($config) -or -not (Test-Path -LiteralPath $config -PathType Leaf)) {
    throw 'ck3-index config was not found. Set CK3_INDEX_CONFIG or update config/settings.json.'
}
& $exe --config $config mcp
exit $LASTEXITCODE
