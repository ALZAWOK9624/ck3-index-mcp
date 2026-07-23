param(
    [string]$PluginPath = "$HOME\plugins\ck3-index",
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\ck3-index.toml'),
    [string]$ArchivePath = '',
    [switch]$AllowUnlicensedRC,
    [switch]$SkipInstall
)

$ErrorActionPreference = 'Stop'

function Assert-SafeDirectoryTarget {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$ExpectedLeaf,
        [string]$ForbiddenPath = ''
    )

    $fullPath = [IO.Path]::GetFullPath($Path)
    $rootPath = [IO.Path]::GetPathRoot($fullPath)
    if ([string]::Equals($fullPath.TrimEnd('\'), $rootPath.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to use a filesystem root as a recursive target: $fullPath"
    }
    if (-not [string]::Equals((Split-Path $fullPath -Leaf), $ExpectedLeaf, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Recursive target must end in '$ExpectedLeaf': $fullPath"
    }
    if (-not [string]::IsNullOrWhiteSpace($ForbiddenPath)) {
        $forbidden = [IO.Path]::GetFullPath($ForbiddenPath)
        if ([string]::Equals($fullPath.TrimEnd('\'), $forbidden.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to replace the source plugin directory: $fullPath"
        }
    }
    return $fullPath
}

$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$resolvedConfig = (Resolve-Path -LiteralPath $ConfigPath).Path
Set-Location -LiteralPath $repo
$versionPath = Join-Path $repo 'VERSION'
$version = (Get-Content -LiteralPath $versionPath -Raw -Encoding UTF8).Trim()
if ($version -notmatch '^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') {
    throw "VERSION must contain one release semver without build metadata: $version"
}
$projectLicense = @('LICENSE', 'LICENSE.txt', 'LICENSE.md', 'COPYING', 'COPYING.txt') |
    ForEach-Object { Join-Path $repo $_ } |
    Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
    Select-Object -First 1
if ([string]::IsNullOrWhiteSpace([string]$projectLicense) -and -not $AllowUnlicensedRC) {
    throw 'PROJECT_LICENSE_MISSING: choose and add the ck3-index project license, or pass -AllowUnlicensedRC for a local non-public RC.'
}

$goCommand = Get-Command go.exe -ErrorAction Stop
$pythonCommand = Get-Command python.exe -ErrorAction Stop
$go = $goCommand.Source
$python = $pythonCommand.Source
$codex = $null
if (-not $SkipInstall) {
    $codex = (Get-Command codex.cmd -ErrorAction Stop).Source
}

$pluginSource = (Resolve-Path (Join-Path $repo 'plugin\ck3-index')).Path
$sourceManifestPath = Join-Path $pluginSource '.codex-plugin\plugin.json'
$sourceManifest = Get-Content -LiteralPath $sourceManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
if ([string]$sourceManifest.version -ne $version) {
    throw "Plugin manifest version '$($sourceManifest.version)' does not match VERSION '$version'."
}

$wbtManifestPath = Join-Path $repo 'third_party\whitebox-tools-v2.4.0.json'
$wbtLicensePath = Join-Path $repo 'third_party\WHITEBOXTOOLS_LICENSE.txt'
$wbtManifest = Get-Content -LiteralPath $wbtManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
$wbtPlatform = $wbtManifest.platforms.'windows-x64'
if ([string]$wbtManifest.version -ne '2.4.0' -or [string]::IsNullOrWhiteSpace([string]$wbtPlatform.archive_sha256)) {
    throw 'WhiteboxTools release manifest is invalid.'
}

$creator = Join-Path $HOME '.codex\skills\.system\plugin-creator'
$skillCreator = Join-Path $HOME '.codex\skills\.system\skill-creator'
$pluginValidator = Join-Path $creator 'scripts\validate_plugin.py'
$skillValidator = Join-Path $skillCreator 'scripts\quick_validate.py'
$cachebuster = Join-Path $creator 'scripts\update_plugin_cachebuster.py'
$marketplaceReader = Join-Path $creator 'scripts\read_marketplace_name.py'
$bundleBuilder = Join-Path $repo 'tools\build_release_bundle.py'
$mcpSmoke = Join-Path $repo 'tools\verify_release_mcp.py'
$bundleTests = Join-Path $repo 'tools\test_release_bundle.py'
$requiredFiles = @($pluginValidator, $skillValidator, $bundleBuilder, $mcpSmoke, $bundleTests)
if (-not $SkipInstall) { $requiredFiles += @($cachebuster, $marketplaceReader) }
foreach ($requiredFile in $requiredFiles) {
    if (-not (Test-Path -LiteralPath $requiredFile -PathType Leaf)) {
        throw "Required release helper is missing: $requiredFile"
    }
}

$stageRoot = [IO.Path]::GetFullPath((Join-Path $repo 'cache\plugin-stage'))
$stage = Assert-SafeDirectoryTarget -Path (Join-Path $stageRoot 'ck3-index') -ExpectedLeaf 'ck3-index' -ForbiddenPath $pluginSource
if (-not $stage.StartsWith($repo.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) {
    throw "Plugin staging directory escaped the repository: $stage"
}

$binaryName = "ck3-index-v$version.exe"
$repoBin = Join-Path $repo 'bin'
$repoBinary = Join-Path $repoBin $binaryName
$verifyBinary = Join-Path $repoBin "$binaryName.repro-check"
$env:GOCACHE = Join-Path $repo 'cache\go-build'
$env:CGO_ENABLED = '0'

& $go run ./cmd/mcp-docgen -check
if ($LASTEXITCODE -ne 0) { throw 'Generated MCP documentation is stale.' }
& $python $bundleTests
if ($LASTEXITCODE -ne 0) { throw 'Release bundle tests failed.' }
& $go mod verify
if ($LASTEXITCODE -ne 0) { throw 'Go module verification failed.' }
& $go test ./...
if ($LASTEXITCODE -ne 0) { throw 'Go tests failed.' }
& $go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'Go vet failed.' }
& $python $skillValidator (Join-Path $repo 'skill\ck3-coding')
if ($LASTEXITCODE -ne 0) { throw 'Skill validation failed.' }

New-Item -ItemType Directory -Force -Path $repoBin | Out-Null
& $go build -trimpath -buildvcs=false -ldflags "-s -w -X ck3-index/internal/buildinfo.Version=$version" -o $repoBinary .
if ($LASTEXITCODE -ne 0) { throw 'Go build failed.' }
try {
    & $go build -trimpath -buildvcs=false -ldflags "-s -w -X ck3-index/internal/buildinfo.Version=$version" -o $verifyBinary .
    if ($LASTEXITCODE -ne 0) { throw 'Reproducibility verification build failed.' }
    $firstHash = (Get-FileHash -LiteralPath $repoBinary -Algorithm SHA256).Hash.ToLowerInvariant()
    $secondHash = (Get-FileHash -LiteralPath $verifyBinary -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($firstHash -ne $secondHash) {
        throw "REPRODUCIBLE_BUILD_MISMATCH: repeated ck3-index builds differ ($firstHash vs $secondHash)."
    }
}
finally {
    Remove-Item -LiteralPath $verifyBinary -Force -ErrorAction SilentlyContinue
}
& $repoBinary --config $resolvedConfig health | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Built binary health check failed.' }

New-Item -ItemType Directory -Force -Path $stageRoot | Out-Null
if (Test-Path -LiteralPath $stage) {
    Remove-Item -LiteralPath $stage -Recurse -Force
}
Copy-Item -LiteralPath $pluginSource -Destination $stageRoot -Recurse -Force

$thirdPartyCache = Join-Path $repo 'cache\third-party'
New-Item -ItemType Directory -Force -Path $thirdPartyCache | Out-Null
$wbtArchive = Join-Path $thirdPartyCache 'WhiteboxTools_win_amd64.zip'
$downloadWhitebox = $true
if (Test-Path -LiteralPath $wbtArchive -PathType Leaf) {
    $downloadWhitebox = (Get-FileHash -LiteralPath $wbtArchive -Algorithm SHA256).Hash.ToLowerInvariant() -ne [string]$wbtPlatform.archive_sha256
}
if ($downloadWhitebox) {
    Invoke-WebRequest -Uri ([string]$wbtPlatform.archive_url) -OutFile $wbtArchive
}
if ((Get-FileHash -LiteralPath $wbtArchive -Algorithm SHA256).Hash.ToLowerInvariant() -ne [string]$wbtPlatform.archive_sha256) {
    throw 'Downloaded WhiteboxTools archive SHA-256 does not match the release manifest.'
}
$wbtExtract = Assert-SafeDirectoryTarget -Path (Join-Path $stageRoot 'whitebox-extract') -ExpectedLeaf 'whitebox-extract'
if (Test-Path -LiteralPath $wbtExtract) { Remove-Item -LiteralPath $wbtExtract -Recurse -Force }
Expand-Archive -LiteralPath $wbtArchive -DestinationPath $wbtExtract -Force
$wbtSource = Join-Path $wbtExtract ([string]$wbtPlatform.archive_root -replace '/', '\')
$wbtBinary = Join-Path $wbtSource ([string]$wbtPlatform.binary)
if (-not (Test-Path -LiteralPath $wbtBinary -PathType Leaf)) { throw 'WhiteboxTools archive lacks the expected executable.' }
if ((Get-FileHash -LiteralPath $wbtBinary -Algorithm SHA256).Hash.ToLowerInvariant() -ne [string]$wbtPlatform.binary_sha256) {
    throw 'WhiteboxTools executable SHA-256 does not match the release manifest.'
}
$wbtVersionText = (& $wbtBinary --version | Out-String)
if ($LASTEXITCODE -ne 0 -or $wbtVersionText -notmatch 'v2\.4\.0') { throw 'WhiteboxTools executable version check failed.' }
$stageSidecar = Join-Path $stage 'sidecar'
New-Item -ItemType Directory -Force -Path $stageSidecar | Out-Null
Copy-Item -LiteralPath $wbtBinary -Destination (Join-Path $stageSidecar 'whitebox_tools.exe') -Force
$stageThirdParty = Join-Path $stage 'third_party'
New-Item -ItemType Directory -Force -Path $stageThirdParty | Out-Null
Copy-Item -LiteralPath $wbtManifestPath -Destination (Join-Path $stageThirdParty 'whitebox-tools-v2.4.0.json') -Force
Copy-Item -LiteralPath $wbtLicensePath -Destination (Join-Path $stageThirdParty 'WHITEBOXTOOLS_LICENSE.txt') -Force
Remove-Item -LiteralPath $wbtExtract -Recurse -Force

$stageBin = Join-Path $stage 'bin'
New-Item -ItemType Directory -Force -Path $stageBin | Out-Null
Get-ChildItem -LiteralPath $stageBin -Filter 'ck3-index-v*.exe' -File -ErrorAction SilentlyContinue | Remove-Item -Force
Copy-Item -LiteralPath $repoBinary -Destination (Join-Path $stageBin $binaryName) -Force
Copy-Item -LiteralPath (Join-Path $repo 'skill\ck3-coding\SKILL.md') -Destination (Join-Path $stage 'skills\ck3-coding\SKILL.md') -Force
$settings = @{version = 1; config_path = ''} | ConvertTo-Json -Compress
[IO.File]::WriteAllText((Join-Path $stage 'config\settings.json'), $settings, [Text.UTF8Encoding]::new($false))

& $python $pluginValidator $stage
if ($LASTEXITCODE -ne 0) { throw 'Staged plugin validation failed.' }

& $python $mcpSmoke --stage $stage --platform windows-x64 --config $resolvedConfig --expected-standard-tools 30 --expected-expert-tools 58
if ($LASTEXITCODE -ne 0) { throw 'Staged plugin MCP smoke check failed.' }

$releaseRoot = Join-Path $repo 'cache\release'
New-Item -ItemType Directory -Force -Path $releaseRoot | Out-Null
if ([string]::IsNullOrWhiteSpace($ArchivePath)) {
    $archiveSuffix = if ([string]::IsNullOrWhiteSpace([string]$projectLicense)) { '-unlicensed-local-rc' } else { '' }
    $ArchivePath = Join-Path $releaseRoot "ck3-index-plugin-v$version-windows-x64$archiveSuffix.zip"
}
$archive = [IO.Path]::GetFullPath($ArchivePath)
if (-not $archive.StartsWith($repo.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) {
    throw "Release archive must stay inside the repository for this local release workflow: $archive"
}
$bundleArgs = @(
    $bundleBuilder,
    '--repo', $repo,
    '--stage', $stage,
    '--platform', 'windows-x64',
    '--output', $archive
)
if ($AllowUnlicensedRC) { $bundleArgs += '--allow-missing-project-license' }
$bundleJSON = (& $python @bundleArgs | Out-String)
if ($LASTEXITCODE -ne 0) { throw 'Release bundle validation or deterministic archive build failed.' }
$bundle = $bundleJSON | ConvertFrom-Json

if (-not $SkipInstall) {
    $destination = Assert-SafeDirectoryTarget -Path $PluginPath -ExpectedLeaf 'ck3-index' -ForbiddenPath $pluginSource
    $destinationParent = Split-Path $destination -Parent
    New-Item -ItemType Directory -Force -Path $destinationParent | Out-Null
    if (Test-Path -LiteralPath $destination) {
        Remove-Item -LiteralPath $destination -Recurse -Force
    }
    Copy-Item -LiteralPath $stage -Destination $destinationParent -Recurse -Force
    Remove-Item -LiteralPath (Join-Path $destination 'RELEASE_MANIFEST.json') -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $destination 'SHA256SUMS') -Force -ErrorAction SilentlyContinue
    $installedSettings = @{version = 1; config_path = $resolvedConfig} | ConvertTo-Json -Compress
    [IO.File]::WriteAllText(
        (Join-Path $destination 'config\settings.json'),
        $installedSettings,
        [Text.UTF8Encoding]::new($false)
    )
    & $python $cachebuster $destination
    if ($LASTEXITCODE -ne 0) { throw 'Cachebuster update failed.' }
    & $python $pluginValidator $destination
    if ($LASTEXITCODE -ne 0) { throw 'Installed plugin validation failed.' }
    $installedManifest = Get-Content -LiteralPath (Join-Path $destination '.codex-plugin\plugin.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    $installedVersion = [string]$installedManifest.version
    $marketplace = (& $python $marketplaceReader).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($marketplace)) { throw 'Marketplace lookup failed.' }
    if ($marketplace -notmatch '^[0-9A-Za-z._-]+$') { throw "Unsafe marketplace name: $marketplace" }
    $selector = "ck3-index@$marketplace"
    $installResult = (& $codex plugin add $selector --json | Out-String) | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) { throw 'Plugin reinstall failed.' }
    if ([string]$installResult.pluginId -ne $selector -or [string]$installResult.version -ne $installedVersion) {
        throw "Plugin install result mismatch: expected $selector $installedVersion."
    }
    $installedCache = [IO.Path]::GetFullPath([string]$installResult.installedPath)
    $expectedCacheRoot = [IO.Path]::GetFullPath((Join-Path $env:USERPROFILE ".codex\plugins\cache\$marketplace\ck3-index"))
    if (-not $installedCache.StartsWith($expectedCacheRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Plugin install returned an unexpected cache path.'
    }
    $cachedManifestPath = Join-Path $installedCache '.codex-plugin\plugin.json'
    if (-not (Test-Path -LiteralPath $cachedManifestPath -PathType Leaf)) {
        throw 'Plugin install did not publish a cached manifest.'
    }
    $cachedManifest = Get-Content -LiteralPath $cachedManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
    if ([string]$cachedManifest.version -ne $installedVersion) {
        throw "Cached plugin version mismatch: expected $installedVersion, found $([string]$cachedManifest.version)."
    }
    Write-Host "Installed $selector $installedVersion. Start a new Codex thread to activate the updated tools."
}

[pscustomobject]@{
    version = $version
    binary = $repoBinary
    stage = $stage
    archive = $archive
    archive_sha256 = [string]$bundle.archive_sha256
    release_ready = [bool]$bundle.release_ready
    blockers = @($bundle.blockers)
    installed = -not $SkipInstall
    plugin_path = if ($SkipInstall) { $null } else { [IO.Path]::GetFullPath($PluginPath) }
}
