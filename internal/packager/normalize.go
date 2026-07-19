package packager

import (
	"encoding/base64"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,63}$`)

var allowedRoots = map[string]bool{
	"common": true, "events": true, "history": true, "gui": true,
	"localization": true, "gfx": true, "map_data": true, "sound": true,
}

var windowsReserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$`)

func normalizeRequest(request Request, limits Limits) (Metadata, []PreparedFile, []string, ValidationReport) {
	meta, report := normalizeMetadata(request.Metadata)
	if report.Blocked {
		return meta, nil, nil, report
	}
	if limits.MaxFiles <= 0 {
		limits = MCPLimits
	}
	if len(request.Files) == 0 {
		return meta, nil, nil, packageBlocked("package_no_files", "package requires at least one mod content file", "files")
	}
	if len(request.Files) > limits.MaxFiles {
		return meta, nil, nil, packageBlocked("package_too_many_files", fmt.Sprintf("package has %d files; limit is %d", len(request.Files), limits.MaxFiles), "files")
	}
	seen := map[string]string{}
	var files []PreparedFile
	var repairs []string
	var total int64
	for _, input := range request.Files {
		rel, err := normalizeContentPath(input.Path)
		if err != nil {
			return meta, nil, repairs, packageBlocked("package_invalid_path", err.Error(), input.Path)
		}
		lower := strings.ToLower(rel)
		if previous, exists := seen[lower]; exists {
			return meta, nil, repairs, packageBlocked("package_duplicate_path", fmt.Sprintf("paths %q and %q collide case-insensitively", previous, rel), rel)
		}
		seen[lower] = rel
		isDescriptor := lower == "descriptor.mod" || (!strings.Contains(rel, "/") && strings.HasSuffix(lower, ".mod"))
		data, err := decodeFileInput(input)
		if err != nil {
			return meta, nil, repairs, packageBlocked("package_invalid_content", err.Error(), rel)
		}
		if int64(len(data)) > limits.MaxFileBytes {
			return meta, nil, repairs, packageBlocked("package_file_too_large", fmt.Sprintf("file %q exceeds %d bytes", rel, limits.MaxFileBytes), rel)
		}
		total += int64(len(data))
		if total > limits.MaxTotalBytes {
			return meta, nil, repairs, packageBlocked("package_too_large", fmt.Sprintf("decoded package content exceeds %d bytes", limits.MaxTotalBytes), rel)
		}
		if isDescriptor {
			launcher := lower != "descriptor.mod"
			if err := descriptorCompatible(data, meta, launcher); err != nil {
				return meta, nil, repairs, packageBlocked("package_descriptor_conflict", err.Error(), rel)
			}
			repairs = append(repairs, "replaced compatible "+rel+" with canonical generated descriptor")
			continue
		}
		if !supportedContentPath(rel) {
			return meta, nil, repairs, packageBlocked("package_unsupported_root", fmt.Sprintf("file %q is outside supported CK3 load roots", rel), rel)
		}
		if isLocalizationPath(rel) && !hasUTF8BOM(data) {
			data = append([]byte{0xef, 0xbb, 0xbf}, data...)
			repairs = append(repairs, "added UTF-8 BOM to "+rel)
		}
		files = append(files, PreparedFile{Path: rel, Data: data})
	}
	if len(files) == 0 {
		return meta, nil, repairs, packageBlocked("package_no_content", "package requires at least one CK3 content or resource file besides descriptors", "files")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return meta, files, repairs, ValidationReport{}
}

func normalizeMetadata(meta Metadata) (Metadata, ValidationReport) {
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Slug = strings.ToLower(strings.TrimSpace(meta.Slug))
	meta.Version = strings.TrimSpace(meta.Version)
	meta.SupportedVersion = strings.TrimSpace(meta.SupportedVersion)
	meta.Kind = strings.ToLower(strings.TrimSpace(meta.Kind))
	if meta.Kind == "" {
		meta.Kind = "addon"
	}
	fields := map[string]string{"name": meta.Name, "slug": meta.Slug, "version": meta.Version, "supported_version": meta.SupportedVersion}
	for field, value := range fields {
		if value == "" {
			return meta, packageBlocked("package_metadata_required", field+" is required", field)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return meta, packageBlocked("package_metadata_invalid", field+" must be one line and contain no NUL bytes", field)
		}
	}
	if !slugPattern.MatchString(meta.Slug) {
		return meta, packageBlocked("package_slug_invalid", "slug must match [a-z0-9][a-z0-9_-]{2,63}", "slug")
	}
	if meta.Kind != "addon" && meta.Kind != "submod" && meta.Kind != "total_conversion" {
		return meta, packageBlocked("package_kind_invalid", "kind must be addon, submod, or total_conversion", "kind")
	}
	meta.Tags = normalizeList(meta.Tags, false)
	meta.Dependencies = normalizeList(meta.Dependencies, false)
	var err error
	meta.ReplacePaths, err = normalizeReplacePaths(meta.ReplacePaths)
	if err != nil {
		return meta, packageBlocked("package_replace_path_invalid", err.Error(), "replace_paths")
	}
	if len(meta.Tags) == 0 {
		return meta, packageBlocked("package_tags_required", "at least one tag is required", "tags")
	}
	if meta.Kind == "submod" && len(meta.Dependencies) == 0 {
		return meta, packageBlocked("package_dependency_required", "submod packages must declare at least one dependency", "dependencies")
	}
	if meta.Kind != "total_conversion" && len(meta.ReplacePaths) > 0 {
		return meta, packageBlocked("package_replace_path_forbidden", "replace_paths are only allowed for kind=total_conversion", "replace_paths")
	}
	for _, values := range [][]string{meta.Tags, meta.Dependencies, meta.ReplacePaths} {
		for _, value := range values {
			if strings.ContainsAny(value, "\x00\r\n") {
				return meta, packageBlocked("package_metadata_invalid", "metadata list values must be single-line strings", value)
			}
		}
	}
	return meta, ValidationReport{}
}

func normalizeReplacePaths(values []string) ([]string, error) {
	seen := map[string]bool{}
	var result []string
	for _, raw := range values {
		value := strings.TrimSpace(filepathSlash(raw))
		if value == "" {
			continue
		}
		if filepath.IsAbs(raw) || strings.HasPrefix(value, "/") || strings.Contains(strings.Split(value, "/")[0], ":") {
			return nil, fmt.Errorf("replace_path must be relative: %q", raw)
		}
		parts := strings.Split(value, "/")
		for _, part := range parts {
			if part == "" || part == "." || part == ".." || windowsReserved.MatchString(part) {
				return nil, fmt.Errorf("replace_path contains an unsafe segment: %q", raw)
			}
		}
		value = strings.Trim(path.Clean(value), "/")
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func normalizeList(values []string, pathValues bool) []string {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if pathValues {
			value = strings.Trim(filepathSlash(value), "/")
		}
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizeContentPath(raw string) (string, error) {
	value := strings.TrimSpace(filepathSlash(raw))
	if value == "" || strings.Contains(value, "\x00") {
		return "", fmt.Errorf("file path is empty or contains NUL")
	}
	if filepath.IsAbs(raw) || strings.HasPrefix(value, "/") || strings.Contains(strings.Split(value, "/")[0], ":") {
		return "", fmt.Errorf("file path must be relative: %q", raw)
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("file path contains an unsafe segment: %q", raw)
		}
		if windowsReserved.MatchString(part) {
			return "", fmt.Errorf("file path contains Windows reserved name %q", part)
		}
	}
	clean := path.Clean(value)
	if clean == "." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("file path must stay inside the mod root: %q", raw)
	}
	return clean, nil
}

func supportedContentPath(rel string) bool {
	if strings.EqualFold(rel, "thumbnail.png") {
		return true
	}
	root := strings.ToLower(strings.SplitN(rel, "/", 2)[0])
	return allowedRoots[root] && strings.Contains(rel, "/")
}

func decodeFileInput(input FileInput) ([]byte, error) {
	if (input.Content == nil) == (input.ContentBase64 == nil) {
		return nil, fmt.Errorf("file %q must provide exactly one of content or content_base64", input.Path)
	}
	if input.Content != nil {
		return []byte(*input.Content), nil
	}
	data, err := base64.StdEncoding.DecodeString(*input.ContentBase64)
	if err != nil {
		return nil, fmt.Errorf("file %q has invalid content_base64: %w", input.Path, err)
	}
	return data, nil
}

func packageBlocked(code, message, path string) ValidationReport {
	return ValidationReport{
		Blocked: true, Blockers: 1,
		Summary:     message,
		Diagnostics: []Diagnostic{{Severity: "error", Code: code, Message: message, Path: path, Confidence: "high"}},
		Fixes:       []string{message},
	}
}

func filepathSlash(value string) string { return strings.ReplaceAll(value, `\`, "/") }
func hasUTF8BOM(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf
}
func trimUTF8BOM(data []byte) []byte {
	if hasUTF8BOM(data) {
		return data[3:]
	}
	return data
}
func isLocalizationPath(rel string) bool {
	lower := strings.ToLower(filepathSlash(rel))
	return strings.HasPrefix(lower, "localization/") && (strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml"))
}
