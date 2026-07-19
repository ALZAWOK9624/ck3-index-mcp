package packager

import (
	"bufio"
	"fmt"
	"sort"
	"strings"
)

type descriptorMetadata struct {
	Name             string
	Version          string
	SupportedVersion string
	Path             string
	Tags             []string
	Dependencies     []string
	ReplacePaths     []string
	Unknown          []string
}

func descriptorContent(meta Metadata, launcher bool) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "version=%s\n", descriptorQuote(meta.Version))
	b.WriteString("tags={\n")
	for _, tag := range meta.Tags {
		fmt.Fprintf(&b, "\t%s\n", descriptorQuote(tag))
	}
	b.WriteString("}\n")
	fmt.Fprintf(&b, "name=%s\n", descriptorQuote(meta.Name))
	fmt.Fprintf(&b, "supported_version=%s\n", descriptorQuote(meta.SupportedVersion))
	if len(meta.Dependencies) > 0 {
		b.WriteString("dependencies={\n")
		for _, dependency := range meta.Dependencies {
			fmt.Fprintf(&b, "\t%s\n", descriptorQuote(dependency))
		}
		b.WriteString("}\n")
	}
	for _, replacePath := range meta.ReplacePaths {
		fmt.Fprintf(&b, "replace_path=%s\n", descriptorQuote(replacePath))
	}
	if launcher {
		fmt.Fprintf(&b, "path=%s\n", descriptorQuote("mod/"+meta.Slug))
	}
	return []byte(b.String())
}

func descriptorQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `/`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func parseDescriptor(data []byte) descriptorMetadata {
	data = trimUTF8BOM(data)
	var out descriptorMetadata
	state := ""
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		if state != "" {
			if line == "}" {
				state = ""
				continue
			}
			value := descriptorUnquote(line)
			switch state {
			case "tags":
				out.Tags = append(out.Tags, value)
			case "dependencies":
				out.Dependencies = append(out.Dependencies, value)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			out.Unknown = append(out.Unknown, line)
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if value == "{" {
			if key == "tags" || key == "dependencies" {
				state = key
			} else {
				out.Unknown = append(out.Unknown, key)
			}
			continue
		}
		value = descriptorUnquote(value)
		switch key {
		case "name":
			out.Name = value
		case "version":
			out.Version = value
		case "supported_version":
			out.SupportedVersion = value
		case "path":
			out.Path = strings.Trim(filepathSlash(value), "/")
		case "replace_path":
			out.ReplacePaths = append(out.ReplacePaths, strings.Trim(filepathSlash(value), "/"))
		default:
			out.Unknown = append(out.Unknown, key)
		}
	}
	sort.Strings(out.Unknown)
	return out
}

func descriptorUnquote(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = value[1 : len(value)-1]
	}
	return strings.ReplaceAll(value, `\"`, `"`)
}

func descriptorCompatible(data []byte, meta Metadata, launcher bool) error {
	got := parseDescriptor(data)
	if len(got.Unknown) > 0 {
		return fmt.Errorf("descriptor contains unsupported fields: %s", strings.Join(got.Unknown, ", "))
	}
	expectedPath := ""
	if launcher {
		expectedPath = "mod/" + meta.Slug
	}
	checks := []struct{ name, got, want string }{
		{"name", got.Name, meta.Name},
		{"version", got.Version, meta.Version},
		{"supported_version", got.SupportedVersion, meta.SupportedVersion},
		{"path", got.Path, expectedPath},
	}
	for _, check := range checks {
		if check.got != check.want {
			return fmt.Errorf("descriptor %s=%q conflicts with package metadata %q", check.name, check.got, check.want)
		}
	}
	if !sameStringSet(got.Tags, meta.Tags) || !sameStringSet(got.Dependencies, meta.Dependencies) || !sameStringSet(got.ReplacePaths, meta.ReplacePaths) {
		return fmt.Errorf("descriptor tags, dependencies, or replace_path entries conflict with package metadata")
	}
	return nil
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
