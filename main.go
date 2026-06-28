package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ck3-index/internal/indexer"
)

const defaultConfig = "ck3-index.toml"

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ck3-index:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printHelp()
		return nil
	}
	cfgPath := defaultConfig
	if len(args) > 0 && args[0] == "--config" {
		if len(args) < 2 {
			return errors.New("--config requires a path")
		}
		cfgPath = args[1]
		args = args[2:]
	}
	if len(args) == 0 {
		printHelp()
		return nil
	}
	cmd := args[0]
	args = args[1:]
	// Allow --clean anywhere after the command, e.g. "scan --clean".
	clean := false
	filtered := args[:0]
	for _, a := range args {
		if a == "--clean" {
			clean = true
			continue
		}
		filtered = append(filtered, a)
	}
	args = filtered

	switch cmd {
	case "init":
		path := defaultConfig
		if len(args) > 0 {
			path = args[0]
		}
		return indexer.WriteDefaultConfig(path)
	case "scan":
		cfg, err := indexer.LoadConfig(cfgPath)
		if err != nil {
			return err
		}
		cfg.ForceClean = clean
		stats, err := indexer.Scan(ctx, cfg)
		if err != nil {
			return err
		}
		return printJSON(stats)
	case "query":
		if len(args) < 1 {
			return errors.New("usage: ck3-index query <object|types> [id]")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		switch args[0] {
		case "object":
			if len(args) < 2 {
				return errors.New("usage: ck3-index query object <id>")
			}
			result, err := db.QueryObject(ctx, args[1])
			if err != nil {
				return err
			}
			return printJSON(result)
		case "types":
			result, err := db.QueryObjectTypes(ctx)
			if err != nil {
				return err
			}
			return printJSON(result)
		default:
			return errors.New("usage: ck3-index query <object|types> [id]")
		}
	case "refs":
		if len(args) < 1 {
			return errors.New("usage: ck3-index refs <id>")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.QueryRefs(ctx, args[0])
		if err != nil {
			return err
		}
		return printJSON(result)
	case "loc":
		if len(args) < 1 {
			return errors.New("usage: ck3-index loc <key>")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.QueryLocalization(ctx, args[0])
		if err != nil {
			return err
		}
		return printJSON(result)
	case "resource":
		if len(args) < 1 {
			return errors.New("usage: ck3-index resource <path-or-id>")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.QueryResource(ctx, args[0])
		if err != nil {
			return err
		}
		return printJSON(result)
	case "examples":
		if len(args) < 1 {
			return errors.New("usage: ck3-index examples <object-type[:contains]>")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		typ, contains := indexer.SplitExampleID(args[0])
		result, err := db.QueryExamples(ctx, typ, contains, 12)
		if err != nil {
			return err
		}
		return printJSON(result)
	case "rules":
		if len(args) < 1 {
			return errors.New("usage: ck3-index rules <object-type>")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.QueryRules(ctx, args[0])
		if err != nil {
			return err
		}
		return printJSON(result)
	case "patterns":
		if len(args) < 1 {
			return errors.New("usage: ck3-index patterns <object-type>")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.QueryPatterns(ctx, args[0])
		if err != nil {
			return err
		}
		return printJSON(result)
	case "inspect":
		if len(args) < 1 {
			return errors.New("usage: ck3-index inspect <id>")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.LLMInspectObject(ctx, args[0], indexer.LLMOptions{AllowProject: true})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "prepare-edit":
		if len(args) < 1 {
			return errors.New("usage: ck3-index prepare-edit <id-or-type[:term]>")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.LLMPrepareEdit(ctx, args[0], indexer.LLMOptions{AllowProject: true})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "preflight":
		if len(args) < 1 {
			return errors.New("usage: ck3-index preflight <id-or-type-or-resource>")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.LLMPreflight(ctx, args[0], indexer.LLMOptions{AllowProject: true})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "diagnose":
		if len(args) < 1 {
			return errors.New("usage: ck3-index diagnose <id-or-key-or-resource>")
		}
		db, err := openDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.LLMDiagnoseKey(ctx, args[0], indexer.LLMOptions{AllowProject: true})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "lookup-scope":
		if len(args) < 1 {
			return errors.New("usage: ck3-index lookup-scope <trigger-or-effect-key>")
		}
		sl := indexer.LookupScope(args[0])
		if sl == nil {
			return printJSON(map[string]any{"found": false, "key": args[0]})
		}
		return printJSON(map[string]any{
			"found":      true,
			"key":        sl.Key,
			"is_trigger": sl.IsTrigger,
			"is_effect":  sl.IsEffect,
			"scope_mask": sl.ScopeMask,
			"scope_desc": sl.ScopeDesc,
		})
	case "lookup-shape":
		if len(args) < 1 {
			return errors.New("usage: ck3-index lookup-shape <trigger-or-effect-key>")
		}
		sd := indexer.LookupShape(args[0])
		if sd == nil {
			return printJSON(map[string]any{"found": false, "key": args[0]})
		}
		return printJSON(map[string]any{
			"found": true,
			"key":   args[0],
			"shape": sd.Shape,
			"desc":  sd.Desc,
		})
	case "lookup-define":
		if len(args) < 1 {
			return errors.New("usage: ck3-index lookup-define <@define-name>")
		}
		found := indexer.IsDefine(args[0])
		return printJSON(map[string]any{"found": found, "key": args[0]})
	case "lookup-on-action":
		if len(args) < 1 {
			return errors.New("usage: ck3-index lookup-on-action <on-action-name>")
		}
		found := indexer.IsOnAction(args[0])
		return printJSON(map[string]any{"found": found, "key": args[0]})
	case "lookup-iterator":
		if len(args) < 1 {
			return errors.New("usage: ck3-index lookup-iterator <iterator-key>")
		}
		il := indexer.LookupIterator(args[0])
		if il == nil {
			return printJSON(map[string]any{"found": false, "key": args[0]})
		}
		return printJSON(map[string]any{
			"found":     true,
			"key":       il.Key,
			"scope_in":  il.ScopeIn,
			"scope_out": il.ScopeOut,
		})
	case "lookup-example":
		if len(args) < 1 {
			return errors.New("usage: ck3-index lookup-example <trigger-or-effect-key>")
		}
		ex := indexer.LookupExample(args[0])
		if ex == nil {
			return printJSON(map[string]any{"found": false, "key": args[0]})
		}
		return printJSON(map[string]any{
			"found":   true,
			"key":     args[0],
			"desc":    ex.Desc,
			"example": ex.Example,
		})
	case "lookup-modifier":
		if len(args) < 1 {
			return errors.New("usage: ck3-index lookup-modifier <modifier-key>")
		}
		ml := indexer.LookupModifier(args[0])
		if !ml.Found {
			return printJSON(map[string]any{"found": false, "key": args[0]})
		}
		return printJSON(map[string]any{
			"found":     true,
			"key":       args[0],
			"use_areas": ml.UseAreas,
		})
	case "validate":
		cfg, err := indexer.LoadConfig(cfgPath)
		if err != nil {
			return err
		}
		db, err := indexer.Open(filepath.Join(filepath.Dir(cfg.ConfigPath), cfg.Database))
		if err != nil {
			return err
		}
		defer db.Close()
		report, err := db.Validate(ctx)
		if err != nil {
			return err
		}
		return printJSON(report)
	case "mcp":
		cfg, err := indexer.LoadConfig(cfgPath)
		if err != nil {
			return err
		}
		dbPath := filepath.Join(filepath.Dir(cfg.ConfigPath), cfg.Database)
		return serveMCP(ctx, dbPath, os.Stdin, os.Stdout)
	case "diag_stats":
		cfg, err := indexer.LoadConfig(cfgPath)
		if err != nil {
			return err
		}
		db, err := indexer.Open(filepath.Join(filepath.Dir(cfg.ConfigPath), cfg.Database))
		if err != nil {
			return err
		}
		defer db.Close()
		return db.DiagStats(ctx)
	default:
		if strings.TrimSpace(cmd) == "" {
			printHelp()
			return nil
		}
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func openDB(cfgPath string) (*indexer.DB, error) {
	cfg, err := indexer.LoadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	return indexer.Open(filepath.Join(filepath.Dir(cfg.ConfigPath), cfg.Database))
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printHelp() {
	fmt.Println(`ck3-index commands:
  init [path]              write ck3-index.toml
  scan [--clean]           rebuild SQLite index (incremental by default; --clean drops everything)
  query object <id>        show object definitions and override chain
  refs <id>                show incoming/outgoing references
  loc <key>                show localization values
  resource <path-or-id>    show resource files and references
  examples <type[:term]>   show vanilla-first examples for an object type
  rules <type>             show self-owned schema fields learned from local .info files
  patterns <type>          show empirical field shapes learned from indexed scripts
  inspect <id>             show LLM-ready object summary, refs, loc, and diagnostics
  prepare-edit <id|type>   show LLM-ready examples, rules, and edit context
  preflight <id|type|path> show LLM-ready generation/edit blockers and warnings
  diagnose <id|key|path>   show LLM-ready object/loc/resource/ref diagnosis
  lookup-scope <key>       check local scope rule for a trigger/effect key
  lookup-shape <key>       check local value-shape rule for a trigger/effect key
  lookup-define <key>      check if @define name exists in local define rules
  lookup-on-action <key>   check if on_action name is known in local rules
  lookup-iterator <key>    check if iterator/scope name is known in local rules
  lookup-example <key>     show local trigger/effect description and syntax example
  lookup-modifier <key>    check if static modifier key is known in local rules
  validate                 run built-in read-only validation
  mcp                      serve read-only MCP tools over stdio

Use --config <path> before the command to select a config file.`)
}
