package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ck3-index/internal/indexer"
	"ck3-index/internal/mcpserver"
	"ck3-index/internal/migrator"
	"ck3-index/internal/packager"
)

const defaultConfig = "ck3-index.toml"

type mapPhysicalContextCLIRequest struct {
	TargetType           string   `json:"target_type,omitempty"`
	Target               string   `json:"target,omitempty"`
	Targets              []string `json:"targets,omitempty"`
	Operation            string   `json:"operation,omitempty"`
	IncludeAdjacentWater bool     `json:"include_adjacent_water,omitempty"`
	Limit                int      `json:"limit,omitempty"`
}

func (request mapPhysicalContextCLIRequest) spec() indexer.MapPhysicalContextSpec {
	return indexer.MapPhysicalContextSpec{
		TargetType:           request.TargetType,
		Target:               request.Target,
		Targets:              request.Targets,
		Operation:            request.Operation,
		IncludeAdjacentWater: request.IncludeAdjacentWater,
	}
}

func (request mapPhysicalContextCLIRequest) normalizedLimit() (int, error) {
	limit := request.Limit
	if limit == 0 {
		limit = 16
	}
	if limit < 1 || limit > 20 {
		return 0, fmt.Errorf("limit must be between 1 and 20, got %d", limit)
	}
	return limit, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	if err := run(ctx, os.Args[1:]); err != nil {
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
	var scanFiles []string
	filtered := args[:0]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--clean" {
			clean = true
			continue
		}
		if a == "--files" {
			scanFiles = append(scanFiles, args[i+1:]...)
			break
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
	case "package":
		if len(args) != 1 {
			return errors.New("usage: ck3-index package <spec.json>")
		}
		cfg, err := indexer.LoadConfig(cfgPath)
		if err != nil {
			return err
		}
		var request packager.Request
		if err := readStrictJSONFile(args[0], &request); err != nil {
			return err
		}
		db, err := openReadOnlyDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := packager.Build(ctx, request, packager.BuildOptions{
			ArtifactRoot: cfg.ArtifactRoot, Retention: time.Duration(cfg.ArtifactRetentionHours) * time.Hour,
			Limits: packager.MCPLimits, Validator: packager.IndexerValidator{DB: db},
		})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "package-dir":
		if len(args) != 3 || args[1] != "--meta" {
			return errors.New("usage: ck3-index package-dir <mod-dir> --meta <metadata.json>")
		}
		cfg, err := indexer.LoadConfig(cfgPath)
		if err != nil {
			return err
		}
		var metadata packager.Metadata
		if err := readStrictJSONFile(args[2], &metadata); err != nil {
			return err
		}
		request, excluded, err := packager.RequestFromDirectory(args[0], metadata, packager.DirectoryLimits)
		if err != nil {
			return err
		}
		db, err := openReadOnlyDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := packager.Build(ctx, request, packager.BuildOptions{
			ArtifactRoot: cfg.ArtifactRoot, Retention: time.Duration(cfg.ArtifactRetentionHours) * time.Hour,
			Limits: packager.DirectoryLimits, Validator: packager.IndexerValidator{DB: db}, ExcludedFiles: excluded,
		})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "scan":
		cfg, err := indexer.LoadConfig(cfgPath)
		if err != nil {
			return err
		}
		if len(scanFiles) > 0 {
			stats, err := indexer.ScanFiles(ctx, cfg, scanFiles)
			if err != nil {
				return err
			}
			return printJSON(stats)
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
		db, err := openReadOnlyDB(cfgPath)
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
		db, err := openDB(ctx, cfgPath)
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
		db, err := openDB(ctx, cfgPath)
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
		db, err := openDB(ctx, cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.QueryResource(ctx, args[0])
		if err != nil {
			return err
		}
		return printJSON(result)
	case "gui":
		if len(args) < 1 {
			return errors.New("usage: ck3-index gui <summary|file|type|template|preview> [path-or-symbol] [path-prefix] [--format png|html|both] [--html-mode static|inspector] [--language raw|english|simp_chinese|bilingual] [--width px] [--height px] [--limit nodes] [--scenario samples.json] [--out file] [--html-out file.html]")
		}
		options := indexer.GUIQueryOptions{Operation: args[0], AllowProject: true}
		previewOutput := ""
		htmlOutput := ""
		switch args[0] {
		case "summary":
			if len(args) > 1 {
				options.PathPrefix = args[1]
			}
		case "file":
			if len(args) < 2 {
				return errors.New("usage: ck3-index gui file <gui/path.gui>")
			}
			options.Path = args[1]
		case "type", "template":
			if len(args) < 2 {
				return fmt.Errorf("usage: ck3-index gui %s <symbol> [gui/path-prefix]", args[0])
			}
			options.Symbol = args[1]
			if len(args) > 2 {
				options.PathPrefix = args[2]
			}
		case "preview":
			if len(args) < 2 {
				return errors.New("usage: ck3-index gui preview <type-template-or-element> [gui/path-prefix] [--format png|html|both] [--html-mode static|inspector] [--language raw|english|simp_chinese|bilingual] [--width px] [--height px] [--limit nodes] [--scenario samples.json] [--out file] [--html-out file.html]")
			}
			options.Symbol = args[1]
			for index := 2; index < len(args); index++ {
				if args[index] == "--out" {
					if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
						return errors.New("usage: ck3-index gui preview <type-template-or-element> [gui/path-prefix] [--format png|html|both] [--html-mode static|inspector] [--language raw|english|simp_chinese|bilingual] [--out file] [--html-out file.html]")
					}
					previewOutput = args[index+1]
					index++
					continue
				}
				if args[index] == "--html-out" {
					if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
						return errors.New("usage: ck3-index gui preview <type-template-or-element> [gui/path-prefix] [--format png|html|both] [--html-mode static|inspector] [--language raw|english|simp_chinese|bilingual] [--out file] [--html-out file.html]")
					}
					htmlOutput = args[index+1]
					index++
					continue
				}
				if args[index] == "--format" {
					if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
						return errors.New("GUI preview --format requires png, html, or both")
					}
					options.Format = args[index+1]
					index++
					continue
				}
				if args[index] == "--html-mode" {
					if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
						return errors.New("GUI preview --html-mode requires static or inspector")
					}
					options.HTMLMode = args[index+1]
					index++
					continue
				}
				if args[index] == "--language" {
					if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
						return errors.New("GUI preview --language requires raw, english, simp_chinese, or bilingual")
					}
					options.Language = args[index+1]
					index++
					continue
				}
				if args[index] == "--width" || args[index] == "--height" || args[index] == "--limit" {
					flag := args[index]
					if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
						return fmt.Errorf("GUI preview %s requires an integer value", flag)
					}
					value, err := strconv.Atoi(args[index+1])
					if err != nil {
						return fmt.Errorf("GUI preview %s requires an integer value: %w", flag, err)
					}
					switch flag {
					case "--width":
						if value < 64 || value > indexer.GUIPreviewMaxWidth {
							return fmt.Errorf("GUI preview --width must be between 64 and %d", indexer.GUIPreviewMaxWidth)
						}
						options.Width = value
					case "--height":
						if value < 64 || value > indexer.GUIPreviewMaxHeight {
							return fmt.Errorf("GUI preview --height must be between 64 and %d", indexer.GUIPreviewMaxHeight)
						}
						options.Height = value
					case "--limit":
						if value < 1 || value > 500 {
							return errors.New("GUI preview --limit must be between 1 and 500")
						}
						options.Limit = value
					}
					index++
					continue
				}
				if args[index] == "--scenario" {
					if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
						return errors.New("GUI preview --scenario requires a JSON file containing sample_values, model_samples, runtime_facts, and/or action_effects")
					}
					data, err := os.ReadFile(args[index+1])
					if err != nil {
						return fmt.Errorf("read GUI scenario: %w", err)
					}
					var spec struct {
						SampleValues  []indexer.GUIScenarioSample           `json:"sample_values"`
						ModelSamples  []indexer.GUIModelSampleCollection    `json:"model_samples"`
						RuntimeFacts  []indexer.GUIRuntimeFactInput         `json:"runtime_facts"`
						ActionEffects []indexer.GUIRuntimeActionEffectInput `json:"action_effects"`
					}
					decoder := json.NewDecoder(bytes.NewReader(data))
					decoder.DisallowUnknownFields()
					if err := decoder.Decode(&spec); err != nil {
						return fmt.Errorf("decode GUI scenario: %w", err)
					}
					if err := decoder.Decode(&struct{}{}); err != io.EOF {
						return errors.New("decode GUI scenario: expected one JSON object")
					}
					options.Samples = spec.SampleValues
					options.ModelSamples = spec.ModelSamples
					options.RuntimeFacts = spec.RuntimeFacts
					options.ActionEffects = spec.ActionEffects
					index++
					continue
				}
				if options.PathPrefix != "" {
					return fmt.Errorf("unexpected GUI preview argument %q", args[index])
				}
				options.PathPrefix = args[index]
			}
			if htmlOutput != "" && (options.Format == "" || strings.EqualFold(options.Format, "png")) {
				options.Format = "both"
			}
		default:
			return errors.New("usage: ck3-index gui <summary|file|type|template|preview> [path-or-symbol] [path-prefix] [--format png|html|both] [--html-mode static|inspector] [--language raw|english|simp_chinese|bilingual] [--width px] [--height px] [--limit nodes] [--scenario samples.json] [--out file] [--html-out file.html]")
		}
		db, err := openReadOnlyDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.QueryGUI(ctx, options)
		if err != nil {
			return err
		}
		htmlWritten := false
		if previewOutput != "" {
			if result.Preview == nil {
				return fmt.Errorf("GUI preview symbol %q was not found", options.Symbol)
			}
			if strings.EqualFold(result.Preview.Format, "html") {
				if result.Preview.HTML == nil || result.Preview.HTML.Document == "" {
					return errors.New("GUI HTML preview was not generated")
				}
				if err := os.WriteFile(previewOutput, []byte(result.Preview.HTML.Document), 0644); err != nil {
					return err
				}
				htmlWritten = true
			} else {
				if len(result.Preview.PNG) == 0 {
					return errors.New("GUI PNG preview was not generated")
				}
				if err := os.WriteFile(previewOutput, result.Preview.PNG, 0644); err != nil {
					return err
				}
				result.Preview.PNG = nil
			}
		}
		if htmlOutput != "" {
			if result.Preview == nil || result.Preview.HTML == nil || result.Preview.HTML.Document == "" {
				return errors.New("GUI HTML preview was not generated")
			}
			if err := os.WriteFile(htmlOutput, []byte(result.Preview.HTML.Document), 0644); err != nil {
				return err
			}
			htmlWritten = true
		}
		if htmlWritten {
			result.Preview.HTML.Document = ""
		}
		return printJSON(result)
	case "gui-model":
		if len(args) < 1 {
			return errors.New("usage: ck3-index gui-model <file.gui>")
		}
		data, err := os.ReadFile(args[0])
		if err != nil {
			return err
		}
		return printJSON(indexer.BuildGUIModel(string(data)))
	case "gui-resolve":
		if len(args) < 1 {
			return errors.New("usage: ck3-index gui-resolve <file-or-directory> [more...] [--summary]")
		}
		summaryOnly := false
		paths := args[:0]
		for _, arg := range args {
			if arg == "--summary" {
				summaryOnly = true
				continue
			}
			paths = append(paths, arg)
		}
		if len(paths) == 0 {
			return errors.New("usage: ck3-index gui-resolve <file-or-directory> [more...] [--summary]")
		}
		inputs, err := loadGUIInputs(paths)
		if err != nil {
			return err
		}
		resolution := indexer.ResolveGUIModels(inputs)
		if summaryOnly {
			return printJSON(resolution.Summary())
		}
		return printJSON(resolution)
	case "examples":
		if len(args) < 1 {
			return errors.New("usage: ck3-index examples <object-type[:contains]>")
		}
		db, err := openDB(ctx, cfgPath)
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
			return errors.New("usage: ck3-index rules <object-type>|audit|contracts|evidence [--limit N]")
		}
		if args[0] == "audit" {
			limit := 50
			if len(args) == 3 && args[1] == "--limit" {
				value, err := strconv.Atoi(args[2])
				if err != nil || value < 1 || value > 500 {
					return errors.New("usage: ck3-index rules audit [--limit 1..500]")
				}
				limit = value
			} else if len(args) != 1 {
				return errors.New("usage: ck3-index rules audit [--limit 1..500]")
			}
			db, err := openReadOnlyDB(cfgPath)
			if err != nil {
				return err
			}
			defer db.Close()
			result, err := db.AuditOnActionRules(ctx, limit)
			if err != nil {
				return err
			}
			return printJSON(result)
		}
		if args[0] == "contracts" {
			limit := 50
			if len(args) == 3 && args[1] == "--limit" {
				value, err := strconv.Atoi(args[2])
				if err != nil || value < 1 || value > 500 {
					return errors.New("usage: ck3-index rules contracts [--limit 1..500]")
				}
				limit = value
			} else if len(args) != 1 {
				return errors.New("usage: ck3-index rules contracts [--limit 1..500]")
			}
			cfg, err := indexer.LoadConfig(cfgPath)
			if err != nil {
				return err
			}
			db, err := openReadOnlyDB(cfgPath)
			if err != nil {
				return err
			}
			defer db.Close()
			result, err := db.AuditOnActionScopeContracts(ctx, cfg, limit)
			if err != nil {
				return err
			}
			return printJSON(result)
		}
		if args[0] == "evidence" {
			limit := 50
			if len(args) == 3 && args[1] == "--limit" {
				value, err := strconv.Atoi(args[2])
				if err != nil || value < 1 || value > 500 {
					return errors.New("usage: ck3-index rules evidence [--limit 1..500]")
				}
				limit = value
			} else if len(args) != 1 {
				return errors.New("usage: ck3-index rules evidence [--limit 1..500]")
			}
			cfg, err := indexer.LoadConfig(cfgPath)
			if err != nil {
				return err
			}
			db, err := openReadOnlyDB(cfgPath)
			if err != nil {
				return err
			}
			defer db.Close()
			result, err := db.AuditOnActionEvidence(ctx, cfg, limit)
			if err != nil {
				return err
			}
			return printJSON(result)
		}
		db, err := openDB(ctx, cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.QueryRules(ctx, args[0])
		if err != nil {
			return err
		}
		return printJSON(result)
	case "override":
		if len(args) < 1 {
			return errors.New("usage: ck3-index override audit [--source configured-source] [--base configured-source] [--path source-root-relative-prefix] [--limit 1..500]\n       ck3-index override compare <type:id> [--source configured-source] [--base configured-source]")
		}
		switch args[0] {
		case "audit":
			options := indexer.OverrideDriftAuditOptions{Limit: 50}
			for index := 1; index < len(args); index++ {
				if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
					return errors.New("usage: ck3-index override audit [--source configured-source] [--base configured-source] [--path source-root-relative-prefix] [--limit 1..500]")
				}
				value := args[index+1]
				switch args[index] {
				case "--source":
					options.Source = value
				case "--base":
					options.Base = value
				case "--path":
					options.PathPrefix = value
				case "--limit":
					limit, err := strconv.Atoi(value)
					if err != nil || limit < 1 || limit > 500 {
						return errors.New("usage: ck3-index override audit [--source configured-source] [--base configured-source] [--path source-root-relative-prefix] [--limit 1..500]")
					}
					options.Limit = limit
				default:
					return fmt.Errorf("unknown override audit flag %q", args[index])
				}
				index++
			}
			cfg, err := indexer.LoadConfig(cfgPath)
			if err != nil {
				return err
			}
			result, err := indexer.AuditOverrideDrift(ctx, cfg, options)
			if err != nil {
				return err
			}
			return printJSON(result)
		case "compare":
			if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
				return errors.New("usage: ck3-index override compare <type:id> [--source configured-source] [--base configured-source]")
			}
			options := indexer.ObjectCompareOptions{}
			for index := 2; index < len(args); index++ {
				if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
					return errors.New("usage: ck3-index override compare <type:id> [--source configured-source] [--base configured-source]")
				}
				value := args[index+1]
				switch args[index] {
				case "--source":
					options.Source = value
				case "--base":
					options.Base = value
				default:
					return fmt.Errorf("unknown override compare flag %q", args[index])
				}
				index++
			}
			cfg, err := indexer.LoadConfig(cfgPath)
			if err != nil {
				return err
			}
			result, err := indexer.CompareObjectAgainstBase(ctx, cfg, args[1], options)
			if err != nil {
				return err
			}
			return printJSON(result)
		default:
			return fmt.Errorf("unknown override operation %q; expected audit or compare", args[0])
		}
	case "patterns":
		if len(args) < 1 {
			return errors.New("usage: ck3-index patterns <object-type>")
		}
		db, err := openDB(ctx, cfgPath)
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
		db, err := openDB(ctx, cfgPath)
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
		db, err := openDB(ctx, cfgPath)
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
		db, err := openDB(ctx, cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.LLMPreflight(ctx, args[0], indexer.LLMOptions{AllowProject: true})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "preflight-patch":
		if len(args) < 1 {
			return errors.New("usage: ck3-index preflight-patch <json-file>")
		}
		input, err := readPatchInput(args[0])
		if err != nil {
			return err
		}
		db, err := openDB(ctx, cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.LLMPreflightPatch(ctx, input.Files, indexer.LLMOptions{Limit: input.Limit, AllowProject: true})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "impact-patch":
		if len(args) < 1 {
			return errors.New("usage: ck3-index impact-patch <json-file>")
		}
		input, err := readPatchInput(args[0])
		if err != nil {
			return err
		}
		db, err := openDB(ctx, cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.LLMImpactPatch(ctx, input.Files, indexer.LLMOptions{Limit: input.Limit, AllowProject: true})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "preflight-dirty":
		cfg, err := indexer.LoadConfig(cfgPath)
		if err != nil {
			return err
		}
		db, err := openDB(ctx, cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.LLMPreflightDirty(ctx, cfg, indexer.LLMOptions{AllowProject: true})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "diagnose":
		if len(args) < 1 {
			return errors.New("usage: ck3-index diagnose <id-or-key-or-resource>")
		}
		db, err := openDB(ctx, cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.LLMDiagnoseKey(ctx, args[0], indexer.LLMOptions{AllowProject: true})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "search":
		if len(args) < 1 {
			return errors.New("usage: ck3-index search <query>")
		}
		db, err := openReadOnlyDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		result, err := db.LLMSearch(ctx, indexer.SearchOptions{Query: strings.Join(args, " "), LLMOptions: indexer.LLMOptions{Limit: 20, AllowProject: true}})
		if err != nil {
			return err
		}
		return printJSON(result)
	case "lookup-scope":
		if len(args) < 1 {
			return errors.New("usage: ck3-index lookup-scope <trigger-or-effect-key>")
		}
		db, err := openReadOnlyDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		live, err := db.LookupScopeEvidence(ctx, args[0])
		if err != nil {
			return err
		}
		if len(live) > 0 {
			return printJSON(map[string]any{"found": true, "key": args[0], "rules": live, "confidence": "high", "rule_source": "engine_logs"})
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
	case "lookup-datatype":
		if len(args) < 1 {
			return errors.New("usage: ck3-index lookup-datatype <name>")
		}
		db, err := openReadOnlyDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		items, err := db.LookupDatatype(ctx, args[0], 20)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"query": args[0], "found": len(items) > 0, "items": items})
	case "lookup-shape":
		if len(args) < 1 {
			return errors.New("usage: ck3-index lookup-shape <trigger-or-effect-key>")
		}
		sd := indexer.LookupShape(args[0])
		if sd == nil {
			return printJSON(map[string]any{"found": false, "key": args[0]})
		}
		return printJSON(map[string]any{
			"found":         true,
			"key":           sd.Key,
			"evidence_kind": sd.EvidenceKind,
			"documentation": sd.Documentation,
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
		db, err := openReadOnlyDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		state, err := db.IndexState(ctx)
		if err != nil {
			return err
		}
		if state.Ready() {
			live, err := db.LookupOnActionEvidence(ctx, args[0])
			if err != nil {
				return err
			}
			if len(live) > 0 {
				result := map[string]any{"found": true, "key": args[0], "rules": live, "confidence": "high", "rule_source": "engine_logs"}
				if snapshot, found := indexer.ResolveOnActionSnapshotContract(args[0]); found {
					result["snapshot_contract"] = snapshot
				}
				return printJSON(result)
			}
		}
		found := indexer.IsOnAction(args[0])
		result := map[string]any{"found": found, "key": args[0], "confidence": "medium", "rule_source": "engine_1_19_snapshot"}
		if snapshot, staticFound := indexer.ResolveOnActionSnapshotContract(args[0]); staticFound {
			result["snapshot_contract"] = snapshot
		}
		return printJSON(result)
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
	case "map":
		if len(args) < 1 {
			return errors.New("usage: ck3-index map <audit|province-mapping|physical-context|migration-snapshot|migrate|recipes|metric|route|render> [operation|spec.json] [--out path] [--meta sidecar.json]")
		}
		switch args[0] {
		case "audit":
			operation := "summary"
			if len(args) > 1 {
				operation = args[1]
			}
			cfg, err := indexer.LoadConfig(cfgPath)
			if err != nil {
				return err
			}
			result, err := indexer.AuditMapAssets(ctx, cfg, operation, 20)
			if err != nil {
				return err
			}
			return printJSON(result)
		case "province-mapping":
			if len(args) < 2 {
				return errors.New("usage: ck3-index map province-mapping <spec.json>")
			}
			var spec indexer.MapProvinceMappingSpec
			if err := readJSONFile(args[1], &spec); err != nil {
				return err
			}
			cfg, err := indexer.LoadConfig(cfgPath)
			if err != nil {
				return err
			}
			result, err := indexer.MapProvinceMapping(ctx, cfg, spec)
			if err != nil {
				return err
			}
			return printJSON(result)
		case "physical-context":
			if len(args) != 2 {
				return errors.New("usage: ck3-index map physical-context <spec.json>")
			}
			var request mapPhysicalContextCLIRequest
			if err := readStrictJSONFile(args[1], &request); err != nil {
				return err
			}
			limit, err := request.normalizedLimit()
			if err != nil {
				return err
			}
			db, err := openReadOnlyDB(cfgPath)
			if err != nil {
				return err
			}
			defer db.Close()
			result, err := db.LLMMapPhysicalContext(ctx, request.spec(), indexer.LLMOptions{AllowProject: true, Limit: limit})
			if err != nil {
				return err
			}
			return printJSON(result)
		case "migration-snapshot":
			if len(args) != 2 {
				return errors.New("usage: ck3-index map migration-snapshot <spec.json>")
			}
			var spec migrator.SnapshotSpec
			if err := readStrictJSONFile(args[1], &spec); err != nil {
				return err
			}
			cfg, err := indexer.LoadConfig(cfgPath)
			if err != nil {
				return err
			}
			result, err := migrator.CreateSnapshot(ctx, cfg, spec)
			if err != nil {
				return err
			}
			return printJSON(result)
		case "migrate":
			if len(args) < 2 || len(args) > 4 || (len(args) == 4 && args[2] != "--out") || len(args) == 3 {
				return errors.New("usage: ck3-index map migrate <spec.json> [--out <new-mod-dir>]")
			}
			var spec migrator.MigrationSpec
			if err := readStrictJSONFile(args[1], &spec); err != nil {
				return err
			}
			cfg, err := indexer.LoadConfig(cfgPath)
			if err != nil {
				return err
			}
			db, err := openReadOnlyDB(cfgPath)
			if err != nil {
				return err
			}
			defer db.Close()
			output := ""
			if len(args) == 4 {
				output = args[3]
			}
			result, err := migrator.BuildMigration(ctx, cfg, spec, migrator.BuildOptions{
				ArtifactRoot: cfg.ArtifactRoot, OutputDir: output, Retention: time.Duration(cfg.ArtifactRetentionHours) * time.Hour, DB: db,
			})
			if err != nil {
				return err
			}
			return printJSON(result)
		case "recipes":
			return printJSON(indexer.MapRecipeCatalog())
		case "metric":
			if len(args) < 2 {
				return errors.New("usage: ck3-index map metric <spec.json>")
			}
			var spec indexer.MapMetricSpec
			if err := readJSONFile(args[1], &spec); err != nil {
				return err
			}
			db, err := openReadOnlyDB(cfgPath)
			if err != nil {
				return err
			}
			defer db.Close()
			result, err := db.LLMMapBuildMetric(ctx, spec, indexer.LLMOptions{AllowProject: true, Limit: 20})
			if err != nil {
				return err
			}
			return printJSON(result)
		case "route":
			if len(args) != 2 {
				return errors.New("usage: ck3-index map route <spec.json>")
			}
			var spec indexer.MapRouteSpec
			if err := readStrictJSONFile(args[1], &spec); err != nil {
				return err
			}
			db, err := openReadOnlyDB(cfgPath)
			if err != nil {
				return err
			}
			defer db.Close()
			result, err := db.LLMMapRoute(ctx, spec, indexer.LLMOptions{AllowProject: true, Limit: 20})
			if err != nil {
				return err
			}
			return printJSON(result)
		case "render":
			if len(args) < 4 {
				return errors.New("usage: ck3-index map render <spec.json> --out <file.png> [--meta <sidecar.json>]")
			}
			outputPath, metadataPath := "", ""
			for index := 2; index < len(args); index += 2 {
				if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
					return errors.New("usage: ck3-index map render <spec.json> --out <file.png> [--meta <sidecar.json>]")
				}
				switch args[index] {
				case "--out":
					outputPath = args[index+1]
				case "--meta":
					metadataPath = args[index+1]
				default:
					return fmt.Errorf("unknown map render flag %q", args[index])
				}
			}
			if outputPath == "" {
				return errors.New("map render requires --out <file.png>")
			}
			var spec indexer.MapRenderSpec
			if err := readStrictJSONFile(args[1], &spec); err != nil {
				return err
			}
			db, err := openReadOnlyDB(cfgPath)
			if err != nil {
				return err
			}
			defer db.Close()
			result, err := db.LLMMapRender(ctx, spec, indexer.LLMOptions{AllowProject: true, Limit: 20})
			if err != nil {
				return err
			}
			if err := os.WriteFile(outputPath, result.PNG, 0644); err != nil {
				return err
			}
			result.PNG = nil
			if metadataPath != "" {
				data, err := json.MarshalIndent(result, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(metadataPath, append(data, '\n'), 0644); err != nil {
					return err
				}
			}
			return printJSON(result)
		default:
			return errors.New("usage: ck3-index map <audit|province-mapping|physical-context|migration-snapshot|migrate|recipes|metric|route|render> [operation|spec.json] [--out path]")
		}
	case "validate":
		db, err := openDB(ctx, cfgPath)
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
		dbPath, err := indexer.ConfiguredDatabasePath(cfg)
		if err != nil {
			return err
		}
		return mcpserver.Serve(ctx, cfg, dbPath, os.Stdin, os.Stdout)
	case "diag_stats":
		db, err := openDB(ctx, cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		return db.DiagStats(ctx)
	case "accuracy":
		dir := filepath.Join("testdata", "accuracy")
		if len(args) > 0 {
			dir = args[0]
		}
		report, err := indexer.RunAccuracy(ctx, dir)
		if err != nil {
			return err
		}
		return printJSON(report)
	case "bench":
		db, err := openReadOnlyDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		report, err := db.Bench(ctx)
		if err != nil {
			return err
		}
		return printJSON(report)
	case "health":
		db, err := openReadOnlyDB(cfgPath)
		if err != nil {
			return err
		}
		defer db.Close()
		cfg, err := indexer.LoadConfig(cfgPath)
		if err != nil {
			return err
		}
		report, err := db.HealthConfigured(ctx, cfg)
		if err != nil {
			return err
		}
		gis := db.GISSidecarStatus(ctx, cfg)
		report.GIS = &gis
		return printJSON(report)
	default:
		if strings.TrimSpace(cmd) == "" {
			printHelp()
			return nil
		}
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func openDB(ctx context.Context, cfgPath string) (*indexer.DB, error) {
	cfg, err := indexer.LoadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	dbPath, err := indexer.ConfiguredDatabasePath(cfg)
	if err != nil {
		return nil, err
	}
	db, err := indexer.Open(dbPath)
	if err != nil {
		return nil, err
	}
	if err := db.EnsureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func openReadOnlyDB(cfgPath string) (*indexer.DB, error) {
	cfg, err := indexer.LoadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	dbPath, err := indexer.ConfiguredDatabasePath(cfg)
	if err != nil {
		return nil, err
	}
	return indexer.OpenReadOnly(dbPath)
}

func readPatchInput(path string) (indexer.PreflightPatchInput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return indexer.PreflightPatchInput{}, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var input indexer.PreflightPatchInput
	if err := json.Unmarshal(data, &input); err != nil {
		return indexer.PreflightPatchInput{}, err
	}
	return input, nil
}

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	return json.Unmarshal(data, target)
}

func readStrictJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON input must contain exactly one value")
		}
		return err
	}
	return nil
}

func loadGUIInputs(paths []string) ([]indexer.GUIModelInput, error) {
	var files []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, path)
			continue
		}
		err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(candidate), ".gui") {
				files = append(files, candidate)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	inputs := make([]indexer.GUIModelInput, 0, len(files))
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, indexer.GUIModelInput{Path: filepath.ToSlash(path), Model: indexer.BuildGUIModel(string(data))})
	}
	return inputs, nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printHelp() {
	fmt.Println(`ck3-index commands:
  init [path]              write ck3-index.toml
	package <spec.json>      validate and build a portable CK3 mod ZIP from proposed files
	package-dir <dir> --meta validate and build a portable CK3 mod ZIP from an existing directory
  scan [--clean]           rebuild SQLite index (incremental by default; --clean drops everything)
  scan --files <paths...>  refresh current-project files and affected refs without full scan
  query object <id>        show object definitions and override chain
  refs <id>                show incoming/outgoing references
  loc <key>                show localization values
  resource <path-or-id>    show resource files and references
  gui <view> [...]         query active GUI or export bounded PNG/HTML previews with resolved runtime metadata
  gui-model <file.gui>     parse CK3 Jomini GUI into a renderer/editor-neutral tree
  gui-resolve <paths...>   recursively expand GUI templates, inheritance, custom children, and block overrides; supports --summary
  examples <type[:term]>   show vanilla-first examples for an object type
	  rules <type>             show self-owned schema fields learned from local .info files
	  rules audit              report live on_actions.log drift against the generated CK3 1.19 snapshot (read-only)
	  rules contracts          audit adjacent vanilla on_action comment contracts against live root evidence (read-only)
	  rules evidence           reconcile engine, the CK3 1.19 snapshot, and vanilla-comment on_action evidence (read-only)
	  override audit           compare unique top-level common/events definitions across configured source layers (read-only)
	  override compare <id>    compare one typed object against its upstream layer, including field drift (read-only)
  patterns <type>          show empirical field shapes learned from indexed scripts
  inspect <id>             show LLM-ready object summary, refs, loc, and diagnostics
  prepare-edit <id|type>   show LLM-ready examples, rules, and edit context
  preflight <id|type|path> show LLM-ready generation/edit blockers and warnings
  preflight-patch <json>   check proposed file contents without scanning or writing SQLite
  impact-patch <json>      summarize patch impact without scanning or writing SQLite
  preflight-dirty          preflight current dirty project files without scanning
  diagnose <id|key|path>   show LLM-ready object/loc/resource/ref diagnosis
  search <query>           semantic exact/prefix/FTS discovery before raw text search
  lookup-scope <key>       check local scope rule for a trigger/effect key
  lookup-datatype <key>    query engine logs/data_types signatures and return types
  lookup-shape <key>       show CK3 1.19 documented trigger/effect usage (not an exhaustive value grammar)
  lookup-define <key>      check if @define name exists in local define rules
  lookup-on-action <key>   check if on_action name is known in local rules
  lookup-iterator <key>    check if iterator/scope name is known in local rules
  lookup-example <key>     show local trigger/effect description and syntax example
	  lookup-modifier <key>    check if static modifier key is known in local rules
	  map audit [operation]    audit active province and river raster integrity
	  map province-mapping     compare two configured province maps and classify renumbers, splits, and merges
	  map physical-context     query normalized terrain, hydrology, water bodies, and relative bathymetry
	  map migration-snapshot  persist the old-upstream/current-project baseline before a map update
	  map migrate <json>       create a validated local fork from a saved snapshot and new upstream
	  map recipes              list thematic map recipes and supported fields/layers
  map metric <json>        build an auditable indexed/custom map metric
  map route <json>         resolve places and calculate a legal land, sea, or mixed route
  map render <json> --out  render a thematic or route-context PNG; add --meta for transform JSON
  validate                 run built-in read-only validation
  accuracy [dir]           run golden accuracy fixtures (default testdata/accuracy)
  bench                    benchmark hot LLM query paths and index plans
  health                   report DB/index/MCP health signals
  mcp                      serve read-only MCP tools over stdio

Use --config <path> before the command to select a config file.`)
}
