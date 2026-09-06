package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeAuditFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// A valid 1x1 uncompressed RGBA DDS, not a renamed text file.
func auditDDS() []byte {
	data := make([]byte, 132)
	copy(data, "DDS ")
	for offset, value := range map[int]uint32{4: 124, 8: 0x100f, 12: 1, 16: 1, 20: 4, 76: 32, 80: 0x41, 88: 32, 92: 0xff, 96: 0xff00, 100: 0xff0000, 104: 0xff000000, 108: 0x1000} {
		binary.LittleEndian.PutUint32(data[offset:], value)
	}
	data[131] = 255
	return data
}

func TestAuditRefreshHashesSameMetadataResource(t *testing.T) {
	for _, mode := range []string{"base", "files"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := writeBaseSeedFixture(t)
			root := f.project
			if mode == "base" {
				root = f.game
			}
			rel := "gfx/audit/same_metadata.dds"
			path := filepath.Join(root, filepath.FromSlash(rel))
			data := auditDDS()
			writeAuditFile(t, path, data)
			cfg := f.config(t, f.project, "cache/audit.sqlite")
			if mode == "base" {
				base := f.config(t, f.emptyProject, "cache/base.sqlite")
				if _, err := Scan(ctx, base); err != nil {
					t.Fatal(err)
				}
				cfg.BaseDatabase = filepath.Join(f.dir, "cache/base.sqlite")
			} else if _, err := ScanFullStaged(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			data[128] = 123
			writeAuditFile(t, path, data)
			if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			var stats ScanStats
			if mode == "base" {
				stats, err = ScanFullStaged(ctx, cfg)
			} else {
				stats, err = ScanFiles(ctx, cfg, []string{rel})
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "base" && (stats.BaseSeed == nil || !stats.BaseSeed.Used) {
				t.Fatalf("base not exercised: %+v", stats)
			}
			if mode == "files" && (stats.ChangedFiles != 1 || stats.FilesHashed != 1 || stats.Noop) {
				t.Fatalf("content edit missed: %+v", stats)
			}
			db := openConfiguredDatabase(t, cfg)
			var got string
			if err := db.sql.QueryRow(`SELECT sha256 FROM files WHERE rel_path=? AND overridden=0`, rel).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if want := fmt.Sprintf("%x", sha256.Sum256(data)); got != want {
				t.Fatalf("stored SHA=%s actual SHA=%s", got, want)
			}
			clean := cfg
			clean.Database = filepath.Join(f.dir, "cache/clean.sqlite")
			clean.BaseDatabase = ""
			clean.ForceClean = true
			if _, err := ScanFullStaged(ctx, clean); err != nil {
				t.Fatal(err)
			}
			actualPath, _ := ConfiguredDatabasePath(cfg)
			cleanPath, _ := ConfiguredDatabasePath(clean)
			gotProjection, wantProjection := indexProjection(t, actualPath), indexProjection(t, cleanPath)
			for table, want := range wantProjection {
				diffProjections(t, table, want, gotProjection[table])
			}
		})
	}
}

func TestAuditCancellationAfterCommitReturnsSuccess(t *testing.T) {
	for _, mode := range []string{"full", "files", "scan"} {
		t.Run(mode, func(t *testing.T) {
			cfg, reader, path, _ := stagedFullRefreshFixture(t)
			before, err := reader.IndexState(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			writeAuditFile(t, path, []byte("audit_after_commit = {}\n"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg.afterScanCommit = cancel
			var stats ScanStats
			switch mode {
			case "full":
				stats, err = ScanFullStaged(ctx, cfg)
			case "files":
				stats, err = ScanFiles(ctx, cfg, []string{"common/traits/staged_refresh.txt"})
			case "scan":
				stats, err = Scan(ctx, cfg)
			}
			if err != nil || !stats.Committed || ctx.Err() == nil {
				t.Fatalf("committed cancellation: stats=%+v err=%v ctx=%v", stats, err, ctx.Err())
			}
			db := openConfiguredDatabase(t, cfg)
			after, err := db.IndexState(context.Background())
			if err != nil || !after.Ready() || samePublishedIndexState(before, after) {
				t.Fatalf("publication lost: %+v %v", after, err)
			}
			var count int
			if err := db.sql.QueryRow(`SELECT COUNT(*) FROM objects WHERE name='audit_after_commit'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("new rows unavailable: %d %v", count, err)
			}
			if mode == "files" && stats.DiagnosticDelta == nil {
				t.Fatal("committed diagnostic delta lost")
			}
		})
	}
}

func TestAuditOnActionRefreshParityAndDependencyChange(t *testing.T) {
	ctx := context.Background()
	f := writeBaseSeedFixture(t)
	rel := "common/on_action/audit.txt"
	gamePath := filepath.Join(f.game, filepath.FromSlash(rel))
	projectPath := filepath.Join(f.project, filepath.FromSlash(rel))
	original := "on_birth = { effect = { add_prestige = 1 } }\n"
	changed := "on_birth = { effect = { add_prestige = 2 } }\n"
	writeAuditFile(t, gamePath, []byte(original))
	writeAuditFile(t, projectPath, []byte(changed))
	cfg := f.config(t, f.project, "cache/on_action.sqlite")
	if _, err := ScanFullStaged(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	assertCount := func(want int) {
		t.Helper()
		db := openConfiguredDatabase(t, cfg)
		var got int
		if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='on_action_direct_override'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("on_action diagnostics=%d want=%d", got, want)
		}
	}
	assertCount(1)
	writeAuditFile(t, projectPath, []byte(changed+"# explicit files reparse\n"))
	if _, err := ScanFiles(ctx, cfg, []string{rel}); err != nil {
		t.Fatal(err)
	}
	assertCount(1)
	// Only the game changes. The project's SHA remains exactly the same.
	writeAuditFile(t, gamePath, []byte(changed))
	if _, err := ScanFiles(ctx, cfg, []string{rel}); err == nil {
		t.Fatal("files accepted changed external diagnostic inputs")
	} else {
		var required *FullScanRequiredError
		if !errors.As(err, &required) {
			t.Fatal(err)
		}
	}
	if _, err := ScanFullStaged(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	assertCount(0)
	writeAuditFile(t, gamePath, []byte(original))
	if _, err := ScanFullStaged(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	assertCount(1)
	writeAuditFile(t, projectPath, []byte(original))
	if _, err := ScanFiles(ctx, cfg, []string{rel}); err != nil {
		t.Fatal(err)
	}
	assertCount(0)
}

func TestAuditFilesRejectsOldLintVersion(t *testing.T) {
	cfg, _, _, _ := stagedFullRefreshFixture(t)
	db := openConfiguredDatabase(t, cfg)
	if _, err := db.sql.Exec(`UPDATE meta SET value='old-lint' WHERE key='lint_rule_version'`); err != nil {
		t.Fatal(err)
	}
	_, err := ScanFiles(context.Background(), cfg, []string{"common/traits/staged_refresh.txt"})
	var required *FullScanRequiredError
	if !errors.As(err, &required) || !strings.Contains(required.Reason, "diagnostic rule") {
		t.Fatalf("old diagnostic cache accepted: %v", err)
	}
	if _, err := ScanFullStaged(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := ScanFiles(context.Background(), cfg, []string{"common/traits/staged_refresh.txt"}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditWriterDurabilityAndTimingSemantics(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "durability.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stage := range []bool{false, true, false} {
		conn, err := db.scanWriteConnection(context.Background(), stage)
		if err != nil {
			t.Fatal(err)
		}
		var sync int
		err = conn.QueryRowContext(context.Background(), `PRAGMA synchronous`).Scan(&sync)
		conn.Close()
		want := 2
		if stage {
			want = 0
		}
		if err != nil || sync != want {
			t.Fatalf("stage=%v synchronous=%d want=%d err=%v", stage, sync, want, err)
		}
	}
	totals := fileWorkTotals{readHashElapsed: 3 * time.Millisecond, parseElapsed: 7 * time.Millisecond, lintElapsed: 2 * time.Millisecond, extractElapsed: 5 * time.Millisecond}
	stats := ScanStats{TimingsMillis: map[string]int64{}}
	totals.applyTimings(&stats)
	for phase, want := range map[string]int64{"read_hash": 3, "parse": 7, "lint": 2, "extract": 5} {
		if stats.TimingsMillis[phase+"_worker_elapsed_sum"] != want || stats.TimingsMillis[phase+"_worker_cpu_total"] != want {
			t.Fatalf("timings lost: %+v", stats.TimingsMillis)
		}
	}
}

func TestAuditCleanRefusesUnreadablePublishedDatabase(t *testing.T) {
	f := writeBaseSeedFixture(t)
	cfg := f.config(t, f.project, "cache/broken.sqlite")
	cfg.ForceClean = true
	path, err := ConfiguredDatabaseAnchorPath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("not a sqlite database\x00audit")
	writeAuditFile(t, path, original)
	if _, err := ScanFullStaged(context.Background(), cfg); err == nil {
		t.Fatal("clean silently discarded an unreadable published database")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatalf("unreadable database was modified: %q %v", got, err)
	}
}
