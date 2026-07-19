package indexer

import (
	"context"
	"database/sql"
	"strings"
)

// bindGUIPreviewTextures reuses the main resources table. It deliberately
// does not create a GUI-specific sprite catalog or expose filesystem paths.
func (db *DB) bindGUIPreviewTextures(ctx context.Context, preview *GUIPreviewResult, allowProject bool) error {
	if preview == nil {
		return nil
	}
	type binding struct {
		ref GUITextureRef
	}
	cache := map[string]binding{}
	resolve := func(raw string) (*GUITextureRef, error) {
		raw = strings.Trim(strings.TrimSpace(raw), "\"")
		if raw == "" {
			return nil, nil
		}
		preview.Textures.Total++
		if strings.Contains(raw, "[") || strings.Contains(raw, "]") {
			preview.Textures.Dynamic++
			return &GUITextureRef{Path: raw, Dynamic: true}, nil
		}
		path := normalizeResource(raw)
		if cached, ok := cache[path]; ok {
			ref := cached.ref
			if ref.Resolved {
				preview.Textures.Resolved++
			} else {
				preview.Textures.Missing++
			}
			return &ref, nil
		}
		query := `SELECT r.source_name,r.kind,r.path FROM resources r JOIN files f ON f.id=r.file_id
			WHERE f.overridden=0 AND r.resource_path=?`
		args := []any{path}
		if !allowProject {
			query += ` AND r.source_rank>1`
		}
		query += ` ORDER BY r.source_rank ASC LIMIT 1`
		ref := GUITextureRef{Path: path}
		err := db.sql.QueryRowContext(ctx, query, args...).Scan(&ref.Source, &ref.Kind, &ref.filePath)
		if err == nil {
			ref.Resolved = true
			preview.Textures.Resolved++
		} else if err == sql.ErrNoRows {
			preview.Textures.Missing++
		} else {
			return nil, err
		}
		cache[path] = binding{ref: ref}
		return &ref, nil
	}
	for index := range preview.Nodes {
		ref, err := resolve(preview.Nodes[index].Texture)
		if err != nil {
			return err
		}
		preview.Nodes[index].TextureRef = ref
		if preview.Nodes[index].Semantics == nil {
			continue
		}
		ref, err = resolve(preview.Nodes[index].Semantics.NoProgressTexture)
		if err != nil {
			return err
		}
		preview.Nodes[index].NoProgressTextureRef = ref
	}
	if preview.Textures.Dynamic > 0 {
		preview.Warnings = append(preview.Warnings, "Dynamic GUI textures require runtime data and are shown as references only")
	}
	if preview.Textures.Missing > 0 {
		preview.Warnings = append(preview.Warnings, "Some literal GUI textures are not present in the active indexed resource set")
	}
	return nil
}
