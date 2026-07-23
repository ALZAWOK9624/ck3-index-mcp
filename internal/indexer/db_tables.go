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
}

// publishedIndexTables intentionally aliases the immutable package catalog.
// Tests compare this catalog against the schema itself.
var publishedIndexTables = semanticIndexTableCatalog[:]
