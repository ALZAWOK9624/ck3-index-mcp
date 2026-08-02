package indexer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const mapSplitPlanArtifactSchemaVersion = 1

var mapSplitPlanIDPattern = regexp.MustCompile(`^split-plan-[0-9a-f]{32}$`)

type MapSplitPlanArtifactSummary struct {
	PlanID           string `json:"plan_id"`
	PlanHash         string `json:"plan_hash"`
	RequestKey       string `json:"request_key,omitempty"`
	RequestSHA256    string `json:"request_sha256,omitempty"`
	Replayed         bool   `json:"replayed,omitempty"`
	CreatedAt        string `json:"created_at"`
	ProvinceID       int    `json:"province_id"`
	ScanGeneration   int64  `json:"scan_generation"`
	ScanRevision     string `json:"scan_revision"`
	ProvincesSHA256  string `json:"provinces_sha256"`
	DefinitionSHA256 string `json:"definition_sha256"`
}

type MapSplitPlanStaleError struct{ Reason string }

func (e *MapSplitPlanStaleError) Error() string {
	return "split plan no longer matches the published map state: " + e.Reason
}

type persistedMapSplitPart struct {
	Index      int          `json:"index"`
	Seed       MapSplitSeed `json:"seed"`
	PixelCount int          `json:"pixel_count"`
	MinX       int          `json:"min_x"`
	MinY       int          `json:"min_y"`
	MaxX       int          `json:"max_x"`
	MaxY       int          `json:"max_y"`
	Runs       []MapRun     `json:"runs"`
}

type persistedMapSplitResult struct {
	ProvinceID   int                     `json:"province_id"`
	SourcePixel  int                     `json:"source_pixel_count"`
	Parts        []persistedMapSplitPart `json:"parts"`
	Unreachable  int                     `json:"unreachable_pixel_count"`
	OrphanPieces int                     `json:"orphan_piece_count"`
	OrphanPixels int                     `json:"orphan_pixel_count"`
	Warnings     []string                `json:"warnings,omitempty"`
	RetainSeed   *int                    `json:"retain_seed,omitempty"`
}

type mapSplitPlanPayload struct {
	PlanID           string                  `json:"plan_id"`
	CreatedAt        string                  `json:"created_at"`
	RequestKey       string                  `json:"request_key,omitempty"`
	RequestSHA256    string                  `json:"request_sha256,omitempty"`
	ScanGeneration   int64                   `json:"scan_generation"`
	ScanRevision     string                  `json:"scan_revision"`
	ProvincesSource  string                  `json:"provinces_source"`
	ProvincesSHA256  string                  `json:"provinces_sha256"`
	DefinitionSource string                  `json:"definition_source"`
	DefinitionSHA256 string                  `json:"definition_sha256"`
	Request          MapSplitRequest         `json:"request"`
	Emit             MapSplitEmit            `json:"emit"`
	Result           persistedMapSplitResult `json:"result"`
	Plan             MapSplitPlan            `json:"plan"`
}

type mapSplitPlanArtifact struct {
	SchemaVersion int                 `json:"schema_version"`
	PlanHash      string              `json:"plan_hash"`
	Payload       mapSplitPlanPayload `json:"payload"`
}

func CreateMapSplitPlanArtifact(ctx context.Context, cfg Config, db *DB, expected IndexState, request MapSplitRequest, emit MapSplitEmit, result MapSplitResult, plan MapSplitPlan) (MapSplitPlanArtifactSummary, error) {
	return CreateMapSplitPlanArtifactWithRequestKey(ctx, cfg, db, expected, request, emit, result, plan, "")
}

func CreateMapSplitPlanArtifactWithRequestKey(ctx context.Context, cfg Config, db *DB, expected IndexState, request MapSplitRequest, emit MapSplitEmit, result MapSplitResult, plan MapSplitPlan, requestKey string) (MapSplitPlanArtifactSummary, error) {
	requestKey, err := NormalizeArtifactRequestKey(requestKey)
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	if strings.TrimSpace(cfg.ArtifactRoot) == "" {
		return MapSplitPlanArtifactSummary{}, fmt.Errorf("artifact_root is not configured")
	}
	if !expected.Ready() {
		return MapSplitPlanArtifactSummary{}, &MapSplitPlanStaleError{Reason: "the planning generation is not ready"}
	}
	base := filepath.Join(cfg.ArtifactRoot, "map-edits", "split-plans")
	if err := EnsureMapOutputOutsideSources(base, cfg.Sources); err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	if err := rejectSymlinkPath(base); err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	provincesPath, provincesSource, err := ActiveMapAsset(cfg, "map_data/provinces.png")
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	definitionPath, definitionSource, err := ActiveMapAsset(cfg, "map_data/definition.csv")
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	provincesHash, err := sha256FileContext(ctx, provincesPath)
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	definitionHash, err := sha256FileContext(ctx, definitionPath)
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	current, err := db.IndexState(ctx)
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	if !samePublishedIndexState(expected, current) {
		return MapSplitPlanArtifactSummary{}, &MapSplitPlanStaleError{Reason: "the scan generation changed while the plan was being materialized"}
	}
	requestSHA, err := artifactRequestSHA256(struct {
		Generation       int64
		Revision         string
		ProvincesSource  string
		ProvincesHash    string
		DefinitionSource string
		DefinitionHash   string
		Request          MapSplitRequest
		Emit             MapSplitEmit
	}{
		Generation: expected.Generation, Revision: expected.Revision,
		ProvincesSource: provincesSource, ProvincesHash: provincesHash,
		DefinitionSource: definitionSource, DefinitionHash: definitionHash,
		Request: request, Emit: emit,
	})
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	id := ""
	if requestKey != "" {
		id = idempotentArtifactID("split-plan-", "map_split_province", requestKey)
		if _, statErr := os.Lstat(filepath.Join(base, id)); statErr == nil {
			return loadMapSplitPlanReplay(ctx, base, id, requestKey, requestSHA)
		} else if !os.IsNotExist(statErr) {
			return MapSplitPlanArtifactSummary{}, statErr
		}
	} else {
		id, err = newMapSplitPlanID()
		if err != nil {
			return MapSplitPlanArtifactSummary{}, err
		}
	}
	payload := mapSplitPlanPayload{
		PlanID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		RequestKey: requestKey, RequestSHA256: requestSHA,
		ScanGeneration: expected.Generation, ScanRevision: expected.Revision,
		ProvincesSource: provincesSource, ProvincesSHA256: provincesHash,
		DefinitionSource: definitionSource, DefinitionSHA256: definitionHash,
		Request: request, Emit: emit, Result: persistMapSplitResult(result), Plan: plan,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	digest := sha256.Sum256(payloadJSON)
	planHash := hex.EncodeToString(digest[:])
	artifactJSON, err := json.MarshalIndent(mapSplitPlanArtifact{SchemaVersion: mapSplitPlanArtifactSchemaVersion, PlanHash: planHash, Payload: payload}, "", "  ")
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	temp, err := os.MkdirTemp(base, ".building-"+id+"-")
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temp)
		}
	}()
	manifestPath := filepath.Join(temp, "manifest.json")
	file, err := os.OpenFile(manifestPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	if _, err := file.Write(append(artifactJSON, '\n')); err != nil {
		file.Close()
		return MapSplitPlanArtifactSummary{}, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return MapSplitPlanArtifactSummary{}, err
	}
	if err := file.Close(); err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	if err := ctx.Err(); err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	final := filepath.Join(base, id)
	if err := os.Rename(temp, final); err != nil {
		if requestKey != "" {
			if replay, replayErr := loadMapSplitPlanReplay(context.Background(), base, id, requestKey, requestSHA); replayErr == nil {
				return replay, nil
			}
		}
		return MapSplitPlanArtifactSummary{}, err
	}
	published = true
	return mapSplitPlanSummary(payload, planHash), nil
}

func loadMapSplitPlanReplay(ctx context.Context, base, id, requestKey, requestSHA string) (MapSplitPlanArtifactSummary, error) {
	manifestPath := filepath.Join(base, id, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	var artifact mapSplitPlanArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return MapSplitPlanArtifactSummary{}, fmt.Errorf("request_key %q points to a damaged split plan: %w", requestKey, err)
	}
	if artifact.SchemaVersion != mapSplitPlanArtifactSchemaVersion || artifact.Payload.PlanID != id ||
		artifact.Payload.RequestKey != requestKey || artifact.Payload.RequestSHA256 != requestSHA {
		return MapSplitPlanArtifactSummary{}, fmt.Errorf("request_key %q is already bound to a different split plan request", requestKey)
	}
	payloadJSON, err := json.Marshal(artifact.Payload)
	if err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	digest := sha256.Sum256(payloadJSON)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), artifact.PlanHash) {
		return MapSplitPlanArtifactSummary{}, fmt.Errorf("request_key %q points to a split plan with a damaged hash", requestKey)
	}
	if err := ctx.Err(); err != nil {
		return MapSplitPlanArtifactSummary{}, err
	}
	summary := mapSplitPlanSummary(artifact.Payload, artifact.PlanHash)
	summary.Replayed = true
	return summary, nil
}

func LoadMapSplitPlanArtifact(ctx context.Context, cfg Config, db *DB, planID, planHash string) (MapSplitResult, MapSplitPlan, MapSplitPlanArtifactSummary, error) {
	if !mapSplitPlanIDPattern.MatchString(planID) || !terrainSHA256Pattern.MatchString(strings.ToLower(planHash)) {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, fmt.Errorf("plan_id or plan_hash has an invalid format")
	}
	base := filepath.Join(cfg.ArtifactRoot, "map-edits", "split-plans")
	dir := filepath.Join(base, planID)
	if !pathContainsPath(base, dir) || rejectSymlinkPath(dir) != nil {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, fmt.Errorf("split plan is unavailable or unsafe")
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	info, err := os.Lstat(manifestPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, fmt.Errorf("split plan manifest is unavailable or unsafe")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, err
	}
	var artifact mapSplitPlanArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, fmt.Errorf("split plan manifest is damaged: %w", err)
	}
	if artifact.SchemaVersion != mapSplitPlanArtifactSchemaVersion || artifact.Payload.PlanID != planID || !strings.EqualFold(artifact.PlanHash, planHash) {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, &MapSplitPlanStaleError{Reason: "plan identity or schema does not match"}
	}
	payloadJSON, err := json.Marshal(artifact.Payload)
	if err != nil {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, err
	}
	digest := sha256.Sum256(payloadJSON)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), artifact.PlanHash) {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, &MapSplitPlanStaleError{Reason: "plan artifact hash verification failed"}
	}
	state, err := db.IndexState(ctx)
	if err != nil {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, err
	}
	if !state.Ready() || state.Generation != artifact.Payload.ScanGeneration || state.Revision != artifact.Payload.ScanRevision {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, &MapSplitPlanStaleError{Reason: "the published scan generation or revision changed; create a new plan"}
	}
	provincesPath, provincesSource, err := ActiveMapAsset(cfg, "map_data/provinces.png")
	if err != nil {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, err
	}
	definitionPath, definitionSource, err := ActiveMapAsset(cfg, "map_data/definition.csv")
	if err != nil {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, err
	}
	provincesHash, err := sha256FileContext(ctx, provincesPath)
	if err != nil {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, err
	}
	definitionHash, err := sha256FileContext(ctx, definitionPath)
	if err != nil {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, err
	}
	if provincesSource != artifact.Payload.ProvincesSource || provincesHash != artifact.Payload.ProvincesSHA256 {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, &MapSplitPlanStaleError{Reason: "provinces.png source or hash changed; refresh and create a new plan"}
	}
	if definitionSource != artifact.Payload.DefinitionSource || definitionHash != artifact.Payload.DefinitionSHA256 {
		return MapSplitResult{}, MapSplitPlan{}, MapSplitPlanArtifactSummary{}, &MapSplitPlanStaleError{Reason: "definition.csv source or hash changed; refresh and create a new plan"}
	}
	return restoreMapSplitResult(artifact.Payload.Result), artifact.Payload.Plan, mapSplitPlanSummary(artifact.Payload, artifact.PlanHash), nil
}

func persistMapSplitResult(result MapSplitResult) persistedMapSplitResult {
	out := persistedMapSplitResult{
		ProvinceID: result.ProvinceID, SourcePixel: result.SourcePixel, Unreachable: result.Unreachable,
		OrphanPieces: result.OrphanPieces, OrphanPixels: result.OrphanPixels,
		Warnings: append([]string(nil), result.Warnings...), RetainSeed: result.RetainSeed,
	}
	for _, part := range result.Parts {
		out.Parts = append(out.Parts, persistedMapSplitPart{
			Index: part.Index, Seed: part.Seed, PixelCount: part.PixelCount,
			MinX: part.MinX, MinY: part.MinY, MaxX: part.MaxX, MaxY: part.MaxY,
			Runs: append([]MapRun(nil), part.Runs...),
		})
	}
	return out
}

func restoreMapSplitResult(result persistedMapSplitResult) MapSplitResult {
	out := MapSplitResult{
		ProvinceID: result.ProvinceID, SourcePixel: result.SourcePixel, Unreachable: result.Unreachable,
		OrphanPieces: result.OrphanPieces, OrphanPixels: result.OrphanPixels,
		Warnings: append([]string(nil), result.Warnings...), RetainSeed: result.RetainSeed,
	}
	for _, part := range result.Parts {
		out.Parts = append(out.Parts, MapSplitPart{
			Index: part.Index, Seed: part.Seed, PixelCount: part.PixelCount,
			MinX: part.MinX, MinY: part.MinY, MaxX: part.MaxX, MaxY: part.MaxY,
			Runs: append([]MapRun(nil), part.Runs...),
		})
	}
	return out
}

func mapSplitPlanSummary(payload mapSplitPlanPayload, planHash string) MapSplitPlanArtifactSummary {
	return MapSplitPlanArtifactSummary{
		PlanID: payload.PlanID, PlanHash: planHash, CreatedAt: payload.CreatedAt,
		RequestKey: payload.RequestKey, RequestSHA256: payload.RequestSHA256,
		ProvinceID: payload.Request.ProvinceID, ScanGeneration: payload.ScanGeneration, ScanRevision: payload.ScanRevision,
		ProvincesSHA256: payload.ProvincesSHA256, DefinitionSHA256: payload.DefinitionSHA256,
	}
}

func newMapSplitPlanID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "split-plan-" + hex.EncodeToString(raw[:]), nil
}

func sha256FileContext(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := hash.Write(buffer[:count]); err != nil {
				return "", err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
