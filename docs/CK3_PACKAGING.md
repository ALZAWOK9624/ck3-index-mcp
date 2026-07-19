# CK3 成品打包器

`ck3_package`、`ck3-index package` 与 `ck3-index package-dir` 共用 `internal/packager`，目标是把模型或已有工程交付为可以直接解压到 CK3 `mod` 目录的成品，而不是开发目录快照。

## 输入规格

```json
{
  "metadata": {
    "name": "QQ Example",
    "slug": "qq_example",
    "version": "1.0.0",
    "supported_version": "1.16.*",
    "tags": ["Utilities"],
    "kind": "addon"
  },
  "files": [
    {
      "path": "common/scripted_triggers/qq_example_triggers.txt",
      "content": "qq_example_is_available = { always = yes }\n"
    },
    {
      "path": "gfx/interface/icons/qq_example.dds",
      "content_base64": "<标准 Base64>"
    }
  ]
}
```

`name`、ASCII `slug`、`version`、`supported_version` 和至少一个 `tag` 必填。`kind` 默认为 `addon`；`submod` 必须声明 `dependencies`；只有显式的 `total_conversion` 可以声明 `replace_paths`。每个文件必须且只能提供 `content` 或 `content_base64`。

模型文件入口默认限制为 256 个文件、单文件解码后 8 MiB、总计 32 MiB。MCP 传输层另设 64 MiB JSON 信封上限，以容纳 32 MiB 二进制内容的 Base64 膨胀；这不会提高解码后的打包限额。目录入口使用更高的本地限制，并只收录 CK3 加载目录、`thumbnail.png` 和可兼容重建的描述文件；版本库、缓存、日志、数据库、备份与旧压缩包会被排除并列在结果中。

## 发布闸门

打包前会规范化斜杠、slug 和元数据，拒绝绝对路径、`..`、Windows 保留名、大小写重复路径、符号链接与非法 Base64。本地化 `.yml`/`.yaml` 会自动补 UTF-8 BOM。调用方提交的描述文件若与元数据一致会被规范版本替换；字段冲突则返回 `blocked`。

规范化文件随后进入 ck3-index 的虚拟文件分析与 patch preflight。解析错误、高置信度作用域/引用/本地化/资源/覆盖风险会阻止发布；结果同时给出诊断、缺失项和修正建议。失败不会留下残包。

## 成品与生命周期

成功结果返回 `status=ready`、`artifact_id`、受限的 `artifact_relpath`、ZIP 名称、SHA-256、大小和到期时间，不返回宿主机绝对路径。artifact 默认保留七天；每次成功打包会清理过期 ZIP 和残留暂存目录。

临时区与保留时长由 `ck3-index.toml` 配置；相对路径以配置文件所在目录为基准：

```toml
artifact_root = "cache/artifacts"
artifact_retention_hours = 168
```

ZIP 条目按路径排序并使用固定时间戳。`ck3-package-manifest.json` 记录 schema 版本、规范化元数据、每个成品文件的路径/大小/SHA-256 和验证摘要，但不记录构建时间、用户名或本机路径。相同输入和相同验证结果会产生完全相同的 ZIP 字节。
