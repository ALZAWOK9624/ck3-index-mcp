# 无数据库的 CK3 DSL 校验与原版规则审查

`ck3-check` 接收完整的拟议文件文本，直接解析、检查静态合同并返回 JSON。
不需要 `ck3-index.toml`、SQLite、已有索引或 Mod 目录。
它也可作为只提供检查与规则查询的独立 MCP 服务，供 QQ bot 使用。

## 构建与调用

```sh
go build -o ck3-check ./cmd/ck3-check
go build -o dsl-audit ./cmd/dsl-audit
```

Windows 可将输出文件名改为 `ck3-check.exe`。
完整的 `ck3-index` 二进制也提供相同的 `check` 子命令。
`ck3-index check` 在读取配置之前分派，因而同样不依赖数据库。

向标准输入写入一个 JSON 请求：

```json
{
  "files": [
    {
      "path": "common/scripted_triggers/bot_proposal.txt",
      "content": "bot_proposal = { AND = { add_gold = 5 } }"
    }
  ],
  "limit": 100
}
```

这个请求返回 `effect_in_trigger`、`passed: false`、`database_used: false`。
将效果放进条件的错误即使位于嵌套 `AND` 内，也会被发现。
CLI 正常且无错误时退出码为 0；检查发现错误或请求失败时为 1。
检查结果在 stdout，请求/执行错误在 stderr。警告不自动成为错误；bot 必须阅读它们。

`--rule add_gold` 查询一个精确命令的效果/条件类别、输入作用域和版本化用法文档。
默认规则来自编译进程序的 CK3 1.19 快照。管理员可在启动时传入
`--engine-logs /path/to/engine/logs`，获得独立、不可变的日志规则快照。
日志路径不会成为 MCP 参数，也不会由客户端任意指定。
显式指定的日志若缺失或不可读，启动失败，不能悄悄退回默认规则。

## QQ bot 接入

以 `ck3-check --serve` 启动 MCP stdio 服务。客户端配置示意：

```json
{
  "mcpServers": {
    "ck3-syntax": {
      "command": "ck3-check.exe",
      "args": ["--serve"]
    }
  }
}
```

`command` 使用管理员安装位置或 PATH 中的程序名。服务只暴露两个工具：

| 工具 | 输入 | 作用 |
|---|---|---|
| `ck3_check` | `files: [{path, content}]`，可选 `syntax_only`、`limit` | 检查拟议的完整文本 |
| `ck3_check_rules` | `key` | 查询一个精确命令的文档事实 |

推荐给 bot 的约束：生成代码后，先把完整文件提交给 `ck3_check`；有 error 就修复再检查；
检查 warning 与 `coverage`，未知引用、动态作用域、逻辑与运行效果不能宣称已验证。
需要引用与覆盖分析时，再在具有适当访问权限的环境中使用索引服务。

这里的 `path` 是语言选择提示，例如 `events/proposal.txt`，并不是本机文件读取请求。
绝对路径、`..`、不支持的扩展名、重复虚拟路径、额外 JSON 字段会被拒绝。
不存在读取文件、运行 shell、刷新数据库、修改 Mod 或访问私人项目的工具。
服务同时只执行一个检查；额外并发检查得到明确的 busy 响应，取消与 ping 仍可处理。
这份交付提供服务与接入配置示例，不会改写现有 bot 的配置或重启现有服务。

## 返回值的正确解读

`passed` 只表示本次静态检查没有 error；不是 CK3 官方编译器的认可。
每个文件都报告覆盖情况：

- `syntax: checked`：按该扩展名的解析器检查；达到保护上限时为 `limited`。
- `semantics: partial_static_contracts`：执行当前已实现的运行时字段、命令上下文和可证明作用域检查。
- 有解析错误时跳过语义检查，避免基于破损树继续给出假结论。
- `references: not_checked`、`runtime: not_checked`：未解析对象、本地化、资源引用，未执行引擎。
- `diagnostic_count` 在输出裁剪前计算；`truncated` 不会隐藏错误总数或把失败变成通过。

局限包括：自定义 scripted effect/trigger 的参数展开、动态 `scope:`、文件间覆盖关系、
Mod 提供的新类型、全部引擎加载器语法、运行动作的副作用。未知命令不会仅因不在表中就判为非法。
`ck3_check_rules` 的用法文档也不是穷尽的参数模式；其文档版本与可选实时作用域指纹分别标明。

每请求最多 64 文件，单文件 32 MiB、总文本 64 MiB；CLI JSON envelope 128 MiB，
MCP 沿用 64 MiB 消息上限。解析树深度最多 256，节点最多 500,000，解析错误报告最多 1,000。
保护上限是工具的资源限制，不宣称是游戏本身的语法上限。

## DSL 结构定义

这一层保留语法形状，不把出现次数当成合法性证明：

```ebnf
document   = { statement } ;
statement  = scalar, operator, value
           | scalar, block
           | scalar
           | block ;
value      = scalar | operator | block | color_tag, block ;
block      = "{", { statement }, "}" ;
operator   = "=" | "?=" | "!=" | "==" | "<" | "<=" | ">" | ">=" ;
color_tag  = "rgb" | "hsv" | "hsv360" ;
scalar     = identifier | quoted_string | arithmetic_expression ;
```

标识符可携带类型前缀、日期、数字、`@变量`、`$参数$` 等。`#` 注释在字符串外生效。
字符串保留正确的源文本范围，转义后字符数不会错误缩短末尾位置。
`@[ ... ]` 算术表达式作为一个值保存，支持嵌套方括号；这里只检查封闭性，不求值。
重复 key、裸值列表、匿名嵌套块都保留顺序，不能用普通字典覆盖掉。
`OPERATOR = <=` 这种脚本参数仍可携带操作符值。

GUI 单独增加 `types Namespace {}`、`type child = parent {}`、`template`、
`local_template`、`block`、`blockoverride`，以及继承/实例化形式。
这些扩展通过 `.gui` 模式分派，拟议文件审查与实际扫描使用同一种 GUI 解析器。
GUI 不执行数据绑定表达式；图形着色器不是 PDX DSL，不送入此解析器。

`rgb/hsv/hsv360` 节点现在同时保留标签和分量，而非拆成相邻节点。
纹章颜色消费方支持新形式及旧存储树的兼容形式。
多余的顶层 `}` 报错后继续解析后续文件内容；未闭合块在起始位置报错；
缺值时不吞掉父块的 `}`。普通脚本的连写等号不能再借 GUI 语法被错误吞掉。

命令上下文检查使用完整生成表或显式日志快照，覆盖 scripted effect/trigger 根、
布尔容器、已知作用域和迭代器。`switch` 标签、`random_list` 权重、迭代器元数据
与未知 helper 的参数块按其容器处理，不能仅凭字段名与命令重名就误报。

本地化使用专用行语法。完整文件需要语言头，key 允许原版实际使用的撇号；
索引提取也同步修复了这类 key 的遗漏。旧原版历史注释中的非 UTF-8 字节不参与语法判断，
实际执行 token 中的坏编码与 NUL 仍会被检查工具拒绝。

## 全量原版审查的复现

```sh
dsl-audit --root /path/to/CK3/game --engine-logs /path/to/engine/logs > vanilla.json
python tools/summarize_dsl_audit.py vanilla.json docs/DSL_CORPUS_20260906.json
```

审查原版加载目录内所有支持的文本：`common/events/history/gui/localization/gfx/map_data/sound/music/notifications`。
`.info` 教学文件独立提取字段证据，不能把文档中的占位符当作可执行脚本。
报告包括文本与文档的内容指纹、规则指纹、每目录文件/节点计数、命令数量、
直接定义字段的形状及原版位置、所有诊断类别及有界实例。
不会联网或写入原版文件。CSV、着色器、二进制资源和不支持的格式明确在覆盖范围外。

发布的 [语料报告](DSL_CORPUS_20260906.json) 保留各目录统计以及 common/events 的字段证据，
完整 JSON 可在本地重新生成。文件名、key、计数与位置不包含原版文件正文或本机绝对路径。
“出现过某字段”只是学习资料，不自动生成未知字段黑名单。

### 2026-09-06 实际语料结果

- 17,449 个可执行/本地化文本文件，465,825,128 字节，6,332,894 个脚本节点。
- 164 份 `.info` 教学文件，提取 2,700 条字段证据；26 份没有可抽取的字段行，空列表不是完整语法证明。
- 2,614 个目录的覆盖统计，发布 common/events 的 16,535 个目录内字段观察条目。
- 当前日志记录 1,905 个效果、1,784 个条件、231 个 target、879 个 on_action。
- 逐项比较已编译的效果/条件与这份日志的可表示输入作用域，没有报告差异。
  39 个编译表中的 target 前缀/内建符号未出现在日志的输入作用域字典，保留为 `compiled_only`，不据此删除或判断非法。
- 原版静态检查留下 4 个 error 和 2 个 warning。2 份 `.asset` 的多余/缺失括号贡献 3 条 error；
  原版一个 `limit` 内使用 `save_scope_as`，与其引擎“effect”分类冲突，贡献另 1 条 error；
  另外 2 条作用域 warning 保留实例。它们是静态审查线索，不代表已在游戏中复现故障。
- 全量扫描曾暴露 105 条命令上下文误报和 18 条本地化撇号 key 误报；修正容器分类、颜色结构和 key 正则后重新扫描，相关误报均消失。

语料指纹为 `e021f8bd450ddd083b7d51e10bd93932f1287e1731dc8e323892785d84b73a18`；
日志规则指纹为 `d84faf80e32e07b0b5ae9746d6f726c2d4fcf644d0a0fba169bf2383c9460f5f`。
这些是明确的快照边界。工作区另有标为 1.18.4 的旧日志，本轮没有用它替换编译表。

独立二进制在一个只有故意损坏配置文件、没有数据库的目录中完成 CLI 与 MCP 冒烟检查，
没有创建额外文件。两次 CLI 启动至返回分别约 102 ms 和 39 ms，仅作为本机小文件样例，
不是大型项目速度或并发吞吐承诺。

## 索引兼容与验证

更新了 lint 合同版本。已有索引需要一次完整刷新以重新解析脚本与本地化，
更新节点形状、诊断和此前漏掉的本地化 key；之后恢复正常增量流程。
独立检查服务不受这次索引升级影响。

新增回归覆盖：残缺括号与恢复、错误运算符、GUI、颜色记法与源范围、深度限制、
无配置/数据库的 CLI 与 MCP、规则隔离、路径与容量边界、诊断裁剪、文档指纹和原版撇号 key。
`FuzzParseStructural` 可进一步进行有界解析器 fuzz；全量原版语料用于审计误报，
恶意/错误输入与既有测试用于发现漏报，不能只用“原版全部通过”衡量正确性。
