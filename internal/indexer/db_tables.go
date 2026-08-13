package indexer

// semanticIndexTableCatalog is the single authoritative list of top-level
// tables that make up a published index generation. Full reset and staged
// publication must both use this catalog so a newly added semantic table
// cannot be silently omitted from either operation.
var semanticIndexTableCatalog = [...]string{
	"meta",
	"source_layers",
	"files",
	"nodes",
	"objects",
	"object_defs",
	"refs",
	"localization",
	"resources",
	"schema_fields",
	"object_fields",
	"diagnostics",
	"saved_scopes",
	"variables",
	"map_provinces",
	"map_province_geometry",
	"map_physical_rasters",
	"map_province_physical",
	"map_physical_water_body_provinces",
	"map_physical_water_bodies",
	"map_major_river_edges",
	"map_surface_rasters",
	"map_surface_materials",
	"map_province_materials",
	"map_object_instances",
	"map_adjacencies",
	"map_strategic_adjacencies",
	"map_water_body_shores",
	"map_water_body_provinces",
	"map_water_bodies",
	"map_title_adjacencies",
	"map_titles",
	"map_title_provinces",
	"map_integrity_issues",
	"map_province_history",
	"map_title_history",
	"map_characters",
	"map_character_history",
	"map_holy_sites",
	"map_holy_site_faiths",
	"map_province_regions",
	"engine_datatypes",
	"engine_scope_rules",
	"search_fts",
	"script_text_fts",
	"trigram_loc",
}

// script_text_fts and trigram_loc are contentless derived caches keyed by
// files.id and localization.id. Neither can be copied row-for-row because
// contentless FTS columns deliberately return no stored source text -- a copy
// would insert NULLs and fresh rowids -- so staged publication rebuilds both
// from the copied source tables instead.
//
// Excluded by name, not by position: appending a table to the catalog used to
// push the previously-last entry back into the published set silently.
var contentlessDerivedTables = map[string]bool{
	"script_text_fts": true,
	"trigram_loc":     true,
}

// rebuildPreservedTables hold caller decisions rather than derived index data,
// so they are created by ensureSchema but belong to neither of the catalog's
// two jobs: reset must not drop them and staged publication must not copy them.
// A diagnostic baseline records which findings the caller has already accepted,
// and a full rebuild reproduces exactly those findings -- dropping the baseline
// with them would silently undo the decision at the moment it is needed most.
//
// Named here rather than left out silently so the schema-catalog test can tell
// a deliberate exclusion from a table someone forgot to register.
var rebuildPreservedTables = map[string]bool{
	"diagnostic_baselines": true,
}

// schemaTableNames is every table ensureSchema creates: the published index
// catalog plus the preserved ones.
func schemaTableNames() []string {
	out := make([]string, 0, len(semanticIndexTableCatalog)+len(rebuildPreservedTables))
	out = append(out, semanticIndexTableCatalog[:]...)
	for table := range rebuildPreservedTables {
		out = append(out, table)
	}
	return out
}

var publishedIndexTables = publishableTables(semanticIndexTableCatalog[:])

func publishableTables(catalog []string) []string {
	out := make([]string, 0, len(catalog))
	for _, table := range catalog {
		if contentlessDerivedTables[table] {
			continue
		}
		out = append(out, table)
	}
	return out
}
