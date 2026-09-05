package indexer

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Check the production query against the exact substring oracle, not a copy
// of the candidate-generation algorithm. Exercise both on-disk formats so
// existing published indexes do not need rebuilding just to use the reader.
func TestCompactTrigramSubstringParity(t *testing.T) {
	ctx := context.Background()
	for _, detail := range []string{"full", "none"} {
		t.Run(detail, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "substring.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := db.sql.Exec(`DROP TABLE trigram_loc; CREATE VIRTUAL TABLE trigram_loc USING fts5(value,content='',contentless_delete=1,detail=` + detail + `,tokenize='trigram')`); err != nil {
				t.Fatal(err)
			}
			values := []string{"", "ab", "The Pale Knight of Aversaria", "Knight Pale The", "aaaaa", "aaaXaaa", "苹果树精学院", "树精苹果学院", `a"b'c [foo] AND bar*`, "  padded text  ", "École e\u0301cole İstanbul", "🙂🍎🌳快乐", "line\nbreak\tand space", "mixed CASE case", "abCdeFghIjkLmnop"}
			rng := rand.New(rand.NewSource(731))
			alphabet := []rune("aAbB 苹果树精🙂\"\t")
			for i := 0; i < 60; i++ {
				word := make([]rune, 3+rng.Intn(35))
				for j := range word {
					word[j] = alphabet[rng.Intn(len(alphabet))]
				}
				values = append(values, string(word))
			}
			for i, value := range values {
				// Include hidden source files and differently ranked rows.
				if _, err := db.sql.Exec(`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden) VALUES(?,'project',1,?,?,'localization',0,'test',?)`, i+1, fmt.Sprint(i), fmt.Sprint(i), i%7 == 0); err != nil {
					t.Fatal(err)
				}
				if _, err := db.sql.Exec(`INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,'english',?,?,'project',1,?,1,0)`, fmt.Sprintf("key_%03d", i), value, i+1, fmt.Sprint(i)); err != nil {
					t.Fatal(err)
				}
			}
			queries := []string{"ab", "Pale Knight", "Knight Pale", "aaaa", "苹果树精", "树精苹果", `a"b'c`, "[foo] AND", "🙂🍎🌳", "line\nbreak", " padded ", "CASE", "case", "not present"}
			for _, v := range values {
				r := []rune(v)
				if len(r) >= 3 {
					queries = append(queries, v, string(r[:3]), string(r[len(r)/2:]))
				}
			}
			for _, q := range queries {
				if strings.TrimSpace(q) == "" {
					continue
				}
				got, err := db.searchLocalizationValues(ctx, q, SearchOptions{}, 1000)
				if err != nil {
					t.Fatalf("query %q: %v", q, err)
				}
				rows, err := db.sql.Query(`SELECT l.key FROM localization l JOIN files f ON f.id=l.file_id WHERE f.overridden=0 AND instr(l.value,?)>0 ORDER BY l.source_rank,l.key LIMIT 1000`, q)
				if err != nil {
					t.Fatal(err)
				}
				wantNames := []string{}
				gotNames := []string{}
				for rows.Next() {
					var s string
					if err := rows.Scan(&s); err != nil {
						t.Fatal(err)
					}
					wantNames = append(wantNames, s)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				for _, ev := range got {
					gotNames = append(gotNames, ev.Name)
				}
				if !reflect.DeepEqual(gotNames, wantNames) {
					t.Fatalf("query %q: got %v want %v", q, gotNames, wantNames)
				}
			}
		})
	}
}

func TestTrigramCoverageRejectsEqualCountForgedRow(t *testing.T) {
	db, ids := newScriptTextFTSHealthFixture(t)
	ctx := context.Background()
	for i, value := range []string{"", "xy", "苹果树精"} {
		if _, err := db.sql.Exec(`INSERT INTO localization(id,key,language,value,file_id,source_name,source_rank,path,line,replace_dir) VALUES(?,?,'english',?,?,'project',1,'test.yml',1,0)`, i+1, fmt.Sprint(i), value, ids[0]); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := trigramLocMatches(ctx, db.sql); err != nil || !ok {
		t.Fatalf("healthy zero-token rows: %v %v", ok, err)
	}
	if _, err := db.sql.Exec(`DELETE FROM trigram_loc WHERE rowid=2; INSERT INTO trigram_loc(rowid,value) VALUES(999,'forged')`); err != nil {
		t.Fatal(err)
	}
	if ok, err := trigramLocMatches(ctx, db.sql); err != nil || ok {
		t.Fatalf("forged equal count: %v %v", ok, err)
	}
}

func TestCountyAnchorHistoryEffectiveDates(t *testing.T) {
	db, _ := newScriptTextFTSHealthFixture(t)
	ctx := context.Background()
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`INSERT INTO map_province_history VALUES(7,0,'culture','old'),(7,100,'culture',''),(7,200,'culture','new')`,
		`INSERT INTO map_province_history VALUES(7,0,'religion','faith'),(7,150,'holding','none'),(7,200,'holding','castle_holding')`,
		`INSERT INTO map_province_history VALUES(8,0,'culture','other'),(8,0,'religion','other'),(8,0,'holding','castle_holding')`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	anchors := map[string]countyHistoryAnchor{"c_test": {ProvinceID: 7, BaronyID: "b_test", Path: "titles.txt", Line: 5}, "c_other": {ProvinceID: 8}}
	got := countyHistoryAnchorDiagnostics(ctx, tx, anchors, []int{200, 0, 150, 100, 199, 201})
	if len(got) != 1 || got[0].Code != "county_history_anchor_missing" || got[0].Occurrences != 7 || !strings.Contains(got[0].Message, "culture, holding") {
		t.Fatalf("effective date boundaries: %+v", got)
	}
	if got := countyHistoryAnchorDiagnostics(ctx, tx, anchors, nil); len(got) != 0 {
		t.Fatalf("no bookmarks: %+v", got)
	}
}
