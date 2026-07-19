# AzgaarToCK3 吸收记录

## 上游快照

- 仓库：`MnTronslien/AzgaarToCK3`
- 分支：`stable`
- 审阅提交：`5c41484fecc58fc23f66a1c92544861c47f42278`
- 许可证：MIT
- 重点文件：`ProvinceImageValidator.cs`、`RiverImageValidator.cs`、`RiverActualPixelValidator.cs`、`docs/RELEASE_SMOKE.md`

## 已吸收的独有能力

- 新增只读 MCP 工具 `map_asset_audit`，支持 `summary`、`provinces`、`rivers` 三种审计视图。
- 交叉检查 `definition.csv` 与 `provinces.png`：重复 ID/颜色、非法行、黑色/未定义像素、没有像素的省份定义和带 Alpha 的错误 PNG 格式。
- 检查 `rivers.png` 必须为 8 位索引 PNG，并按 CK3 实际语义校验 0–15、254、255 的调色板位置。
- 统计河流主体、源点、汇合点与分流点的四邻域拓扑异常，并以 warning 返回坐标样本。
- CLI 同步提供 `ck3-index map audit [summary|provinces|rivers]`。

## 已吸收的打包编排思想

- 参考 `ModDescriptorWriter`、writer 顺序编排和运行清单的职责划分，在 Go 中重新实现单一打包核心，而不是移植其地图转换代码。
- 从同一份规范化元数据生成包内 `descriptor.mod` 与包外 `<slug>.mod`，并为每个成品生成排序、可校验、无本机信息的确定性清单。
- 采用 artifact 根目录内唯一暂存区、全部闸门通过后原子发布的方式；失败只清理本次暂存区，不清空用户工程或整个输出目录。
- 未吸收上游绝对路径写法、直接清空目标目录、面向总转换的固定 `replace_path`，也未吸收文化、宗教、地形、法理或地图生成算法。

## 明确排除的重复或不可靠内容

- 不引入另一套 Clausewitz/Jomini 解析器、头衔树、省份图或地图邻接模型；这些能力继续由 ck3-index 的现有 AST 与地图缓存负责。
- 不把 AzgaarToCK3 的文化、信仰、地形和法理生成启发式当作 CK3 引擎规则。
- 不照搬上游验证器对河流索引 12–15 的一概拒绝。Godherja 当前生效的 `rivers.png` 确实使用索引 12，因此该检查会产生确定的假阳性；ck3-index 允许这些保留索引，但仍校验其调色板位置。
- 不把河流拓扑 warning 升格为必崩错误。Godherja 上游存在少量有意或历史遗留的复杂交汇，工具负责提供坐标证据供人工复核。

## 当前工作区实测

- 当前工程的 9,886 个 `definition.csv` 省份颜色与 `provinces.png` 完整一一覆盖，没有黑色、未定义颜色或缺失像素定义。
- 当前工程的 `rivers.png` 使用正确的 8 位索引格式，但调色板索引 15 为 `#00FF00`，与 CK3/Godherja 的 `#18CE00` 不一致；工具将其报告为 `map_rivers_palette_order` error。
- 当前工程报告 108 个河流拓扑 warning；公开 Godherja 基线报告 47 个。两者都只作为可定位的视觉审查证据，不自动判定为崩溃。
