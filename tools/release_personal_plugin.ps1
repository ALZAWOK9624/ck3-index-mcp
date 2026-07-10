param(
    [string]$PluginPath = "$HOME\plugins\ck3-index",
    [string]$ConfigPath = (Join-Path $PSScriptRoot '..\ck3-index.toml')
)
$ErrorActionPreference = 'Stop'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$go = (Resolve-Path (Join-Path $repo '..\tools\go1.26.4\go\bin\go.exe')).Path
$python = 'C:\Program Files\Python313\python.exe'
$stage = Join-Path $repo 'plugin\ck3-index'
$creator = Join-Path $HOME '.codex\skills\.system\plugin-creator'
$version = '0.2.1'
$binaryName = "ck3-index-v$version.exe"
$env:GOCACHE = Join-Path $repo 'cache\go-build'

& $go test ./...
if ($LASTEXITCODE -ne 0) { throw 'Go tests failed.' }
New-Item -ItemType Directory -Force -Path (Join-Path $stage 'bin'), (Join-Path $stage 'skills\ck3-coding'), (Join-Path $stage 'config') | Out-Null
Get-ChildItem -LiteralPath (Join-Path $stage 'bin') -Filter 'ck3-index-v*.exe' -File -ErrorAction SilentlyContinue | Remove-Item -Force
& $go build -buildvcs=false -o (Join-Path $stage "bin\$binaryName") .
if ($LASTEXITCODE -ne 0) { throw 'Go build failed.' }
New-Item -ItemType Directory -Force -Path (Join-Path $repo 'bin') | Out-Null
Copy-Item -LiteralPath (Join-Path $stage "bin\$binaryName") -Destination (Join-Path $repo "bin\$binaryName") -Force
Copy-Item -LiteralPath (Join-Path $repo 'skill\ck3-coding\SKILL.md') -Destination (Join-Path $stage 'skills\ck3-coding\SKILL.md') -Force
$settings = @{version=1; config_path=(Resolve-Path $ConfigPath).Path} | ConvertTo-Json
[IO.File]::WriteAllText((Join-Path $stage 'config\settings.json'), $settings, [Text.UTF8Encoding]::new($false))
Copy-Item -LiteralPath $stage -Destination (Split-Path $PluginPath -Parent) -Recurse -Force
Get-ChildItem -LiteralPath (Join-Path $PluginPath 'bin') -Filter 'ck3-index-v*.exe' -File -ErrorAction SilentlyContinue | Where-Object Name -ne $binaryName | Remove-Item -Force
& $python (Join-Path $creator 'scripts\update_plugin_cachebuster.py') $PluginPath
if ($LASTEXITCODE -ne 0) { throw 'Cachebuster update failed.' }
& $python (Join-Path $creator 'scripts\validate_plugin.py') $PluginPath
if ($LASTEXITCODE -ne 0) { throw 'Plugin validation failed.' }
$marketplace = & $python (Join-Path $creator 'scripts\read_marketplace_name.py')
& codex.cmd plugin remove "ck3-index@$marketplace" | Out-Null
& codex.cmd plugin add "ck3-index@$marketplace"
if ($LASTEXITCODE -ne 0) { throw 'Plugin reinstall failed.' }
