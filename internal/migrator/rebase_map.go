package migrator

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// rebaseProvinceMapping is deliberately limited to identities proven by an
// exact RGB match in definition.csv. Numeric province IDs are not an identity
// proof: upstream and project maps may have independently renumbered them.
//
// The mapping direction is target -> project because a project-authoritative
// map needs target references rewritten back to the project map's IDs.
type rebaseProvinceMapping struct {
	TargetToProject   map[int]int
	UnmappedTargetIDs []int
	Ambiguous         bool
}

// RewriteExactProvinceID returns a rewritten ID only when the definition
// tables establish an exact RGB identity. In particular, it intentionally
// does not treat an unknown ID as an identity mapping.
func (mapping rebaseProvinceMapping) RewriteExactProvinceID(id int) (int, bool) {
	if mapping.Ambiguous {
		return 0, false
	}
	mapped, ok := mapping.TargetToProject[id]
	return mapped, ok
}

// buildRebaseProvinceMapping parses all three definition files before
// accepting any mapping. Base is retained as an input rather than inferred
// from project because a rebase transaction must fail closed if its saved
// three-way baseline is missing, malformed, or drifted.
func buildRebaseProvinceMapping(baseDefinition, projectDefinition, targetDefinition []byte) (rebaseProvinceMapping, error) {
	empty := rebaseProvinceMapping{TargetToProject: map[int]int{}}
	if _, err := parseRebaseProvinceDefinitions("base", baseDefinition); err != nil {
		empty.Ambiguous = rebaseDefinitionErrorAmbiguous(err)
		return empty, fmt.Errorf("parse base map_data/definition.csv: %w", err)
	}
	project, err := parseRebaseProvinceDefinitions("project", projectDefinition)
	if err != nil {
		empty.Ambiguous = rebaseDefinitionErrorAmbiguous(err)
		return empty, fmt.Errorf("parse project map_data/definition.csv: %w", err)
	}
	target, err := parseRebaseProvinceDefinitions("target", targetDefinition)
	if err != nil {
		empty.Ambiguous = rebaseDefinitionErrorAmbiguous(err)
		return empty, fmt.Errorf("parse target map_data/definition.csv: %w", err)
	}

	mapping := rebaseProvinceMapping{TargetToProject: make(map[int]int, len(target.byID))}
	for targetID, rgb := range target.byID {
		projectID, ok := project.idByRGB[rgb]
		if !ok {
			mapping.UnmappedTargetIDs = append(mapping.UnmappedTargetIDs, targetID)
			continue
		}
		mapping.TargetToProject[targetID] = projectID
	}
	sort.Ints(mapping.UnmappedTargetIDs)
	return mapping, nil
}

// rebaseMapReferenceFile identifies text definitions whose values can contain
// province or title references. It is intentionally narrow: adding an
// unfamiliar map-adjacent path here must first gain a dedicated parser and
// rewrite rule, rather than permitting a generic numeric replacement.
func rebaseMapReferenceFile(rel string) bool {
	clean := normalizeRebaseMapPath(rel)
	if !strings.HasSuffix(clean, ".txt") {
		return false
	}
	for _, prefix := range []string{
		"history/provinces/",
		"common/province_terrain/",
		"history/titles/",
		"common/landed_titles/",
	} {
		if strings.HasPrefix(clean, prefix) && len(clean) > len(prefix) {
			return true
		}
	}
	return false
}

// rebaseCoreMapDefinitionPath is kept separate from reference-file routing:
// definition.csv establishes the province-ID authority and must never be
// processed as ordinary script text.
func rebaseCoreMapDefinitionPath(rel string) bool {
	return normalizeRebaseMapPath(rel) == "map_data/definition.csv"
}

type rebaseProvinceRGB struct {
	r byte
	g byte
	b byte
}

type rebaseProvinceDefinitions struct {
	byID    map[int]rebaseProvinceRGB
	idByRGB map[rebaseProvinceRGB]int
}

type rebaseDefinitionParseError struct {
	Source    string
	Record    int
	Ambiguous bool
	Message   string
}

func (err *rebaseDefinitionParseError) Error() string {
	if err.Record > 0 {
		return fmt.Sprintf("%s definition record %d: %s", err.Source, err.Record, err.Message)
	}
	return fmt.Sprintf("%s definition: %s", err.Source, err.Message)
}

func parseRebaseProvinceDefinitions(source string, data []byte) (rebaseProvinceDefinitions, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if len(bytes.TrimSpace(data)) == 0 {
		return rebaseProvinceDefinitions{}, &rebaseDefinitionParseError{Source: source, Message: "is empty"}
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = ';'
	reader.FieldsPerRecord = -1
	recordNumber := 0
	definitions := rebaseProvinceDefinitions{
		byID:    make(map[int]rebaseProvinceRGB),
		idByRGB: make(map[rebaseProvinceRGB]int),
	}
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		recordNumber++
		if err != nil {
			return rebaseProvinceDefinitions{}, &rebaseDefinitionParseError{
				Source: source, Record: recordNumber, Message: fmt.Sprintf("invalid semicolon CSV: %v", err),
			}
		}
		if rebaseDefinitionBlankOrComment(record) {
			continue
		}
		if len(record) < 4 {
			return rebaseProvinceDefinitions{}, &rebaseDefinitionParseError{
				Source: source, Record: recordNumber, Message: "expected ID;R;G;B fields",
			}
		}

		id, err := parseRebaseProvinceID(record[0])
		if err != nil {
			return rebaseProvinceDefinitions{}, &rebaseDefinitionParseError{
				Source: source, Record: recordNumber, Message: fmt.Sprintf("invalid province ID %q: %v", strings.TrimSpace(record[0]), err),
			}
		}
		red, err := parseRebaseColorComponent(record[1])
		if err != nil {
			return rebaseProvinceDefinitions{}, &rebaseDefinitionParseError{
				Source: source, Record: recordNumber, Message: fmt.Sprintf("invalid red component %q: %v", strings.TrimSpace(record[1]), err),
			}
		}
		green, err := parseRebaseColorComponent(record[2])
		if err != nil {
			return rebaseProvinceDefinitions{}, &rebaseDefinitionParseError{
				Source: source, Record: recordNumber, Message: fmt.Sprintf("invalid green component %q: %v", strings.TrimSpace(record[2]), err),
			}
		}
		blue, err := parseRebaseColorComponent(record[3])
		if err != nil {
			return rebaseProvinceDefinitions{}, &rebaseDefinitionParseError{
				Source: source, Record: recordNumber, Message: fmt.Sprintf("invalid blue component %q: %v", strings.TrimSpace(record[3]), err),
			}
		}

		if _, exists := definitions.byID[id]; exists {
			return rebaseProvinceDefinitions{}, &rebaseDefinitionParseError{
				Source: source, Record: recordNumber, Ambiguous: true, Message: fmt.Sprintf("duplicate province ID %d", id),
			}
		}
		rgb := rebaseProvinceRGB{r: red, g: green, b: blue}
		if otherID, exists := definitions.idByRGB[rgb]; exists {
			return rebaseProvinceDefinitions{}, &rebaseDefinitionParseError{
				Source: source, Record: recordNumber, Ambiguous: true,
				Message: fmt.Sprintf("duplicate RGB %d;%d;%d for province IDs %d and %d", red, green, blue, otherID, id),
			}
		}
		definitions.byID[id] = rgb
		definitions.idByRGB[rgb] = id
	}
	if len(definitions.byID) == 0 {
		return rebaseProvinceDefinitions{}, &rebaseDefinitionParseError{Source: source, Message: "contains no province definitions"}
	}
	return definitions, nil
}

func rebaseDefinitionBlankOrComment(record []string) bool {
	if len(record) == 0 {
		return true
	}
	first := strings.TrimSpace(record[0])
	if first == "" {
		for _, value := range record[1:] {
			if strings.TrimSpace(value) != "" {
				return false
			}
		}
		return true
	}
	return strings.HasPrefix(first, "#") || strings.HasPrefix(first, "//")
}

func parseRebaseProvinceID(value string) (int, error) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "\ufeff"))
	id, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if id < 0 {
		return 0, errors.New("must be non-negative")
	}
	return id, nil
}

func parseRebaseColorComponent(value string) (byte, error) {
	value = strings.TrimSpace(value)
	component, err := strconv.ParseUint(value, 10, 8)
	if err != nil {
		return 0, err
	}
	return byte(component), nil
}

func rebaseDefinitionErrorAmbiguous(err error) bool {
	var parseError *rebaseDefinitionParseError
	return errors.As(err, &parseError) && parseError.Ambiguous
}

func normalizeRebaseMapPath(rel string) string {
	raw := strings.ReplaceAll(strings.TrimSpace(rel), "\\", "/")
	for _, segment := range strings.Split(raw, "/") {
		// Collected source-relative paths should never need traversal. Rejecting
		// it here avoids accidentally routing an arbitrary path that happens to
		// normalize beneath one of the supported directories.
		if segment == ".." {
			return ""
		}
	}
	clean := path.Clean(raw)
	return strings.ToLower(clean)
}
