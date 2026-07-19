package indexer

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	GUIPreviewLanguageRaw         = "raw"
	GUIPreviewLanguageEnglish     = "english"
	GUIPreviewLanguageSimpChinese = "simp_chinese"
	GUIPreviewLanguageBilingual   = "bilingual"

	guiLocalizationValueMaxRunes = 2048
	guiLocalizationDisplayMax    = 768
	guiLocalizationNestedMaxKeys = 256
	guiLocalizationNestedDepth   = 4
)

// GUIPreviewLocalizationStats summarizes indexed localization attached to a
// preview. A binding is counted per GUI property, so one key used by both text
// and tooltip fields contributes two bindings.
type GUIPreviewLocalizationStats struct {
	Bindings  int `json:"bindings"`
	Resolved  int `json:"resolved"`
	Bilingual int `json:"bilingual"`
	Partial   int `json:"partial"`
	Missing   int `json:"missing"`
	Truncated int `json:"truncated"`
}

// GUILocalizedText is a path-redacted, bounded localization binding. Values
// are observed index facts; DisplayText is a conservative formatting-only
// projection and never evaluates Jomini expressions.
type GUILocalizedText struct {
	Key              string             `json:"key"`
	SelectedLanguage string             `json:"selected_language"`
	SelectedText     string             `json:"selected_text"`
	English          *GUILocalizedValue `json:"english,omitempty"`
	SimpChinese      *GUILocalizedValue `json:"simp_chinese,omitempty"`
	Partial          bool               `json:"partial,omitempty"`
}

type GUILocalizedValue struct {
	Language      string `json:"language"`
	Value         string `json:"value"`
	ResolvedValue string `json:"resolved_value,omitempty"`
	DisplayText   string `json:"display_text"`
	Source        string `json:"source"`
	Dynamic       bool   `json:"dynamic,omitempty"`
	Truncated     bool   `json:"truncated,omitempty"`

	// runtimeLookup is the bounded, active-index localization closure for this
	// language. It is intentionally omitted from JSON; the runtime text
	// compiler only uses it to resolve static branches of SelectLocalization
	// and AddLocalizationIf before emitting an inert browser plan.
	runtimeLookup map[string]string
}

type guiLocalizationRow struct {
	key, language, value, source, path string
	rank                               int
}

var (
	guiLocalizationKeyRE      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,159}$`)
	guiLocalizationSimpleLink = regexp.MustCompile(`\[([[:alnum:]_.-]+)\|[[:alnum:]_.-]+\]`)
	guiLocalizationBracketKey = regexp.MustCompile(`\[([A-Za-z_][A-Za-z0-9_.-]{0,159})(?:\|[A-Za-z0-9_.-]+)?\]`)
	guiLocalizationMacroKey   = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_.-]{0,159})\$`)
	guiLocalizationColorTag   = regexp.MustCompile(`#[Cc][Oo][Ll][Oo][Rr]:\{[^}\r\n]{1,128}\}`)
	guiLocalizationFormat     = regexp.MustCompile(`#[[:alnum:]_.-]+|#!`)
	guiLocalizationIcon       = regexp.MustCompile(`@[[:alnum:]_.-]+!`)
	guiLocalizationRuntime    = regexp.MustCompile(`\[[^\]]+\]|\$[^$]+\$`)
	guiLocalizationSpace      = regexp.MustCompile(`[ \t]+`)
	guiLocalizationNewlines   = regexp.MustCompile(`\n{3,}`)
)

func normalizeGUIPreviewLanguage(value string) (string, error) {
	language := strings.ToLower(strings.TrimSpace(value))
	if language == "" {
		language = GUIPreviewLanguageRaw
	}
	switch language {
	case GUIPreviewLanguageRaw, GUIPreviewLanguageEnglish, GUIPreviewLanguageSimpChinese, GUIPreviewLanguageBilingual:
		return language, nil
	default:
		return "", fmt.Errorf("GUI preview language %q is invalid; expected raw, english, simp_chinese, or bilingual", value)
	}
}

func (db *DB) bindGUIPreviewLocalization(ctx context.Context, preview *GUIPreviewResult, language string, allowProject bool) error {
	if preview == nil {
		return nil
	}
	language, err := normalizeGUIPreviewLanguage(language)
	if err != nil {
		return err
	}
	preview.Language = language

	keys := map[string]struct{}{}
	for index := range preview.Nodes {
		node := &preview.Nodes[index]
		if key, ok := guiPreviewLocalizationKey(node.Text); ok {
			keys[key] = struct{}{}
		}
		for key := range guiConditionalLocalizationKeys(node.Text) {
			keys[key] = struct{}{}
		}
		if node.Semantics != nil {
			if key, ok := guiPreviewLocalizationKey(node.Semantics.Tooltip); ok {
				keys[key] = struct{}{}
			}
			for key := range guiConditionalLocalizationKeys(node.Semantics.RawText) {
				keys[key] = struct{}{}
			}
			for key := range guiConditionalLocalizationKeys(node.Semantics.Tooltip) {
				keys[key] = struct{}{}
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}

	rows, err := db.queryGUIPreviewLocalizationClosure(ctx, keys, allowProject)
	if err != nil {
		return err
	}
	preview.runtimeLocalizationLookups = buildGUIPreviewRuntimeLocalizationLookups(buildGUIPreviewLocalizationValueLookup(rows))
	bindings := buildGUIPreviewLocalizationBindings(rows, language)
	for index := range preview.Nodes {
		node := &preview.Nodes[index]
		if key, ok := guiPreviewLocalizationKey(node.Text); ok {
			if binding := cloneGUILocalizedText(bindings[key]); binding != nil {
				preview.Localization.Bindings++
				node.TextLocalization = binding
				preview.recordGUIPreviewLocalization(binding)
			}
		}
		if node.Semantics != nil {
			if key, ok := guiPreviewLocalizationKey(node.Semantics.Tooltip); ok {
				if binding := cloneGUILocalizedText(bindings[key]); binding != nil {
					preview.Localization.Bindings++
					node.TooltipLocalization = binding
					preview.recordGUIPreviewLocalization(binding)
				}
			}
		}
	}
	if preview.Localization.Partial > 0 {
		preview.Warnings = append(preview.Warnings, fmt.Sprintf("%d localized GUI field(s) contain runtime expressions; the browser shows a formatting-only partial value", preview.Localization.Partial))
	}
	if language != GUIPreviewLanguageRaw && preview.Localization.Missing > 0 {
		preview.Warnings = append(preview.Warnings, fmt.Sprintf("%d GUI localization field(s) have no indexed value and remain as raw keys", preview.Localization.Missing))
	}
	return nil
}

func (db *DB) queryGUIPreviewLocalizationClosure(ctx context.Context, keys map[string]struct{}, allowProject bool) ([]guiLocalizationRow, error) {
	queried := make(map[string]struct{}, minInt(len(keys), guiLocalizationNestedMaxKeys))
	pending := make(map[string]struct{}, len(keys))
	for key := range keys {
		if len(queried) >= guiLocalizationNestedMaxKeys {
			break
		}
		queried[key] = struct{}{}
		pending[key] = struct{}{}
	}
	var result []guiLocalizationRow
	for depth := 0; depth < guiLocalizationNestedDepth && len(pending) > 0; depth++ {
		rows, err := db.queryGUIPreviewLocalizations(ctx, pending, allowProject)
		if err != nil {
			return nil, err
		}
		result = append(result, rows...)
		next := map[string]struct{}{}
		for _, row := range rows {
			for key := range guiLocalizationNestedKeys(row.value) {
				if _, exists := queried[key]; exists || len(queried) >= guiLocalizationNestedMaxKeys {
					continue
				}
				queried[key] = struct{}{}
				next[key] = struct{}{}
			}
		}
		pending = next
	}
	return result, nil
}

func guiLocalizationNestedKeys(value string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, match := range guiLocalizationBracketKey.FindAllStringSubmatch(value, -1) {
		if len(match) > 1 && guiLocalizationKeyRE.MatchString(match[1]) {
			result[match[1]] = struct{}{}
		}
	}
	for _, match := range guiLocalizationMacroKey.FindAllStringSubmatch(value, -1) {
		if len(match) > 1 && guiLocalizationKeyRE.MatchString(match[1]) {
			result[match[1]] = struct{}{}
		}
	}
	for key := range guiConditionalLocalizationKeys(value) {
		result[key] = struct{}{}
	}
	return result
}

func guiConditionalLocalizationKeys(value string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, marker := range guiLocalizationRuntime.FindAllString(value, -1) {
		if len(marker) < 3 || marker[0] != '[' || marker[len(marker)-1] != ']' {
			continue
		}
		collectGUIConditionalLocalizationKeys(strings.TrimSpace(marker[1:len(marker)-1]), result, 0)
	}
	return result
}

func collectGUIConditionalLocalizationKeys(expression string, result map[string]struct{}, depth int) {
	if depth >= guiLocalizationNestedDepth || len(result) >= guiLocalizationNestedMaxKeys {
		return
	}
	name, args, isCall, err := splitGUIRuntimeCall(expression)
	if err != nil || !isCall {
		return
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "selectlocalization":
		if len(args) == 3 {
			addGUIConditionalLocalizationKey(args[1], result)
			addGUIConditionalLocalizationKey(args[2], result)
		}
	case "addlocalizationif":
		if len(args) == 2 {
			addGUIConditionalLocalizationKey(args[1], result)
		}
	}
	for _, argument := range args {
		collectGUIConditionalLocalizationKeys(argument, result, depth+1)
	}
}

func addGUIConditionalLocalizationKey(expression string, result map[string]struct{}) {
	literal, ok := parseGUIRuntimeLiteral(expression)
	if !ok || literal.kind != guiRuntimeKindString || !guiLocalizationKeyRE.MatchString(literal.text) {
		return
	}
	result[literal.text] = struct{}{}
}

func (preview *GUIPreviewResult) recordGUIPreviewLocalization(binding *GUILocalizedText) {
	preview.Localization.Resolved++
	if binding.English != nil && binding.SimpChinese != nil {
		preview.Localization.Bilingual++
	}
	if binding.Partial {
		preview.Localization.Partial++
	}
	if (binding.English != nil && binding.English.Truncated) || (binding.SimpChinese != nil && binding.SimpChinese.Truncated) {
		preview.Localization.Truncated++
	}
	switch preview.Language {
	case GUIPreviewLanguageEnglish:
		if binding.English == nil {
			preview.Localization.Missing++
		}
	case GUIPreviewLanguageSimpChinese:
		if binding.SimpChinese == nil {
			preview.Localization.Missing++
		}
	case GUIPreviewLanguageBilingual:
		if binding.English == nil || binding.SimpChinese == nil {
			preview.Localization.Missing++
		}
	}
}

func guiPreviewLocalizationKey(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return "", false
	}
	if !guiLocalizationKeyRE.MatchString(value) {
		return "", false
	}
	return value, true
}

func (db *DB) queryGUIPreviewLocalizations(ctx context.Context, keys map[string]struct{}, allowProject bool) ([]guiLocalizationRow, error) {
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	result := make([]guiLocalizationRow, 0, len(ordered)*2)
	const batchSize = 160
	for start := 0; start < len(ordered); start += batchSize {
		end := minInt(start+batchSize, len(ordered))
		placeholders := make([]string, end-start)
		args := make([]any, 0, end-start)
		for index, key := range ordered[start:end] {
			placeholders[index] = "?"
			args = append(args, key)
		}
		query := `SELECT l.key,l.language,l.value,l.source_name,l.source_rank,l.path
			FROM localization l JOIN files f ON f.id=l.file_id
			WHERE f.overridden=0 AND l.key IN (` + strings.Join(placeholders, ",") + `)`
		if !allowProject {
			query += ` AND l.source_rank>1`
		}
		query += ` ORDER BY l.key,l.source_rank,l.replace_dir DESC,l.path`
		rows, err := db.sql.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var row guiLocalizationRow
			if err := rows.Scan(&row.key, &row.language, &row.value, &row.source, &row.rank, &row.path); err != nil {
				rows.Close()
				return nil, err
			}
			result = append(result, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func buildGUIPreviewLocalizationBindings(rows []guiLocalizationRow, selectedLanguage string) map[string]*GUILocalizedText {
	bindings := map[string]*GUILocalizedText{}
	values := buildGUIPreviewLocalizationValueLookup(rows)
	runtimeLookups := buildGUIPreviewRuntimeLocalizationLookups(values)
	for _, row := range rows {
		language := canonicalGUIPreviewLocalizationLanguage(row.language, row.path)
		if language != GUIPreviewLanguageEnglish && language != GUIPreviewLanguageSimpChinese {
			continue
		}
		binding := bindings[row.key]
		if binding == nil {
			binding = &GUILocalizedText{Key: row.key, SelectedLanguage: selectedLanguage}
			bindings[row.key] = binding
		}
		resolved := expandGUIPreviewLocalizationValue(row.value, language, values, 0, map[string]bool{})
		value := makeGUILocalizedValue(language, row.value, resolved, row.source, runtimeLookups[language])
		if language == GUIPreviewLanguageEnglish && binding.English == nil {
			binding.English = value
		}
		if language == GUIPreviewLanguageSimpChinese && binding.SimpChinese == nil {
			binding.SimpChinese = value
		}
	}
	for key, binding := range bindings {
		binding.Partial = (binding.English != nil && binding.English.Dynamic) || (binding.SimpChinese != nil && binding.SimpChinese.Dynamic)
		binding.SelectedText = selectGUIPreviewLocalizedText(binding, selectedLanguage)
		if binding.English == nil && binding.SimpChinese == nil {
			delete(bindings, key)
		}
	}
	return bindings
}

func buildGUIPreviewRuntimeLocalizationLookups(values map[string]map[string]string) map[string]map[string]string {
	result := map[string]map[string]string{
		GUIPreviewLanguageEnglish:     {},
		GUIPreviewLanguageSimpChinese: {},
	}
	for key, byLanguage := range values {
		for language, value := range byLanguage {
			if language != GUIPreviewLanguageEnglish && language != GUIPreviewLanguageSimpChinese {
				continue
			}
			result[language][key] = expandGUIPreviewLocalizationValue(value, language, values, 0, map[string]bool{key: true})
		}
	}
	return result
}

func buildGUIPreviewLocalizationValueLookup(rows []guiLocalizationRow) map[string]map[string]string {
	result := map[string]map[string]string{}
	for _, row := range rows {
		language := canonicalGUIPreviewLocalizationLanguage(row.language, row.path)
		if language != GUIPreviewLanguageEnglish && language != GUIPreviewLanguageSimpChinese {
			continue
		}
		byLanguage := result[row.key]
		if byLanguage == nil {
			byLanguage = map[string]string{}
			result[row.key] = byLanguage
		}
		if _, exists := byLanguage[language]; !exists {
			byLanguage[language] = row.value
		}
	}
	return result
}

func expandGUIPreviewLocalizationValue(value, language string, values map[string]map[string]string, depth int, seen map[string]bool) string {
	if depth >= guiLocalizationNestedDepth {
		return value
	}
	expand := func(match, key string) string {
		if key == "" || seen[key] {
			return match
		}
		byLanguage := values[key]
		replacement, exists := byLanguage[language]
		if !exists {
			return match
		}
		nextSeen := make(map[string]bool, len(seen)+1)
		for item := range seen {
			nextSeen[item] = true
		}
		nextSeen[key] = true
		return expandGUIPreviewLocalizationValue(replacement, language, values, depth+1, nextSeen)
	}
	value = guiLocalizationBracketKey.ReplaceAllStringFunc(value, func(match string) string {
		parts := guiLocalizationBracketKey.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		return expand(match, parts[1])
	})
	value = guiLocalizationMacroKey.ReplaceAllStringFunc(value, func(match string) string {
		parts := guiLocalizationMacroKey.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		return expand(match, parts[1])
	})
	return value
}

func canonicalGUIPreviewLocalizationLanguage(language, path string) string {
	base := strings.ToLower(filepath.Base(path))
	value := strings.ToLower(strings.TrimSpace(language))
	switch {
	case strings.Contains(base, "_l_simp_chinese."), strings.Contains(value, "simp_chinese"), strings.Contains(value, "simplified_chinese"):
		return GUIPreviewLanguageSimpChinese
	case strings.Contains(base, "_l_english."), strings.Contains(value, "english"):
		return GUIPreviewLanguageEnglish
	default:
		return value
	}
}

func makeGUILocalizedValue(language, value, resolved, source string, runtimeLookup map[string]string) *GUILocalizedValue {
	bounded, truncated := truncateGUIRunes(value, guiLocalizationValueMaxRunes)
	resolvedBounded, resolvedTruncated := truncateGUIRunes(resolved, guiLocalizationValueMaxRunes)
	display, dynamic := guiLocalizationDisplayText(resolvedBounded)
	resolvedValue := ""
	if resolvedBounded != bounded {
		resolvedValue = resolvedBounded
	}
	return &GUILocalizedValue{
		Language: language, Value: bounded, ResolvedValue: resolvedValue, DisplayText: display, Source: source,
		Dynamic: dynamic, Truncated: truncated || resolvedTruncated, runtimeLookup: runtimeLookup,
	}
}

func guiLocalizationDisplayText(value string) (string, bool) {
	value = strings.ReplaceAll(value, `\n`, "\n")
	value = guiLocalizationSimpleLink.ReplaceAllString(value, "$1")
	dynamic := guiLocalizationRuntime.MatchString(value)
	value = guiLocalizationRuntime.ReplaceAllString(value, "<runtime>")
	value = guiLocalizationIcon.ReplaceAllString(value, "◆")
	value = guiLocalizationColorTag.ReplaceAllString(value, "")
	value = guiLocalizationFormat.ReplaceAllString(value, "")
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = strings.TrimSpace(guiLocalizationSpace.ReplaceAllString(lines[index], " "))
	}
	value = strings.TrimSpace(guiLocalizationNewlines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
	value, _ = truncateGUIRunes(value, guiLocalizationDisplayMax)
	return value, dynamic
}

func selectGUIPreviewLocalizedText(binding *GUILocalizedText, language string) string {
	if binding == nil {
		return ""
	}
	switch language {
	case GUIPreviewLanguageEnglish:
		if binding.English != nil {
			return binding.English.DisplayText
		}
	case GUIPreviewLanguageSimpChinese:
		if binding.SimpChinese != nil {
			return binding.SimpChinese.DisplayText
		}
	case GUIPreviewLanguageBilingual:
		values := make([]string, 0, 2)
		if binding.SimpChinese != nil && binding.SimpChinese.DisplayText != "" {
			values = append(values, binding.SimpChinese.DisplayText)
		}
		if binding.English != nil && binding.English.DisplayText != "" {
			values = append(values, binding.English.DisplayText)
		}
		if len(values) > 0 {
			return strings.Join(values, " / ")
		}
	case GUIPreviewLanguageRaw:
		return binding.Key
	}
	return binding.Key
}

func cloneGUILocalizedText(value *GUILocalizedText) *GUILocalizedText {
	if value == nil {
		return nil
	}
	clone := *value
	if value.English != nil {
		item := *value.English
		clone.English = &item
	}
	if value.SimpChinese != nil {
		item := *value.SimpChinese
		clone.SimpChinese = &item
	}
	return &clone
}

func truncateGUIRunes(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]) + "…", true
}
