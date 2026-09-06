# 审查修复与验证（2026-09-06）

审查基线为 `02e03ef84c2418a90c5fe7e93855ef370eec9df2`。本轮先修复报告 A1–A7，再处理两项已能落实的关联风险。以下“通过”指实际执行的测试；未执行的破坏性或长期验证单独列出。

## 修复对应关系

| 审查项 | 修改 | 回归证据 |
| --- | --- | --- |
| A1 基线缓存竞态 | save/clear 在同一事务内递增持久化 `baseline_revision`；缓存键、命中检查、执行后检查、重试和回填资格包含该版本。本地清缓存仅用于回收空间。 | `TestAuditBaselineRevisionAcrossMCPProcesses` 启动真实子进程，通过 MCP 写基线，验证空基线、保存、清除、同名重建和再次覆盖；强制清缓存后回填旧条目。`TestAuditBaselineChangeDuringQueryRetriesBeforeCaching` 用同步 gate 把清除放在查询完成与结果检查之间。 |
| A2 相同文件元数据漏检 | 所有 staged full 的种子来源都启用内容哈希；显式 files 路径也强制读取并哈希。 | `TestAuditRefreshHashesSameMetadataResource` 使用有效 1×1 RGBA DDS，改变像素并恢复原 mtime，分别验证 base 播种和 files 刷新的 SHA，并与 clean 全表语义投影比较。 |
| A3 已提交却返回失败 | 提交前准备引擎规则快照、诊断差异和新索引状态；提交后发布内存快照。收尾使用有时间上限、不会继承请求取消的 context。MCP 重绑定、规则恢复或状态读取失败返回完成事实及 warning，不把旧连接标成新代 ready。 | `TestAuditCancellationAfterCommitReturnsSuccess` 在 full、files、普通 Scan 的提交边界主动取消并验证已提交行。`TestAuditCommittedRefreshStatusFailureIsWarning` 注入无效重绑定目标或关闭连接，验证结果及输出 schema。现有提交前取消测试继续覆盖旧代保留。 |
| A4 files 缺少 vanilla 上下文 | full/files 共用 game 来源选择，向文件任务提供 vanilla on_action 定义。 | `TestAuditOnActionRefreshParityAndDependencyChange` 验证不同块告警保留、相同块不告警，以及只修改 game 时的诊断变化。 |
| A5 旧 lint 缓存被接受 | files 和 refresh status 都检查 `lint_rule_version`；旧版本要求 full。 | `TestAuditFilesRejectsOldLintVersion` 验证无变化 files 也被拒绝，full 后可继续刷新。 |
| A6 LIKE 转义不配套 | 补齐对象、资源、本地化短子串、trigram、语义 FTS 路径过滤的 `ESCAPE`；全仓核对时也修复了 datatype 前缀查询。 | `TestAuditSearchLiteralPathPrefixes` 直接执行受影响查询，覆盖 `_`、`%`、反斜杠及相似目录，避免上层精确查询掩盖回退错误；`TestAuditDatatypeLiteralPrefix` 验证 datatype 前缀。 |
| A7 计时名称误导 | 新主字段为 `read_hash/parse/lint/extract_worker_elapsed_sum`。旧 `*_worker_cpu_total` 暂时作为相同值的兼容别名保留。 | `TestAuditWriterDurabilityAndTimingSemantics` 验证字段与别名。 |

这些 worker 数值是各文件任务的**经过时间之和**，包含 I/O 等待和调度；并行重叠时可超过总耗时。它们不是线程或进程 CPU time，不能据此估计 CPU 利用率，也不能与各阶段简单相加。定位 CPU 热点应使用 CPU profile。此前报告的整体 wall time 仍是当时运行记录，不能当成本版本的性能承诺。

## 关联修复与兼容行为

- 原版 on_action 的文件路径及内容形成独立指纹。full 在该依赖变化时重算相关脚本；files 遇到依赖指纹变化要求 full，避免只刷新指定项目文件而遗漏其他消费者。第一次使用新版本刷新旧索引也会补齐该指纹。读取依赖失败会报错，不默默当成“原版没有该块”。
- 专用扫描写连接默认使用 `synchronous=FULL`，覆盖活动库 files/普通 Scan；只有明确标记为可丢弃 stage 的扫描采用 `OFF`。测试在实际连接及事务内读回 PRAGMA，并验证连接复用后恢复 FULL。原有 stage 完整 checkpoint、文件同步、原子指针发布流程保留。此项验证不等价于真实断电实验。
- 修复 Ubuntu 测试假设：发布成功可以解除旧文件名。验证旧代内容时保留已打开的文件句柄，比较同一文件对象，不要求旧路径在发布后仍然存在。此前 Ubuntu 失败发生在 `TestScanFullStagedDoesNotRewriteLiveDatabaseOrGrowItsWAL`。
- GitHub 复核另外捕获了打包器的跨进程清理竞态：`ReadDir` 枚举后，另一个进程可以移走 stage 或清理过期 ZIP。清理现在只忽略这些位置的“不存在”，权限等真实错误仍会返回。新增 `TestCleanupStageDisappearsAfterEnumeration` 在所有平台确定性模拟 Unix 延迟 stat；本地跨进程打包测试重复十次及原生 packager 全包 race 均通过。
- `ScanStats.committed` 表示该路径确实执行过提交；纯读取的 no-op 可以为 false。MCP 对已完成刷新继续报告结果，附带 `refresh` 统计。收尾无法验证时使用 `refresh_completed_status_unavailable`，`index`、`scan_generation`、`needs_full_scan` 为 null，返回 warning 和重试 status/health 的指引。调用方不要仅因状态读取不可用就重做写操作。
- 基线仍按数据库 anchor 保存，并跨该 anchor 的重建保留；没有偷偷改成按项目路径自动删除。将完全不同的项目复用到同一 anchor 时，应显式管理基线。不同项目宜使用独立数据库 anchor。

## 真实数据性能复核

使用同一份隔离项目、69,321 个文件、25,525,937,628 字节输入，修复前程序从审查 SHA 单独编译。两版均采用 `ck3_native sqlite_fts5`。因工作盘剩余空间不足以安全容纳额外 stage，实验数据库放在临时盘；没有把这组结果与之前工作盘上的历史数据拼成加速比例。两版共用同一隔离库，按顺序交替刷新，未修改正式项目。测试脚本完成后恢复原内容与时间戳。

| 操作 | 审查版本 | 修复版本 |
| --- | --- | --- |
| 无变化完整刷新，三次原始 wall time | 14.022 / 17.849 / 12.086 秒 | 22.372 / 21.704 / 17.660 秒 |
| 小脚本修改刷新，三次中位数 | 2.130 秒 | 2.179 秒 |
| 同一脚本无变化刷新，三次中位数 | 0.298 秒 | 0.323 秒 |

完整刷新每次均哈希全部文件、解析 0 个文件；文件刷新每次哈希 1 个文件，修改时解析 1 个、无变化时解析 0 个。新增内容与诊断依赖校验、完整同步保留后，小文件路径实测增加约 25–49 毫秒。完整刷新本轮测量偏慢，差异主要落在 `seed_staged` 与 `hash_files_wall`；样本量有限且涉及多 GB 备份，**当前证据不能排除完整刷新性能回退，不宣称“无性能损失”或新加速倍数**。另有一组与全仓测试重叠的采样，未计入上表；表中补测在测试进程退出后执行。

实验库首次建立当前索引用时 98.336 秒，属于单次准备记录，不是成对冷建库基准。原始分阶段统计及日志保存在 `cache/audit-fix-20260906/bench-*.json` / `.log`。

## 自动化测试

本地 Windows amd64 执行了默认 SQLite 与 `ck3_native sqlite_fts5` 两套全仓测试。首轮唯一失败是旧 PRAGMA 用例仍断言 `synchronous=OFF`；更新为活动库 FULL 后，原生版全仓复跑全部通过，默认版对应测试及新增回归复跑通过。两种构建的 `go vet ./...` 均通过。原生 race 覆盖新增审查回归、staged full、基线与缓存测试，未发现数据竞争；受控逻辑竞态测试本身也通过。

```text
go test ./... -count=1 -timeout=20m
go test -tags "ck3_native sqlite_fts5" ./... -count=1 -timeout=20m
go vet ./...
go vet -tags "ck3_native sqlite_fts5" ./...
go test -race -tags "ck3_native sqlite_fts5" ./internal/indexer ./internal/mcpserver -run "^TestAudit|^TestScanWriterTransactionUsesPinnedPragmas|^TestScanFullStaged|^TestDiagnosticBaseline|^TestReadToolCache" -count=1
```

本机测试通过已有 `-exec` 包装器统一 Windows 临时路径；包装器不改变测试断言。原始输出保存在被 Git 忽略的 `cache/audit-fix-20260906/`，不上传真实索引和项目内容。

以下边界没有被包装成“已全部验证”：

- `--clean` 遇到连发布身份都无法读取的旧库会安全拒绝。新增测试验证原始坏文件未被改写；没有实现自动丢弃未知旧库及其用户基线。
- 复用健康检查验证的表、行号和计数不能证明任意手工篡改的 FTS postings 与原文逐项相等。现有搜索 oracle 和健康测试继续保留，需要强制重算时使用 clean。
- 本轮未修改 C/SIMD 算法，也未把 Go race 检查当作 C 内存安全证明。ASan/UBSan、长期多进程压力和真实断电/掉盘仍需要单独验证。
- 本轮重建并验证独立可执行文件；运行中的 MCP 服务需要另行重启后才会使用新程序。
