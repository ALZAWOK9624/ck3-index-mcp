package indexer

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	GUIModelSamplesMaxCollections = 8
	GUIModelSamplesMaxRows        = 16
	GUIModelSamplesMaxTotalRows   = 32
	GUIModelSamplesMaxSamples     = 16
	GUIModelSamplesMaxIDLength    = 64
)

// GUIModelSampleCollection expands one exact fixedgridbox or dynamicgridbox
// item template with bounded caller-provided rows. At least target or datamodel
// is required; when both are present both must match literally.
type GUIModelSampleCollection struct {
	Target    string              `json:"target,omitempty"`
	DataModel string              `json:"datamodel,omitempty"`
	Rows      []GUIModelSampleRow `json:"rows"`
}

type GUIModelSampleRow struct {
	ID      string              `json:"id"`
	Samples []GUIScenarioSample `json:"samples"`
}

// GUIPreviewModelSamples reports exactly which provided rows and expression
// samples were instantiated. It never represents observed Jomini runtime data.
type GUIPreviewModelSamples struct {
	Source             string                                  `json:"source"`
	AppliedCollections int                                     `json:"applied_collections"`
	AppliedRows        int                                     `json:"applied_rows"`
	AppliedSamples     int                                     `json:"applied_samples"`
	UnusedSamples      int                                     `json:"unused_samples"`
	Collections        []GUIPreviewModelSampleCollectionResult `json:"collections,omitempty"`
}

type GUIPreviewModelSampleCollectionResult struct {
	Target       string                           `json:"target,omitempty"`
	DataModel    string                           `json:"datamodel,omitempty"`
	ResolvedGrid string                           `json:"resolved_grid"`
	Rows         []GUIPreviewModelSampleRowResult `json:"rows,omitempty"`
}

type GUIPreviewModelSampleRowResult struct {
	ID      string                    `json:"id"`
	Index   int                       `json:"index"`
	Samples []GUIScenarioSampleResult `json:"samples,omitempty"`
}

type GUIPreviewModelRow struct {
	Source     string `json:"source"`
	Collection int    `json:"collection"`
	Target     string `json:"target,omitempty"`
	DataModel  string `json:"datamodel,omitempty"`
	ID         string `json:"id"`
	Index      int    `json:"index"`
}

type guiModelRowMarker struct {
	Collection int
	Index      int
	Target     string
	DataModel  string
	ID         string
}

type guiPreparedModelSamples struct {
	Input  []GUIModelSampleCollection
	Result *GUIPreviewModelSamples
}

type guiModelGridCandidate struct {
	Path      []int
	Kind      string
	Name      string
	DataModel string
	ItemPath  []int
}

type guiSelectedModelGrid struct {
	Collection int
	Candidate  guiModelGridCandidate
}

func prepareGUIModelSamples(symbol string, element *GUIElement, collections []GUIModelSampleCollection) (*guiPreparedModelSamples, error) {
	if element == nil || len(collections) == 0 {
		return nil, nil
	}
	if len(collections) > GUIModelSamplesMaxCollections {
		return nil, fmt.Errorf("GUI model_samples has %d collections; maximum is %d", len(collections), GUIModelSamplesMaxCollections)
	}
	candidates := collectGUIModelGridCandidates(*element, symbol)
	selected := make([]guiSelectedModelGrid, 0, len(collections))
	totalRows := 0
	result := &GUIPreviewModelSamples{Source: "provided"}
	for collectionIndex, collection := range collections {
		target := strings.TrimSpace(collection.Target)
		dataModel := strings.TrimSpace(collection.DataModel)
		if target == "" && dataModel == "" {
			return nil, fmt.Errorf("GUI model_samples collection %d requires target or datamodel", collectionIndex)
		}
		if len([]rune(target)) > guiScenarioMaxExpression {
			return nil, fmt.Errorf("GUI model_samples collection %d target exceeds %d characters", collectionIndex, guiScenarioMaxExpression)
		}
		if len([]rune(dataModel)) > guiScenarioMaxExpression {
			return nil, fmt.Errorf("GUI model_samples collection %d datamodel exceeds %d characters", collectionIndex, guiScenarioMaxExpression)
		}
		if len(collection.Rows) == 0 {
			return nil, fmt.Errorf("GUI model_samples collection %d requires at least one row", collectionIndex)
		}
		if len(collection.Rows) > GUIModelSamplesMaxRows {
			return nil, fmt.Errorf("GUI model_samples collection %d has %d rows; maximum is %d", collectionIndex, len(collection.Rows), GUIModelSamplesMaxRows)
		}
		totalRows += len(collection.Rows)
		if totalRows > GUIModelSamplesMaxTotalRows {
			return nil, fmt.Errorf("GUI model_samples has %d total rows; maximum is %d", totalRows, GUIModelSamplesMaxTotalRows)
		}

		matches := make([]guiModelGridCandidate, 0, 1)
		for _, candidate := range candidates {
			if target != "" && candidate.Name != target {
				continue
			}
			if dataModel != "" && candidate.DataModel != dataModel {
				continue
			}
			matches = append(matches, candidate)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("GUI model_samples collection %d did not exactly match a fixedgridbox or dynamicgridbox", collectionIndex)
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("GUI model_samples collection %d matched %d grids; provide an exact target to disambiguate", collectionIndex, len(matches))
		}
		if len(matches[0].ItemPath) == 0 {
			return nil, fmt.Errorf("GUI model_samples collection %d grid %q must contain exactly one item template", collectionIndex, matches[0].Name)
		}
		for _, prior := range selected {
			if equalGUIElementPath(prior.Candidate.Path, matches[0].Path) {
				return nil, fmt.Errorf("GUI model_samples collections %d and %d select the same grid", prior.Collection, collectionIndex)
			}
			if isGUIElementPathPrefix(prior.Candidate.Path, matches[0].Path) || isGUIElementPathPrefix(matches[0].Path, prior.Candidate.Path) {
				return nil, fmt.Errorf("GUI model_samples collections %d and %d select nested grids; nested model expansion is not supported", prior.Collection, collectionIndex)
			}
		}

		seenIDs := map[string]bool{}
		collectionResult := GUIPreviewModelSampleCollectionResult{
			Target: target, DataModel: dataModel, ResolvedGrid: guiModelGridLabel(matches[0]),
		}
		for rowIndex, row := range collection.Rows {
			id := strings.TrimSpace(row.ID)
			if id == "" {
				return nil, fmt.Errorf("GUI model_samples collection %d row %d requires an id", collectionIndex, rowIndex)
			}
			if len([]rune(id)) > GUIModelSamplesMaxIDLength {
				return nil, fmt.Errorf("GUI model_samples collection %d row %d id exceeds %d characters", collectionIndex, rowIndex, GUIModelSamplesMaxIDLength)
			}
			if strings.IndexFunc(id, unicode.IsControl) >= 0 {
				return nil, fmt.Errorf("GUI model_samples collection %d row %d id contains control characters", collectionIndex, rowIndex)
			}
			if seenIDs[id] {
				return nil, fmt.Errorf("GUI model_samples collection %d repeats row id %q", collectionIndex, id)
			}
			seenIDs[id] = true
			if len(row.Samples) == 0 {
				return nil, fmt.Errorf("GUI model_samples collection %d row %d requires at least one sample", collectionIndex, rowIndex)
			}
			if _, err := validateGUIScenarioSamples(row.Samples, GUIModelSamplesMaxSamples, fmt.Sprintf("GUI model_samples collection %d row %d", collectionIndex, rowIndex)); err != nil {
				return nil, err
			}
			collectionResult.Rows = append(collectionResult.Rows, GUIPreviewModelSampleRowResult{ID: id, Index: rowIndex})
		}
		result.Collections = append(result.Collections, collectionResult)
		selected = append(selected, guiSelectedModelGrid{Collection: collectionIndex, Candidate: matches[0]})
	}

	// Selected grids are non-nested, so replacing an item template within one
	// grid cannot invalidate another grid's path. Stable path order keeps the
	// mutation deterministic.
	sort.SliceStable(selected, func(left, right int) bool {
		return compareGUIElementPath(selected[left].Candidate.Path, selected[right].Candidate.Path) > 0
	})
	for _, selection := range selected {
		grid := guiElementAtPath(element, selection.Candidate.Path)
		if grid == nil {
			return nil, fmt.Errorf("GUI model_samples internal grid path became invalid")
		}
		template := guiElementAtPath(grid, selection.Candidate.ItemPath)
		if template == nil {
			return nil, fmt.Errorf("GUI model_samples internal item template path became invalid")
		}
		rows := collections[selection.Collection].Rows
		replacements := make([]GUIElement, 0, len(rows))
		for rowIndex, row := range rows {
			clone := cloneGUIElement(*template)
			marker := guiModelRowMarker{
				Collection: selection.Collection,
				Index:      rowIndex,
				Target:     strings.TrimSpace(collections[selection.Collection].Target),
				DataModel:  strings.TrimSpace(collections[selection.Collection].DataModel),
				ID:         strings.TrimSpace(row.ID),
			}
			markGUIModelRow(&clone, &marker)
			replacements = append(replacements, clone)
		}
		if !replaceGUIElementAtPath(grid, selection.Candidate.ItemPath, replacements) {
			return nil, fmt.Errorf("GUI model_samples could not replace item template")
		}
	}
	result.AppliedCollections = len(selected)
	result.AppliedRows = totalRows
	return &guiPreparedModelSamples{Input: collections, Result: result}, nil
}

func applyGUIPreviewModelSamples(preview *GUIPreviewResult, prepared *guiPreparedModelSamples) error {
	if preview == nil || prepared == nil || prepared.Result == nil {
		return nil
	}
	preview.ModelSamples = prepared.Result
	for collectionIndex, collection := range prepared.Input {
		for rowIndex, row := range collection.Rows {
			validated, err := validateGUIScenarioSamples(row.Samples, GUIModelSamplesMaxSamples, fmt.Sprintf("GUI model_samples collection %d row %d", collectionIndex, rowIndex))
			if err != nil {
				return err
			}
			for _, sample := range validated {
				matched := 0
				for nodeIndex := range preview.Nodes {
					node := &preview.Nodes[nodeIndex]
					if node.ModelRow == nil || node.ModelRow.Collection != collectionIndex || node.ModelRow.Index != rowIndex ||
						node.ModelRow.ID != strings.TrimSpace(row.ID) ||
						node.ModelRow.Target != strings.TrimSpace(collection.Target) ||
						node.ModelRow.DataModel != strings.TrimSpace(collection.DataModel) ||
						!guiScenarioSampleMatches(*node, sample.Property, sample.Expression) {
						continue
					}
					applyValidatedGUIScenarioSample(node, sample)
					matched++
				}
				result := GUIScenarioSampleResult{
					Property: sample.Property, Expression: sample.Expression, Value: sample.Value, MatchedNodes: matched,
				}
				preview.ModelSamples.Collections[collectionIndex].Rows[rowIndex].Samples =
					append(preview.ModelSamples.Collections[collectionIndex].Rows[rowIndex].Samples, result)
				if matched == 0 {
					preview.ModelSamples.UnusedSamples++
				} else {
					preview.ModelSamples.AppliedSamples++
				}
			}
		}
	}
	if preview.ModelSamples.UnusedSamples > 0 {
		preview.Warnings = append(preview.Warnings,
			fmt.Sprintf("%d provided GUI model row sample(s) did not exactly match an expression inside their row template", preview.ModelSamples.UnusedSamples))
	}
	return nil
}

func collectGUIModelGridCandidates(root GUIElement, symbol string) []guiModelGridCandidate {
	var result []guiModelGridCandidate
	var walk func(GUIElement, []int, bool)
	walk = func(element GUIElement, path []int, isRoot bool) {
		if isGUIPreviewGrid(element) {
			name := strings.TrimSpace(element.Name)
			if name == "" && isRoot {
				name = strings.TrimSpace(symbol)
			}
			itemPaths := collectGUIItemTemplatePaths(element.Children)
			candidate := guiModelGridCandidate{
				Path: append([]int(nil), path...), Kind: strings.ToLower(strings.TrimSpace(element.Kind)), Name: name,
				DataModel: strings.TrimSpace(guiPreviewProperty(element, "datamodel")),
			}
			if len(itemPaths) == 1 {
				candidate.ItemPath = append([]int(nil), itemPaths[0]...)
			}
			result = append(result, candidate)
		}
		for index := range element.Children {
			walk(element.Children[index], appendGUIElementPath(path, index), false)
		}
	}
	walk(root, nil, true)
	return result
}

func guiModelGridLabel(candidate guiModelGridCandidate) string {
	if candidate.Name != "" {
		return candidate.Name
	}
	if candidate.DataModel != "" {
		return candidate.Kind + ":" + candidate.DataModel
	}
	return candidate.Kind
}

func collectGUIItemTemplatePaths(children []GUIElement) [][]int {
	var result [][]int
	var walk func([]GUIElement, []int)
	walk = func(items []GUIElement, prefix []int) {
		for index := range items {
			path := appendGUIElementPath(prefix, index)
			if strings.EqualFold(strings.TrimSpace(items[index].Kind), "item") {
				result = append(result, path)
				continue
			}
			if isGUIPreviewStructural(items[index].Kind) {
				walk(items[index].Children, path)
			}
		}
	}
	walk(children, nil)
	return result
}

func markGUIModelRow(element *GUIElement, marker *guiModelRowMarker) {
	if element == nil {
		return
	}
	copyMarker := *marker
	element.modelRow = &copyMarker
	for index := range element.Children {
		markGUIModelRow(&element.Children[index], marker)
	}
	for index := range element.Linked {
		markGUIModelRow(&element.Linked[index].Element, marker)
	}
}

func guiElementAtPath(root *GUIElement, path []int) *GUIElement {
	current := root
	for _, index := range path {
		if current == nil || index < 0 || index >= len(current.Children) {
			return nil
		}
		current = &current.Children[index]
	}
	return current
}

func replaceGUIElementAtPath(root *GUIElement, path []int, replacements []GUIElement) bool {
	if root == nil || len(path) == 0 {
		return false
	}
	parent := guiElementAtPath(root, path[:len(path)-1])
	index := path[len(path)-1]
	if parent == nil || index < 0 || index >= len(parent.Children) {
		return false
	}
	next := make([]GUIElement, 0, len(parent.Children)-1+len(replacements))
	next = append(next, parent.Children[:index]...)
	next = append(next, replacements...)
	next = append(next, parent.Children[index+1:]...)
	parent.Children = next
	return true
}

func appendGUIElementPath(path []int, index int) []int {
	next := make([]int, len(path)+1)
	copy(next, path)
	next[len(path)] = index
	return next
}

func equalGUIElementPath(left, right []int) bool {
	return len(left) == len(right) && isGUIElementPathPrefix(left, right)
}

func isGUIElementPathPrefix(prefix, path []int) bool {
	if len(prefix) > len(path) {
		return false
	}
	for index := range prefix {
		if prefix[index] != path[index] {
			return false
		}
	}
	return true
}

func compareGUIElementPath(left, right []int) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}
