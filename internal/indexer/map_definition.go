package indexer

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// MaxProvinceID is the largest province id that can be represented by the
// compact provinceLabel raster. Definition rows are validated against this
// bound before any int-to-int32 conversion occurs.
const MaxProvinceID = 1<<31 - 1

// Province ids and the largest possible gap count remain representable even
// when ck3-index is built for a 32-bit target.
const _ int = MaxProvinceID

type provinceDefinitionEntry struct {
	ID   int
	Line int
}

type provinceDefinitionAudit struct {
	ColorToID             map[uint32]int
	IDToColor             map[int]uint32
	Entries               []provinceDefinitionEntry
	InvalidRows           int
	DuplicateIDs          int
	DuplicateColors       int
	Samples               []string
	DuplicateIDSamples    []string
	DuplicateColorSamples []string
}

type provinceDefinitionColorOccurrence struct {
	ID   int
	Line int
}

// parseProvinceDefinitionsForAudit is the single definition.csv parser used
// by both the diagnostic and raster paths. Invalid or ambiguous rows are
// counted without overwriting the first valid lookup entry, so a malformed id
// cannot overflow provinceLabel and a later duplicate cannot silently replace
// an earlier definition.
func parseProvinceDefinitionsForAudit(path string, limit int) (provinceDefinitionAudit, error) {
	result := provinceDefinitionAudit{
		ColorToID: map[uint32]int{},
		IDToColor: map[int]uint32{},
	}
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer f.Close()

	if limit < 0 {
		limit = 0
	}
	reader := csv.NewReader(f)
	reader.Comma = ';'
	reader.FieldsPerRecord = -1
	firstID := map[int]int{}
	firstColor := map[uint32]provinceDefinitionColorOccurrence{}
	recordNumber := 0
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		recordNumber++
		line := recordNumber
		if readErr == nil && len(record) > 0 {
			line, _ = reader.FieldPos(0)
		}
		if readErr != nil {
			result.InvalidRows++
			appendProvinceDefinitionSample(&result.Samples, limit, fmt.Sprintf("line %d: malformed semicolon CSV", line))
			continue
		}
		if provinceDefinitionBlankOrComment(record) {
			continue
		}
		if provinceDefinitionHeader(record) {
			continue
		}
		if len(record) < 4 {
			result.InvalidRows++
			appendProvinceDefinitionSample(&result.Samples, limit, fmt.Sprintf("line %d: expected ID;R;G;B fields", line))
			continue
		}

		id64, idErr := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(record[0], "\ufeff")), 10, 64)
		red64, redErr := strconv.ParseInt(strings.TrimSpace(record[1]), 10, 64)
		green64, greenErr := strconv.ParseInt(strings.TrimSpace(record[2]), 10, 64)
		blue64, blueErr := strconv.ParseInt(strings.TrimSpace(record[3]), 10, 64)
		if idErr != nil || redErr != nil || greenErr != nil || blueErr != nil {
			result.InvalidRows++
			appendProvinceDefinitionSample(&result.Samples, limit, fmt.Sprintf("line %d: non-numeric id or RGB", line))
			continue
		}
		if red64 < 0 || red64 > 255 || green64 < 0 || green64 > 255 || blue64 < 0 || blue64 > 255 {
			result.InvalidRows++
			appendProvinceDefinitionSample(&result.Samples, limit, fmt.Sprintf("line %d: RGB components must be in 0..255", line))
			continue
		}

		color := uint32(red64)<<16 | uint32(green64)<<8 | uint32(blue64)
		if id64 == 0 && color == 0 {
			// CK3 definition tables commonly contain one all-zero sentinel. It
			// is not a province, but reserving its id and color still makes a
			// repeated sentinel or a positive black province unambiguous.
			duplicateID, duplicateColor := duplicateProvinceDefinitionKeys(&result, firstID, firstColor, 0, color, line, limit)
			if !duplicateID {
				firstID[0] = line
			}
			if !duplicateColor {
				firstColor[color] = provinceDefinitionColorOccurrence{ID: 0, Line: line}
			}
			continue
		}
		if id64 <= 0 || id64 > MaxProvinceID {
			result.InvalidRows++
			appendProvinceDefinitionSample(&result.Samples, limit, fmt.Sprintf("line %d: province id %d must be in 1..%d", line, id64, MaxProvinceID))
			continue
		}

		id := int(id64)
		result.Entries = append(result.Entries, provinceDefinitionEntry{ID: id, Line: line})
		duplicateID, duplicateColor := duplicateProvinceDefinitionKeys(&result, firstID, firstColor, id, color, line, limit)
		// Every syntactically and numerically valid row participates in
		// duplicate tracking, even when another key already made that row
		// ambiguous. This catches chains such as A/color1, B/color1,
		// B/color2 instead of forgetting B from the rejected middle row.
		if !duplicateID {
			firstID[id] = line
		}
		if !duplicateColor {
			firstColor[color] = provinceDefinitionColorOccurrence{ID: id, Line: line}
		}
		if duplicateID || duplicateColor {
			continue
		}
		result.IDToColor[id] = color
		result.ColorToID[color] = id
	}
	return result, nil
}

func parseProvinceDefinitions(path string) (map[uint32]int, error) {
	definitions, err := parseProvinceDefinitionsForAudit(path, 8)
	if err != nil {
		return nil, err
	}
	return definitions.ColorToID, nil
}

func duplicateProvinceDefinitionKeys(result *provinceDefinitionAudit, firstID map[int]int, firstColor map[uint32]provinceDefinitionColorOccurrence, id int, color uint32, line, limit int) (bool, bool) {
	firstIDLine, duplicateID := firstID[id]
	if duplicateID {
		result.DuplicateIDs++
		appendProvinceDefinitionSample(&result.DuplicateIDSamples, limit, fmt.Sprintf("id %d at lines %d and %d", id, firstIDLine, line))
	}
	colorOccurrence, duplicateColor := firstColor[color]
	if duplicateColor {
		result.DuplicateColors++
		appendProvinceDefinitionSample(&result.DuplicateColorSamples, limit, fmt.Sprintf("RGB %d;%d;%d at lines %d and %d (province ids %d and %d)", color>>16, color>>8&0xff, color&0xff, colorOccurrence.Line, line, colorOccurrence.ID, id))
	}
	return duplicateID, duplicateColor
}

func provinceDefinitionBlankOrComment(record []string) bool {
	if len(record) == 0 {
		return true
	}
	first := strings.TrimSpace(strings.TrimPrefix(record[0], "\ufeff"))
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

func provinceDefinitionHeader(record []string) bool {
	if len(record) < 4 {
		return false
	}
	first := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(record[0], "\ufeff")))
	return (first == "province" || first == "id") &&
		strings.EqualFold(strings.TrimSpace(record[1]), "red") &&
		strings.EqualFold(strings.TrimSpace(record[2]), "green") &&
		strings.EqualFold(strings.TrimSpace(record[3]), "blue")
}

func appendProvinceDefinitionSample(samples *[]string, limit int, sample string) {
	if len(*samples) < limit {
		*samples = append(*samples, sample)
	}
}
