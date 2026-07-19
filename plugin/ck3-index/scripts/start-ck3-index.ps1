$ErrorActionPreference = 'Stop'

$pluginRoot = Split-Path -Parent $PSScriptRoot
$manifestPath = Join-Path $pluginRoot '.codex-plugin\plugin.json'
$settingsPath = Join-Path $pluginRoot 'config\settings.json'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw 'Plugin manifest is missing.' }
if (-not (Test-Path -LiteralPath $settingsPath -PathType Leaf)) { throw 'UTF-8 plugin settings are missing.' }

$manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
$manifestVersion = [string]$manifest.version
$binaryVersion = ($manifestVersion -split '\+', 2)[0]
if ($binaryVersion -notmatch '^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') {
    throw "Plugin manifest has an invalid version: $manifestVersion"
}
$exe = Join-Path $pluginRoot "bin\ck3-index-v$binaryVersion.exe"
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw "Bundled ck3-index v$binaryVersion executable is missing." }

$gisManifestPath = Join-Path $pluginRoot 'third_party\whitebox-tools-v2.4.0.json'
$gisBinary = Join-Path $pluginRoot 'sidecar\whitebox_tools.exe'
$hasGISManifest = Test-Path -LiteralPath $gisManifestPath -PathType Leaf
$hasGISBinary = Test-Path -LiteralPath $gisBinary -PathType Leaf
if ($hasGISManifest -xor $hasGISBinary) {
    throw 'Bundled GIS sidecar is incomplete: the manifest and executable must be present together.'
}
if ($hasGISManifest -and $hasGISBinary) {
    $gisManifest = Get-Content -LiteralPath $gisManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $platform = $gisManifest.platforms.'windows-x64'
    if ($null -eq $platform -or [string]$platform.binary_sha256 -notmatch '^[0-9a-f]{64}$') {
        throw 'Bundled WhiteboxTools manifest lacks a valid windows-x64 executable hash.'
    }
    $actualHash = (Get-FileHash -LiteralPath $gisBinary -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualHash -ne [string]$platform.binary_sha256) { throw 'Bundled WhiteboxTools SHA-256 does not match the release manifest.' }
    $env:CK3_INDEX_GIS_SIDECAR_PATH = $gisBinary
    $env:CK3_INDEX_GIS_SIDECAR_SHA256 = [string]$platform.binary_sha256
}

if ([string]::IsNullOrWhiteSpace($env:CK3_INDEX_MAP_FONT) -and -not [string]::IsNullOrWhiteSpace($env:WINDIR)) {
    $fontRoot = Join-Path $env:WINDIR 'Fonts'
    foreach ($fontName in @('NotoSansSC-VF.ttf', 'Deng.ttf', 'simhei.ttf', 'msyh.ttc', 'simsun.ttc')) {
        $candidate = Join-Path $fontRoot $fontName
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            $env:CK3_INDEX_MAP_FONT = $candidate
            break
        }
    }
}

$settings = Get-Content -LiteralPath $settingsPath -Raw -Encoding UTF8 | ConvertFrom-Json
$config = $env:CK3_INDEX_CONFIG
if ([string]::IsNullOrWhiteSpace($config)) { $config = [string]$settings.config_path }
if ([string]::IsNullOrWhiteSpace($config) -or -not (Test-Path -LiteralPath $config -PathType Leaf)) {
    throw 'ck3-index config was not found. Set CK3_INDEX_CONFIG or update config/settings.json.'
}
& $exe --config $config mcp
exit $LASTEXITCODE
