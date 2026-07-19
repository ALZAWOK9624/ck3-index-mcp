# ck3-index 发布流程

## 当前候选状态

仓库当前正式版本为 `0.4.0`，采用 GPL-3.0-or-later。`VERSION` 是构建版本的唯一来源，发布脚本会检查插件 manifest 与它完全一致，并通过 Go linker 把相同版本注入 MCP `serverInfo.version`。

项目采用 GPL-3.0-or-later。WhiteboxTools 与 Go 依赖的第三方许可证会随发布包保留，并不替代 ck3-index 自身许可证。

## Current public-release status

The intended public release is `0.4.0` under GPL-3.0-or-later. Build artifacts must still pass the reproducibility, dependency-notice, release-manifest, and real MCP smoke-test gates below. Do not publish artifacts produced with an unlicensed-RC override.

## Windows x64 本地 RC

```powershell
powershell -NoProfile -ExecutionPolicy Bypass `
  -File tools/release_personal_plugin.ps1 `
  -SkipInstall `
  -AllowUnlicensedRC
```

`-SkipInstall` 只构建并验证便携暂存目录和 ZIP，不更新个人插件目录，不调用 Codex 安装。发布脚本只允许把 ZIP 写入仓库内的 `cache/release/` 或明确指定的仓库内路径。

项目许可证加入后，移除 `-AllowUnlicensedRC` 即可启用正式发布闸门。若确实要更新个人安装，另行省略 `-SkipInstall`；个人安装副本会在复制完成后才写入本机配置路径，便携暂存目录和 ZIP 始终保持空 `config_path`。

## Linux x64 本地 RC

```sh
CK3_INDEX_ALLOW_UNLICENSED_RC=1 \
  sh tools/release_plugin_linux.sh
```

第一个可选参数是供冒烟测试使用的配置文件，第二个可选参数是仓库内的 ZIP 输出路径。便携 Linux 包不会把该配置路径写入 `settings.json`；实际启动时使用 `CK3_INDEX_CONFIG`。

加入项目许可证后，不再设置 `CK3_INDEX_ALLOW_UNLICENSED_RC`。

## 固定发布闸门

两个平台脚本都会依次执行：

1. 检查 `VERSION`、插件 manifest 和变更日志候选版本。
2. 检查生成式 MCP 文档无漂移。
3. 运行发布辅助脚本测试、`go mod verify`、`go test ./...` 与 `go vet ./...`。
4. 使用 `CGO_ENABLED=0`、`-trimpath`、`-buildvcs=false` 和固定 linker 版本构建两次；二进制 SHA-256 不一致即阻止发布。
5. 从 `third_party/whitebox-tools-v2.4.0.json` 读取平台下载地址、压缩包 SHA-256、目录和可执行文件 SHA-256；不会在平台脚本中维护第二份哈希。
6. 检查 WhiteboxTools 版本，并把匹配平台的 sidecar、manifest 和许可证装入暂存目录。
7. 使用真实插件 launcher 启动 MCP，核对协议版本、服务端版本、标准/专家工具数、`ck3_health`、GIS sidecar 哈希和地图审计调用。
8. 通过 `go list -deps` 收集实际链接进二进制的 Go 模块许可证，生成 `THIRD_PARTY_NOTICES.md`。
9. 拒绝符号链接、缓存、数据库、WAL、日志、旧二进制、旧压缩包、意外顶层文件和本机私有路径。
10. 生成无时间字段、无用户名、无本机路径的 `RELEASE_MANIFEST.json`；`SHA256SUMS` 覆盖 manifest 与除 `SHA256SUMS` 自身外的全部暂存文件。
11. 按固定路径顺序、固定 ZIP 时间戳和固定权限生成候选 ZIP。

## 便携包结构

```text
ck3-index/
  .codex-plugin/plugin.json
  .mcp.json
  bin/ck3-index-v<VERSION>[.exe]
  config/settings.json
  scripts/start-ck3-index.ps1
  scripts/start-ck3-index.sh
  sidecar/
  skills/ck3-coding/SKILL.md
  third_party/
    WHITEBOXTOOLS_LICENSE.txt
    whitebox-tools-v2.4.0.json
    go-modules/
  THIRD_PARTY_NOTICES.md
  RELEASE_MANIFEST.json
  SHA256SUMS
```

Windows 与 Linux 使用不同 `.mcp.json` 和匹配平台的二进制。源插件模板保持 Windows 启动配置；Linux 发布脚本在独立暂存目录中替换它，不修改源模板。

## 可复现性边界

Go 二进制会在同一发布运行中构建两次并逐字节比较。ZIP 固定文件顺序、时间戳、权限与压缩参数；相同输入和相同 Python/zlib 工具链应得到相同 SHA-256。不同平台包本来就包含不同二进制与 launcher，不要求 Windows ZIP 与 Linux ZIP 相同。

WhiteboxTools 官方压缩包中的全部暂存文件都会进入 `RELEASE_MANIFEST.json` 和 `SHA256SUMS`。主程序和下载压缩包还必须分别匹配预先固定的 SHA-256；因此旧 sidecar、被替换的可执行文件或未列入清单的缓存不能静默进入候选包。

## 正式 `0.4.0` 前仍需人工确认

- 选择并加入 ck3-index 项目自身许可证。
- 在 `CHANGELOG.md` 新建正式 `0.4.0` 条目并保留新的空 `Unreleased`。
- 在干净签出上分别构建 Windows x64 与 Linux x64 包。
- 复核两个 `RELEASE_MANIFEST.json` 的 `release_ready=true`，验证 `SHA256SUMS`，并记录最终 ZIP SHA-256。
- 完成隔离环境安装和 MCP 冒烟复测后，再进行公开上传。
