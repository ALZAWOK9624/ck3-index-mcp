// scan-perf profiles real workspace rebuilds against an isolated project copy.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"

	"ck3-index/internal/indexer"
)

func main() {
	config := flag.String("config", "", "source workspace configuration (read only)")
	root := flag.String("scratch", "", "scratch directory containing a project copy")
	mode := flag.String("mode", "full", "full, clean, noop, or files")
	database := flag.String("database", "index.sqlite", "scratch database basename")
	rel := flag.String("file", "", "source-relative path for files mode; only the scratch copy is read")
	profile := flag.String("profile", "", "optional CPU profile output")
	flag.Parse()
	if err := run(*config, *root, *mode, *database, *rel, *profile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(config, root, mode, database, rel, profile string) error {
	if config == "" || root == "" || filepath.Base(database) != database {
		return fmt.Errorf("config, scratch and a database basename are required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cfg, err := indexer.LoadConfig(config)
	if err != nil {
		return err
	}
	project, err := indexer.ProjectSource(cfg)
	if err != nil {
		return err
	}
	copyRoot := filepath.Join(root, "project")
	original, err := filepath.EvalSymlinks(project.Path)
	if err != nil {
		return err
	}
	copyResolved, err := filepath.EvalSymlinks(copyRoot)
	if err != nil {
		return err
	}
	if same, err := os.Stat(original); err != nil {
		return err
	} else if copied, err := os.Stat(copyResolved); err != nil {
		return err
	} else if os.SameFile(same, copied) {
		return fmt.Errorf("scratch project must be a separate copy of the live project")
	}
	for i := range cfg.Sources {
		if cfg.Sources[i].Name == project.Name {
			cfg.Sources[i].Path = copyResolved
		}
	}
	cfg.Database = filepath.Join(root, database)
	cfg.ArtifactRoot = filepath.Join(root, "artifacts")
	cfg.MigrationSnapshotRoot = filepath.Join(root, "migration-snapshots")
	cfg.GISCacheRoot = filepath.Join(root, "gis")
	cfg.MCPDatabases = nil
	if profile != "" {
		f, err := os.Create(profile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}
	start := time.Now()
	var stats indexer.ScanStats
	switch mode {
	case "full":
		stats, err = indexer.ScanFullStaged(context.Background(), cfg)
	case "clean":
		cfg.BaseDatabase = ""
		cfg.ForceClean = true
		stats, err = indexer.ScanFullStaged(context.Background(), cfg)
	case "noop":
		stats, err = indexer.Scan(context.Background(), cfg)
	case "files":
		if rel == "" {
			return fmt.Errorf("files mode requires -file")
		}
		stats, err = indexer.ScanFiles(context.Background(), cfg, []string{rel})
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	report := struct {
		Mode       string            `json:"mode"`
		WallMillis int64             `json:"wall_ms"`
		Stats      indexer.ScanStats `json:"stats"`
		Error      string            `json:"error,omitempty"`
	}{Mode: mode, WallMillis: time.Since(start).Milliseconds(), Stats: stats}
	if err != nil {
		report.Error = err.Error()
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if outputErr := enc.Encode(report); outputErr != nil {
		return outputErr
	}
	return err
}
