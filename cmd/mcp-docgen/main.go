package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ck3-index/internal/mcpserver"
)

const (
	beginMarker = "<!-- BEGIN GENERATED MCP TOOLS -->"
	endMarker   = "<!-- END GENERATED MCP TOOLS -->"
)

func main() {
	root := flag.String("root", ".", "ck3-index repository root")
	check := flag.Bool("check", false, "verify generated files without writing")
	flag.Parse()
	if err := run(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, "mcp-docgen:", err)
		os.Exit(1)
	}
}

func run(root string, check bool) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	canonical := mcpserver.CanonicalToolDocumentation()
	legacy := mcpserver.LegacyToolDocumentation()
	if err := validateChineseCatalog(canonical); err != nil {
		return err
	}
	readmeSection := renderChineseCatalogSection(canonical, legacy)
	skillSection := renderCatalogSection(canonical, legacy)
	readmePath := filepath.Join(root, "README.md")
	skillPath := filepath.Join(root, "skill", "ck3-coding", "SKILL.md")
	pluginSkillPath := filepath.Join(root, "plugin", "ck3-index", "skills", "ck3-coding", "SKILL.md")
	referencePath := filepath.Join(root, "docs", "MCP_TOOL_REFERENCE.md")

	readme, err := replaceSectionFile(readmePath, readmeSection)
	if err != nil {
		return err
	}
	skill, err := replaceSectionFile(skillPath, skillSection)
	if err != nil {
		return err
	}
	outputs := map[string][]byte{
		readmePath:      readme,
		skillPath:       skill,
		pluginSkillPath: skill,
		referencePath:   []byte(renderReference(canonical, legacy)),
	}
	var drift []string
	for path, expected := range outputs {
		current, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(current, expected) {
			continue
		}
		if check {
			drift = append(drift, filepath.ToSlash(path))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, expected, 0644); err != nil {
			return err
		}
	}
	if len(drift) > 0 {
		sort.Strings(drift)
		return fmt.Errorf("generated MCP documentation is stale: %s", strings.Join(drift, ", "))
	}
	return nil
}

func replaceSectionFile(path, generated string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(data)
	start := strings.Index(text, beginMarker)
	end := strings.Index(text, endMarker)
	if start < 0 || end < 0 || end < start {
		return nil, fmt.Errorf("%s lacks generated MCP markers", filepath.ToSlash(path))
	}
	end += len(endMarker)
	replacement := beginMarker + "\n" + strings.TrimSpace(generated) + "\n" + endMarker
	return []byte(text[:start] + replacement + text[end:]), nil
}

func renderCatalogSection(canonical, legacy []mcpserver.ToolDocumentation) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "## MCP Tools (standard: %d; expert: %d)\n\n", len(canonical), len(canonical)+len(legacy))
	builder.WriteString("The standard profile advertises the canonical tools below. The expert profile also advertises deprecated compatibility names; all legacy names remain callable during the compatibility window.\n\n")
	writeToolTable(&builder, "Core Tools", filterTools(canonical, false))
	writeToolTable(&builder, "Map Tools", filterTools(canonical, true))
	builder.WriteString("### Compatibility\n\n")
	builder.WriteString("Set `CK3_INDEX_MCP_PROFILE=expert` only when an existing client still discovers legacy specialist names. New prompts and `next_actions` use canonical names.\n")
	return builder.String()
}

func renderChineseCatalogSection(canonical, legacy []mcpserver.ToolDocumentation) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "## MCP 工具（标准模式：%d；专家模式：%d）\n\n", len(canonical), len(canonical)+len(legacy))
	builder.WriteString("标准模式只公开下列规范工具。专家模式还会公开已弃用的兼容名称；在兼容期内，所有旧名称仍然可以调用。\n\n")
	writeChineseToolTable(&builder, "核心工具", filterTools(canonical, false))
	writeChineseToolTable(&builder, "地图工具", filterTools(canonical, true))
	builder.WriteString("### 兼容模式\n\n")
	builder.WriteString("只有仍需发现旧版专用工具名的客户端才应设置 `CK3_INDEX_MCP_PROFILE=expert`。新提示词与 `next_actions` 一律使用规范工具名。\n")
	return builder.String()
}

func filterTools(tools []mcpserver.ToolDocumentation, maps bool) []mcpserver.ToolDocumentation {
	filtered := make([]mcpserver.ToolDocumentation, 0, len(tools))
	for _, tool := range tools {
		if strings.HasPrefix(tool.Name, "map_") == maps {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func writeToolTable(builder *strings.Builder, title string, tools []mcpserver.ToolDocumentation) {
	fmt.Fprintf(builder, "### %s\n\n", title)
	builder.WriteString("| Tool | Purpose |\n|---|---|\n")
	for _, tool := range tools {
		fmt.Fprintf(builder, "| `%s` | %s |\n", tool.Name, markdownCell(tool.Description))
	}
	builder.WriteString("\n")
}

func writeChineseToolTable(builder *strings.Builder, title string, tools []mcpserver.ToolDocumentation) {
	fmt.Fprintf(builder, "### %s\n\n", title)
	builder.WriteString("| 工具 | 用途 |\n|---|---|\n")
	for _, tool := range tools {
		text := chineseToolTexts[tool.Name]
		fmt.Fprintf(builder, "| `%s` | %s |\n", tool.Name, markdownCell(text.Description))
	}
	builder.WriteString("\n")
}

func renderReference(canonical, legacy []mcpserver.ToolDocumentation) string {
	var builder strings.Builder
	builder.WriteString("# ck3-index MCP 工具参考\n\n")
	builder.WriteString("> 本文档由 `go run ./cmd/mcp-docgen` 根据 `internal/mcpserver` 自动生成，请勿手工修改。\n\n")
	fmt.Fprintf(&builder, "标准模式公开 %d 个规范工具。专家模式另外公开 %d 个已弃用的发现别名；即使这些别名未显示，兼容期内仍然可以调用。\n\n", len(canonical), len(legacy))
	for _, tool := range canonical {
		text := chineseToolTexts[tool.Name]
		fmt.Fprintf(&builder, "## `%s` — %s\n\n%s\n\n", tool.Name, text.Title, text.Description)
		writeInputFields(&builder, tool.InputSchema)
		if tool.Name == "ck3_package" || tool.Name == "map_migration_snapshot" || tool.Name == "map_province_migration" {
			builder.WriteString("属性：生成受限临时产物、非破坏、封闭世界。输出：结构化对象与 JSON 文本；成功时返回可供附件发送层解析的 artifact 标识和相对路径。\n\n")
		} else {
			builder.WriteString("属性：只读、非破坏、封闭世界。输出：结构化对象与 JSON 文本内容；`map_render` 还会返回 PNG 图像内容。\n\n")
		}
	}
	builder.WriteString("## 已弃用的专家模式别名\n\n")
	builder.WriteString("| 旧名称 | 规范替代工具 |\n|---|---|\n")
	for _, tool := range legacy {
		fmt.Fprintf(&builder, "| `%s` | `%s` |\n", tool.Name, tool.Canonical)
	}
	return builder.String()
}

func writeInputFields(builder *strings.Builder, schema map[string]any) {
	properties, _ := schema["properties"].(map[string]any)
	required := map[string]bool{}
	for _, name := range stringSlice(schema["required"]) {
		required[name] = true
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		builder.WriteString("输入参数：无。\n\n")
		return
	}
	builder.WriteString("| 参数 | 必填 | 类型 | 约束 | 说明 |\n|---|---:|---|---|---|\n")
	for _, name := range names {
		property, _ := properties[name].(map[string]any)
		constraint := propertyConstraints(property)
		description := chineseFieldDescriptions[schemaText(property, "description")]
		if description == "" {
			description = "—"
		}
		fmt.Fprintf(builder, "| `%s` | %s | %s | %s | %s |\n", name, chineseBool(required[name]), markdownCell(chineseSchemaType(property)), markdownCell(constraint), markdownCell(description))
	}
	builder.WriteString("\n")
}

func schemaText(property map[string]any, key string) string {
	value, ok := property[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func propertyConstraints(property map[string]any) string {
	var parts []string
	if value, ok := property["enum"]; ok {
		parts = append(parts, "可选值="+fmt.Sprint(value))
	}
	labels := map[string]string{
		"minimum": "最小值", "maximum": "最大值", "minLength": "最短长度", "maxLength": "最长长度",
		"minItems": "最少项数", "maxItems": "最多项数", "maxProperties": "最多字段数", "default": "默认值",
	}
	for _, key := range []string{"minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "maxProperties", "default"} {
		if value, ok := property[key]; ok {
			parts = append(parts, labels[key]+"="+fmt.Sprint(value))
		}
	}
	return strings.Join(parts, "; ")
}

func stringSlice(value any) []string {
	switch items := value.(type) {
	case []string:
		return items
	case []any:
		result := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}
