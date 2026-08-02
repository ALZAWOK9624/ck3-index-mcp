package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type MapArtifactInfo struct {
	ArtifactID    string                   `json:"artifact_id"`
	Kind          string                   `json:"kind"`
	Status        string                   `json:"status"`
	CreatedAt     string                   `json:"created_at,omitempty"`
	Operation     string                   `json:"operation,omitempty"`
	RequestKey    string                   `json:"request_key,omitempty"`
	RequestSHA256 string                   `json:"request_sha256,omitempty"`
	PlanID        string                   `json:"plan_id,omitempty"`
	PlanHash      string                   `json:"plan_hash,omitempty"`
	ProvinceID    int                      `json:"province_id,omitempty"`
	Replayed      bool                     `json:"replayed,omitempty"`
	Files         []MapTerrainArtifactFile `json:"files,omitempty"`
	Error         string                   `json:"error,omitempty"`
}

type MapArtifactList struct {
	Artifacts []MapArtifactInfo `json:"artifacts"`
	Count     int               `json:"count"`
	Truncated bool              `json:"truncated,omitempty"`
}

func InspectMapArtifact(ctx context.Context, cfg Config, artifactID string) (MapArtifactInfo, error) {
	artifactID = strings.TrimSpace(artifactID)
	switch {
	case terrainArtifactIDPattern.MatchString(artifactID):
		return inspectTerrainArtifact(ctx, cfg, artifactID)
	case mapSplitPlanIDPattern.MatchString(artifactID):
		return inspectSplitPlanArtifact(ctx, cfg, artifactID)
	case mapSplitApplyArtifactIDPattern.MatchString(artifactID):
		return inspectSplitApplyArtifact(ctx, cfg, artifactID)
	default:
		return MapArtifactInfo{}, fmt.Errorf("artifact_id has an invalid format")
	}
}

func ListMapArtifacts(ctx context.Context, cfg Config, limit int) (MapArtifactList, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	root := filepath.Join(cfg.ArtifactRoot, "map-edits")
	if strings.TrimSpace(cfg.ArtifactRoot) == "" {
		return MapArtifactList{}, fmt.Errorf("artifact_root is not configured")
	}
	if err := EnsureMapOutputOutsideSources(root, cfg.Sources); err != nil {
		return MapArtifactList{}, err
	}
	ids := make([]string, 0, limit+1)
	collect := func(dir string, pattern func(string) bool) error {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() && pattern(entry.Name()) {
				ids = append(ids, entry.Name())
			}
		}
		return nil
	}
	if err := collect(root, terrainArtifactIDPattern.MatchString); err != nil {
		return MapArtifactList{}, err
	}
	if err := collect(filepath.Join(root, "split-plans"), mapSplitPlanIDPattern.MatchString); err != nil {
		return MapArtifactList{}, err
	}
	if err := collect(filepath.Join(root, "split-results"), mapSplitApplyArtifactIDPattern.MatchString); err != nil {
		return MapArtifactList{}, err
	}
	items := make([]MapArtifactInfo, 0, len(ids))
	for _, id := range ids {
		info, err := InspectMapArtifact(ctx, cfg, id)
		if err != nil {
			return MapArtifactList{}, err
		}
		items = append(items, info)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt == items[j].CreatedAt {
			return items[i].ArtifactID < items[j].ArtifactID
		}
		return items[i].CreatedAt > items[j].CreatedAt
	})
	result := MapArtifactList{Count: len(items)}
	if len(items) > limit {
		result.Truncated = true
		items = items[:limit]
	}
	result.Artifacts = items
	return result, nil
}

func inspectTerrainArtifact(ctx context.Context, cfg Config, id string) (MapArtifactInfo, error) {
	info := MapArtifactInfo{ArtifactID: id, Kind: "terrain", Status: "damaged"}
	manifestPath := filepath.Join(cfg.ArtifactRoot, "map-edits", id, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}
	var manifest MapTerrainArtifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		info.Error = err.Error()
		return info, nil
	}
	info.CreatedAt = manifest.CreatedAt
	info.Operation = manifest.Operation
	info.RequestKey = manifest.RequestKey
	info.RequestSHA256 = manifest.RequestSHA256
	info.Files = append([]MapTerrainArtifactFile(nil), manifest.Files...)
	if _, _, err := verifyTerrainArtifact(cfg, id, manifest.SourceFingerprint, map[string]bool{}); err != nil {
		info.Error = err.Error()
		return info, nil
	}
	if err := ctx.Err(); err != nil {
		return MapArtifactInfo{}, err
	}
	info.Status = "committed"
	return info, nil
}

func inspectSplitPlanArtifact(ctx context.Context, cfg Config, id string) (MapArtifactInfo, error) {
	info := MapArtifactInfo{ArtifactID: id, Kind: "split_plan", Status: "damaged"}
	path := filepath.Join(cfg.ArtifactRoot, "map-edits", "split-plans", id, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}
	var artifact mapSplitPlanArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		info.Error = err.Error()
		return info, nil
	}
	info.CreatedAt = artifact.Payload.CreatedAt
	info.RequestKey = artifact.Payload.RequestKey
	info.RequestSHA256 = artifact.Payload.RequestSHA256
	info.PlanID = artifact.Payload.PlanID
	info.PlanHash = artifact.PlanHash
	info.ProvinceID = artifact.Payload.Request.ProvinceID
	if artifact.SchemaVersion != mapSplitPlanArtifactSchemaVersion || artifact.Payload.PlanID != id {
		info.Error = "split plan identity or schema is invalid"
		return info, nil
	}
	payloadJSON, err := json.Marshal(artifact.Payload)
	if err != nil {
		return MapArtifactInfo{}, err
	}
	digest := sha256.Sum256(payloadJSON)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), artifact.PlanHash) {
		info.Error = "split plan payload hash verification failed"
		return info, nil
	}
	if err := ctx.Err(); err != nil {
		return MapArtifactInfo{}, err
	}
	info.Status = "committed"
	return info, nil
}

func inspectSplitApplyArtifact(ctx context.Context, cfg Config, id string) (MapArtifactInfo, error) {
	info := MapArtifactInfo{ArtifactID: id, Kind: "split_apply", Status: "damaged"}
	base := filepath.Join(cfg.ArtifactRoot, "map-edits", "split-results")
	path := filepath.Join(base, id, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}
	var manifest mapSplitApplyArtifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		info.Error = err.Error()
		return info, nil
	}
	info.CreatedAt = manifest.CreatedAt
	info.Operation = "apply_split"
	info.RequestKey = manifest.RequestKey
	info.RequestSHA256 = manifest.RequestSHA256
	info.PlanID = manifest.PlanID
	info.PlanHash = manifest.PlanHash
	info.ProvinceID = manifest.ProvinceID
	info.Files = []MapTerrainArtifactFile{manifest.Output}
	_, _, err = loadMapSplitApplyReplay(ctx, base, id, manifest.RequestKey, manifest.RequestSHA256)
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}
	info.Status = "committed"
	return info, nil
}
