# 单一搜索结果契约

`ck3_search` 只在 `structuredContent` 中返回一份结果，`content` 固定为空数组。没有旧版正文副本、格式开关、共享字段、路径字典或根据结果大小切换的表示。

`evidence`、`suggestions` 和批量查询的 `batch` 均为 `columns` / `rows` 表格。按表头读取每一行即可；`null` 表示字段缺省。来源和路径直接保留为字符串。命中顺序、完整片段、行列号、分页、公开来源过滤和置信度保留。

## 唯一调用方式

请求示例：`{"query":"example"}`。以下响应为合成示例：

```json
{
  "content": [],
  "structuredContent": {
    "intent": "ck3_search",
    "query": "example",
    "summary": "Example search.",
    "evidence": {
      "columns": ["kind", "type", "name", "source", "path", "line", "column"],
      "rows": [
        ["object", "trait", "example_a", "game", "common/traits/example.txt", 3, 1],
        ["object", "trait", "example_b", "game", "common/traits/example.txt", 9, 1]
      ]
    }
  }
}
```

每行长度等于表头长度，字段排列稳定。零命中时 `evidence` 为 `{"columns":[],"rows":[]}`，单条结果也使用表格。没有候选或不是批量查询时，可省略 `suggestions` 或 `batch`。低置信度的 `suggestions` 与已命中的 `evidence` 始终分开，批量无命中的词仍保留其统计行。

## 调用方更新

- 读取 `result.structuredContent`，按各表的 `columns` 读取 `rows`。
- 向模型发送 `JSON.stringify(result.structuredContent)` 一次；不再读取正文作为搜索结果。
- 删除 `format` 参数。服务端拒绝它，不进行旧格式转换。
- 缓存命中和响应大小限制使用同一契约；参数修正提示在结构化结果内。响应截断保留截断计数和分页信息。
- 升级 MCP 可执行文件与客户端后重新载入工具目录。无需重建索引。

[MCP 规范](https://modelcontextprotocol.io/specification/2025-06-18/server/tools#structured-content) 将 `structuredContent` 定义为结构化结果，重复正文的建议用于旧客户端兼容。本接口采用单一结构化结果，输出结构由服务端契约测试校验。

## 2026-09-06 实测

在同一真实索引上通过两个独立 stdio MCP 服务分别运行原版提交 `0e67e3f` 和新版。全部查询使用公开来源过滤。将新表格展开后逐字段核对原版结果，再重复调用检查缓存一致性；证据数量、内容、顺序与已有元数据均一致。游戏、Mod 文件与索引未收入仓库。

计数使用 `tiktoken 0.14.0` / `o200k_base`。原载荷为旧正文与结构化副本之和，新载荷只有结构化结果；不包含工具目录、客户端包装和模型隐藏开销，不等于实际模型计费或推理延迟。

| 查询 | 命中数 | 原载荷 token | 新载荷 token | 减少 |
|---|---:|---:|---:|---:|
| `knight` | 8 | 946 | 380 | 59.8% |
| 批量 `brave / diligent / patient` | 24 | 2898 | 1053 | 63.7% |
| 中文本地化 `骑士` | 8 | 934 | 410 | 56.1% |
| `tradition`，limit=20 | 20 | 2278 | 916 | 59.8% |
| `knight`，page=2 | 8 | 968 | 385 | 60.2% |
| 不存在的标识符 | 0 | 386 | 203 | 47.4% |
| `add_gold`，script_text，game 来源 | 8 | 1032 | 417 | 59.6% |
| `brave`，limit=1 | 1 | 240 | 125 | 47.9% |

若旧客户端之前已只向模型注入一份副本，应与该单份载荷比较，不能套用上述双份基线降幅。

合成样本的完整 MCP 包分别从优化前的 4518 → 1939、9480 → 3540、5112 → 2213 字节，对应 8 / 20 / 8 条命中保持不变。字节基线位于 `internal/mcpserver/testdata/response_size.golden.json`。用 `go test ./internal/mcpserver -run '^$' -bench BenchmarkSearchResultEncoding -benchmem` 可复测表格编码开销。
