# 搜索正文压缩

`ck3_search` 默认提供紧凑、可无损还原的 JSON 正文。查询算法、命中数量、排序、搜索范围和 `structuredContent` 的对象数组均不变。此改动不需要重建索引。

## 客户端用法

- 普通模型调用：照常传 `query` 或 `queries`，默认 `format="compact"`。
- 程序读取：继续读取原有 `structuredContent.evidence`、`suggestions`、`batch` 等字段，输出 schema 不变。
- 旧程序若只解析 `content[0].text` 中的对象数组：显式传 `format="json"`，正文恢复旧格式。
- QQ bot 等自建客户端可以仅将完整 `content` 正文送入模型上下文，将 `structuredContent` 留给程序使用。不要把两份内容再次拼接。服务器保留两份表示以兼容 MCP 客户端，无法控制客户端如何注入上下文。

部署需更换 MCP 可执行文件、重启对应服务，并让客户端重新获取工具目录。旧进程不会因源码更新自动切换。

## 紧凑正文契约

只有压缩后更短时，正文才出现 `format="ck3-search-table-v1"`。单个命中、空结果及不适合压缩的数据仍可使用普通 JSON；客户端必须同时接受两种表示。

`evidence`、`suggestions`、`batch` 中的每个集合可以独立变成如下表格。其他字段原样保留。以下仅为合成示例：

```json
{
  "format": "ck3-search-table-v1",
  "intent": "ck3_search",
  "query": "example",
  "summary": "Example search.",
  "evidence": {
    "shared": {"kind": "object", "type": "trait", "source": "game"},
    "columns": ["name", "path", "line", "column", "detail"],
    "paths": ["common/traits/example.txt"],
    "rows": [
      ["example_a", 0, 3, 1, null],
      ["example_b", 0, 9, 1, "example detail"]
    ]
  }
}
```

还原一行时先复制 `shared`，再将每个单元格对应到 `columns` 的同位置字段。`null` 表示该字段缺省。若表格带有 `paths`，`path` 列的整数是该字典的 **零起始索引**。没有 `paths` 时，`path` 单元格保持原始路径字符串。

表格保持原始行顺序。`suggestions` 仍是低置信度候选，不是已确认的 `evidence`；两者永不混合。批量搜索中未命中的词仍有 `batch` 行。完整详情、片段、引号、换行和 Unicode 都用标准 JSON 保留。遇到显式 null、嵌套值或未来不支持的行结构时保留普通对象数组，不猜测其含义。

缓存命中、参数修正和返回大小限制都会保持正文与结构化数据一致。大小限制仍按原有规则截断有序数组并标注 `truncated` 与截断元数据；压缩本身不删除命中或截短文本。

## 2026-09-06 实测

在同一真实索引、同一查询参数下，分别调用 `format=json` 与默认格式，验证 `structuredContent` 完全相同，并将紧凑正文还原后逐字段比较。随后重复调用验证缓存结果一致。全部查询使用公开来源过滤。原始游戏与 Mod 内容未收入仓库。

token 计数使用 `tiktoken 0.14.0` 的 `o200k_base`；这是可复现的 tokenizer 计数，不代表特定模型的实际计费，也没有测量模型推理延迟。

| 查询 | 命中数 | 原正文 token | 紧凑正文 token | 正文减少 | 正文与结构化副本合计减少 |
|---|---:|---:|---:|---:|---:|
| `knight` | 8 | 473 | 342 | 27.7% | 13.8% |
| 批量 `brave / diligent / patient` | 24 | 1449 | 852 | 41.2% | 20.6% |
| 中文本地化 `骑士` | 8 | 467 | 355 | 24.0% | 12.0% |
| `tradition`，limit=20 | 20 | 1139 | 760 | 33.3% | 16.6% |
| `knight`，page=2 | 8 | 484 | 284 | 41.3% | 20.7% |
| 不存在的标识符 | 0 | 193 | 193 | 0.0% | 0.0% |
| `add_gold`，script_text，game 来源 | 8 | 516 | 323 | 37.4% | 18.7% |

“合计”将正文和单独编码的结构化数据相加，不包含客户端封装、工具目录与模型隐藏开销。压缩的 token 效果取决于重复字段、路径和实际片段长度，不能将表中比例当作每次查询的保证。

合成回归样本的完整 MCP JSON 包（含结构化副本和运行时元数据）分别从 4518 → 3956、9480 → 7602、5112 → 4589 字节；对应的结构化数据字节数与 8 / 20 / 8 条命中保持不变。精确字节基线记录在 `internal/mcpserver/testdata/response_size.golden.json`。

回归验证覆盖无损还原、格式兼容、中文与特殊字符、稀疏字段、未来字段回退、公开来源过滤、低置信度候选、批量无命中词、缓存参数提示和响应截断。

本地合成样本的正文编码微基准：8 行普通 JSON / 紧凑正文为 7.3 / 28.5 微秒，24 行为 27.2 / 72.0 微秒。表格构建需要额外 CPU，但这组样本的总编码耗时低于 0.1 毫秒。微基准没有测量数据库查询、客户端传输或模型推理；不能将 token 降幅当作端到端延迟降幅。可用 `go test ./internal/mcpserver -run '^$' -bench BenchmarkSearchTextEncoding -benchmem` 复测。
