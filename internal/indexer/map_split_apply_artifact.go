package indexer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const mapSplitApplyArtifactSchemaVersion = 1

var mapSplitApplyArtifactIDPattern = regexp.MustCompile(`^split-apply-[0-9a-f]{32}$`)

type MapSplitApplyArtifactSummary struct {
	ArtifactID    string `json:"artifact_id"`
	CreatedAt     string `json:"created_at"`
	RequestKey    string `json:"request_key,omitempty"`
	RequestSHA256 string `json:"request_sha256,omitempty"`
	PlanID        string `json:"plan_id"`
	PlanHash      string `json:"plan_hash"`
	OutputSHA256  string `json:"output_sha256"`
	OutputBytes   int64  `json:"output_bytes"`
	Replayed      bool   `json:"replayed,omitempty"`
}

type mapSplitApplyArtifactManifest struct {
	SchemaVersion int                    `json:"schema_version"`
	ArtifactID    string                 `json:"artifact_id"`
	CreatedAt     string                 `json:"created_at"`
	RequestKey    string                 `json:"request_key,omitempty"`
	RequestSHA256 string                 `json:"request_sha256,omitempty"`
	PlanID        string                 `json:"plan_id"`
	PlanHash      string                 `json:"plan_hash"`
	ProvinceID    int                    `json:"province_id"`
	SourceName    string                 `json:"source_name"`
	SourceSHA256  string                 `json:"source_sha256"`
	Output        MapTerrainArtifactFile `json:"output"`
	Change        MapSplitImageChange    `json:"change"`
	Files         []MapSplitFileEdit     `json:"files,omitempty"`
	Warnings      []string               `json:"warnings,omitempty"`
}

// CreateMapSplitApplyArtifactContext publishes the raster and its recovery
// manifest as one atomic directory rename. A request key deterministically
// names the final directory, so concurrent or retried calls can commit at most
// one artifact for the same reviewed plan.
func CreateMapSplitApplyArtifactContext(
	ctx context.Context,
	cfg Config,
	sourcePath, sourceName string,
	result MapSplitResult,
	plan MapSplitPlan,
	planState MapSplitPlanArtifactSummary,
	requestKey string,
) (MapSplitImageChange, MapSplitApplyArtifactSummary, error) {
	if err := ctx.Err(); err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	requestKey, err := NormalizeArtifactRequestKey(requestKey)
	if err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	base := filepath.Join(cfg.ArtifactRoot, "map-edits", "split-results")
	if strings.TrimSpace(cfg.ArtifactRoot) == "" {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, fmt.Errorf("artifact_root is not configured")
	}
	if err := EnsureMapOutputOutsideSources(base, cfg.Sources); err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	if err := rejectSymlinkPath(base); err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	requestSHA, err := artifactRequestSHA256(struct {
		PlanID       string
		PlanHash     string
		ProvinceID   int
		SourceName   string
		SourceSHA256 string
	}{
		PlanID: planState.PlanID, PlanHash: planState.PlanHash,
		ProvinceID: result.ProvinceID, SourceName: sourceName,
		SourceSHA256: planState.ProvincesSHA256,
	})
	if err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	id := ""
	if requestKey != "" {
		id = idempotentArtifactID("split-apply-", "map_apply_split", requestKey)
		if _, statErr := os.Lstat(filepath.Join(base, id)); statErr == nil {
			return loadMapSplitApplyReplay(ctx, base, id, requestKey, requestSHA)
		} else if !os.IsNotExist(statErr) {
			return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, statErr
		}
	} else {
		id, err = newMapSplitApplyArtifactID()
		if err != nil {
			return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
		}
	}
	beforeHash, err := sha256FileContext(ctx, sourcePath)
	if err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	if beforeHash != planState.ProvincesSHA256 {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, &MapSplitPlanStaleError{Reason: "provinces.png changed after the plan was verified"}
	}
	temp, err := os.MkdirTemp(base, ".building-"+id+"-")
	if err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temp)
		}
	}()
	outputRel := "map_data/provinces.png"
	outputPath := filepath.Join(temp, filepath.FromSlash(outputRel))
	change, err := ApplyProvinceSplitToImageContext(ctx, sourcePath, outputPath, result, plan)
	if err != nil {
		return change, MapSplitApplyArtifactSummary{}, err
	}
	afterHash, err := sha256FileContext(ctx, sourcePath)
	if err != nil {
		return change, MapSplitApplyArtifactSummary{}, err
	}
	if afterHash != beforeHash {
		return change, MapSplitApplyArtifactSummary{}, &MapSplitPlanStaleError{Reason: "provinces.png changed while the split raster was being generated"}
	}
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		return change, MapSplitApplyArtifactSummary{}, err
	}
	outputHash, err := sha256FileContext(ctx, outputPath)
	if err != nil {
		return change, MapSplitApplyArtifactSummary{}, err
	}
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	storedChange := change
	storedChange.SourcePath = ""
	storedChange.OutputPath = ""
	manifest := mapSplitApplyArtifactManifest{
		SchemaVersion: mapSplitApplyArtifactSchemaVersion,
		ArtifactID:    id, CreatedAt: createdAt,
		RequestKey: requestKey, RequestSHA256: requestSHA,
		PlanID: planState.PlanID, PlanHash: planState.PlanHash,
		ProvinceID: result.ProvinceID, SourceName: sourceName,
		SourceSHA256: beforeHash,
		Output:       MapTerrainArtifactFile{Kind: "provinces", Rel: outputRel, SHA256: outputHash, Bytes: outputInfo.Size(), BitDepth: 8},
		Change:       storedChange, Files: append([]MapSplitFileEdit(nil), plan.Files...),
		Warnings: append([]string(nil), plan.Warnings...),
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return change, MapSplitApplyArtifactSummary{}, err
	}
	manifestFile, err := os.OpenFile(filepath.Join(temp, "manifest.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return change, MapSplitApplyArtifactSummary{}, err
	}
	if _, err := manifestFile.Write(append(manifestData, '\n')); err != nil {
		manifestFile.Close()
		return change, MapSplitApplyArtifactSummary{}, err
	}
	if err := manifestFile.Sync(); err != nil {
		manifestFile.Close()
		return change, MapSplitApplyArtifactSummary{}, err
	}
	if err := manifestFile.Close(); err != nil {
		return change, MapSplitApplyArtifactSummary{}, err
	}
	if err := ctx.Err(); err != nil {
		return change, MapSplitApplyArtifactSummary{}, err
	}
	final := filepath.Join(base, id)
	if err := os.Rename(temp, final); err != nil {
		if requestKey != "" {
			if replayChange, replay, replayErr := loadMapSplitApplyReplay(context.Background(), base, id, requestKey, requestSHA); replayErr == nil {
				return replayChange, replay, nil
			}
		}
		return change, MapSplitApplyArtifactSummary{}, err
	}
	published = true
	change.SourcePath = ""
	change.OutputPath = filepath.Join(final, filepath.FromSlash(outputRel))
	return change, mapSplitApplySummary(manifest, false), nil
}

func loadMapSplitApplyReplay(ctx context.Context, base, id, requestKey, requestSHA string) (MapSplitImageChange, MapSplitApplyArtifactSummary, error) {
	if !mapSplitApplyArtifactIDPattern.MatchString(id) {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, fmt.Errorf("invalid split apply artifact id")
	}
	dir := filepath.Join(base, id)
	if !pathContainsPath(base, dir) || rejectSymlinkPath(dir) != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, fmt.Errorf("split apply artifact is unavailable or unsafe")
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	var manifest mapSplitApplyArtifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, fmt.Errorf("request_key %q points to a damaged split artifact: %w", requestKey, err)
	}
	if manifest.SchemaVersion != mapSplitApplyArtifactSchemaVersion || manifest.ArtifactID != id ||
		manifest.RequestKey != requestKey || manifest.RequestSHA256 != requestSHA {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, fmt.Errorf("request_key %q is already bound to a different split apply request", requestKey)
	}
	outputPath, err := safeTerrainArtifactFile(dir, manifest.Output.Rel)
	if err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	info, err := os.Lstat(outputPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != manifest.Output.Bytes {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, fmt.Errorf("split apply artifact output is missing or unsafe")
	}
	hash, err := sha256FileContext(ctx, outputPath)
	if err != nil {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, err
	}
	if hash != manifest.Output.SHA256 {
		return MapSplitImageChange{}, MapSplitApplyArtifactSummary{}, fmt.Errorf("split apply artifact output failed hash verification")
	}
	change := manifest.Change
	change.OutputPath = outputPath
	return change, mapSplitApplySummary(manifest, true), nil
}

func mapSplitApplySummary(manifest mapSplitApplyArtifactManifest, replayed bool) MapSplitApplyArtifactSummary {
	return MapSplitApplyArtifactSummary{
		ArtifactID: manifest.ArtifactID, CreatedAt: manifest.CreatedAt,
		RequestKey: manifest.RequestKey, RequestSHA256: manifest.RequestSHA256,
		PlanID: manifest.PlanID, PlanHash: manifest.PlanHash,
		OutputSHA256: manifest.Output.SHA256, OutputBytes: manifest.Output.Bytes,
		Replayed: replayed,
	}
}

func newMapSplitApplyArtifactID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "split-apply-" + hex.EncodeToString(raw[:]), nil
}
