package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"path"
	"sort"
	"strings"
)

// CK3 dispatches script by directory: the loader walks a fixed set of folders
// and hands each one to a specific parser. A file that lands in a folder the
// loader does not read parses cleanly, indexes cleanly, and is then ignored at
// runtime with no log line — the failure mode that survived the 1.19
// common/religion/religions -> religion_types rename, where the old folder
// stayed behind still holding content that no longer reached the game.
//
// The valid folder set is derived from the configured game layer instead of
// being hardcoded, so it tracks whichever CK3 version the workspace points at
// rather than drifting one release behind the install.

const folderSchemaRenameCode = "unread_script_folder"

// folderSchema is the directory shape of the engine layer: which directories
// exist and which children each of them has.
//
// It deliberately answers only one question — is this folder name a near-miss
// of a real dispatch folder — and not the tempting second one, whether the
// loader recurses below a folder. Vanilla ships no subdirectory under
// common/buildings/, yet Godherja loads its buildings from
// common/buildings/godherja/, so an absent subdirectory in the install is
// evidence about Paradox's filing habits and nothing about the loader.
type folderSchema struct {
	dirs     map[string]bool
	children map[string]map[string]bool
}

func (s folderSchema) known(dir string) bool { return s.dirs[dir] }

// empty reports whether the schema carries no evidence at all. Every check
// degrades to silence in that case: a workspace with no game layer configured
// must not have its own folders declared wrong by an absent authority.
func (s folderSchema) empty() bool { return len(s.dirs) == 0 }

func loadFolderSchema(ctx context.Context, tx *sql.Tx) (folderSchema, error) {
	schema := folderSchema{
		dirs:     map[string]bool{},
		children: map[string]map[string]bool{},
	}
	// The game role is the authority. Dependency mods are peers that can carry
	// the very mistake this check looks for, so they never widen the schema.
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT f.rel_path FROM files f
		JOIN source_layers sl ON lower(sl.name)=lower(f.source_name)
		WHERE sl.role=? AND f.kind IN ('script','localization','schema')`, string(SourceRoleGame))
	if err != nil {
		return folderSchema{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var rel string
		if err := rows.Scan(&rel); err != nil {
			return folderSchema{}, err
		}
		dir := normalizeSchemaDir(rel)
		if dir == "" {
			continue
		}
		for d := dir; d != ""; d = parentSchemaDir(d) {
			schema.dirs[d] = true
			parent := parentSchemaDir(d)
			if parent == "" {
				continue
			}
			if schema.children[parent] == nil {
				schema.children[parent] = map[string]bool{}
			}
			schema.children[parent][path.Base(d)] = true
		}
	}
	return schema, rows.Err()
}

func normalizeSchemaDir(rel string) string {
	cleaned := strings.Trim(strings.ToLower(strings.ReplaceAll(rel, "\\", "/")), "/")
	idx := strings.LastIndex(cleaned, "/")
	if idx <= 0 {
		return ""
	}
	return cleaned[:idx]
}

func parentSchemaDir(dir string) string {
	idx := strings.LastIndex(dir, "/")
	if idx <= 0 {
		return ""
	}
	return dir[:idx]
}

type folderSchemaVerdict struct {
	code     string
	severity string
	message  string
}

// evaluate judges one project directory against the engine layer. It reports
// the single shape that directory evidence decides on its own: a near-miss
// sibling of a real dispatch folder, which is a rename or a typo. An unknown
// folder with no engine neighbour to compare against is left alone, because a
// mod is free to file its own content into subfolders the install never uses.
func (s folderSchema) evaluate(dir string) (folderSchemaVerdict, bool) {
	if s.empty() || dir == "" || s.known(dir) {
		return folderSchemaVerdict{}, false
	}
	parent := parentSchemaDir(dir)
	if parent == "" || !s.known(parent) {
		return folderSchemaVerdict{}, false
	}
	name := path.Base(dir)
	if match, ok := s.nearestSibling(parent, name); ok {
		return folderSchemaVerdict{
			code:     folderSchemaRenameCode,
			severity: "error",
			message: fmt.Sprintf("CK3 does not read %s/; the game layer dispatches %s/ instead, so content here reaches no parser",
				dir, path.Join(parent, match)),
		}, true
	}
	return folderSchemaVerdict{}, false
}

func (s folderSchema) nearestSibling(parent, name string) (string, bool) {
	best := ""
	bestScore := 0.0
	for sibling := range s.children[parent] {
		if sibling == name {
			continue
		}
		score, ok := folderNamesSimilar(name, sibling)
		if !ok {
			continue
		}
		// Ties resolve by name so a rebuild of the same index reports the same
		// suggestion instead of whichever sibling the map handed over first.
		if score > bestScore || (score == bestScore && sibling < best) {
			best, bestScore = sibling, score
		}
	}
	return best, best != ""
}

// folderNamesSimilar decides whether two folder names are plausibly the same
// concept spelled differently. Paradox renames keep the head noun and change
// number or add a suffix (religions -> religion_types, holy_sites ->
// holy_site_types), so token overlap on singularized parts carries the signal
// that raw edit distance loses. A short edit distance still counts, to catch
// plain typos in single-token names.
//
// The head token must match. Without that, the trailing category word alone
// pairs names that merely belong to the same folder — events/godherja_events
// would be reported as a misspelling of events/court_events, which is how a
// mod's own perfectly loaded event folder gets called dead.
func folderNamesSimilar(a, b string) (float64, bool) {
	left, right := singularTokens(a), singularTokens(b)
	if len(left) == 0 || len(right) == 0 {
		return 0, false
	}
	if left[0] == right[0] {
		set := map[string]bool{}
		for _, token := range right {
			set[token] = true
		}
		shared := 0
		for _, token := range left {
			if set[token] {
				shared++
			}
		}
		if score := float64(shared) / float64(maxInt(len(left), len(right))); score >= 0.5 {
			return score, true
		}
	}
	if len(a) >= 5 && len(b) >= 5 {
		if distance := levenshteinDistance(a, b); distance <= 2 {
			return 1 - float64(distance)/float64(maxInt(len(a), len(b))), true
		}
	}
	return 0, false
}

func singularTokens(name string) []string {
	var out []string
	for _, token := range strings.Split(name, "_") {
		if token == "" {
			continue
		}
		if len(token) > 3 && strings.HasSuffix(token, "s") && !strings.HasSuffix(token, "ss") {
			token = strings.TrimSuffix(token, "s")
		}
		out = append(out, token)
	}
	return out
}

func levenshteinDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = minInt(minInt(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

// refreshFolderSchemaDiagnostics reports project content sitting in folders the
// engine layer gives no evidence of reading. One diagnostic is written per
// offending file so the finding lands where the content is, while the verdict
// is computed once per directory.
func refreshFolderSchemaDiagnostics(ctx context.Context, tx *sql.Tx, projectRank int) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM diagnostics
		WHERE source='validator' AND code=?`, folderSchemaRenameCode); err != nil {
		return err
	}
	schema, err := loadFolderSchema(ctx, tx)
	if err != nil {
		return err
	}
	if schema.empty() {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,rel_path,path FROM files
		WHERE source_rank=? AND kind IN ('script','localization','schema')
		ORDER BY rel_path`, projectRank)
	if err != nil {
		return err
	}
	type offender struct {
		id   int64
		path string
	}
	verdicts := map[string]folderSchemaVerdict{}
	clean := map[string]bool{}
	grouped := map[string][]offender{}
	for rows.Next() {
		var id int64
		var rel, abs string
		if err := rows.Scan(&id, &rel, &abs); err != nil {
			rows.Close()
			return err
		}
		dir := normalizeSchemaDir(rel)
		if dir == "" || clean[dir] {
			continue
		}
		if _, judged := verdicts[dir]; !judged {
			verdict, offending := schema.evaluate(dir)
			if !offending {
				clean[dir] = true
				continue
			}
			verdicts[dir] = verdict
		}
		grouped[dir] = append(grouped[dir], offender{id: id, path: abs})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	dirs := make([]string, 0, len(grouped))
	for dir := range grouped {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		verdict := verdicts[dir]
		for _, file := range grouped[dir] {
			insertDiag(ctx, tx, "validator", verdict.severity, verdict.code, verdict.message, file.id, file.path, 1, 1)
		}
	}
	return nil
}
