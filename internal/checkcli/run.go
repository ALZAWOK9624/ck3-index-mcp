package checkcli

import (
	"ck3-index/internal/indexer"
	"ck3-index/internal/mcpserver"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
)

var ErrCheckFailed = errors.New("static check found errors")

// Run takes complete virtual files as JSON on stdin. There is deliberately no
// config/database fallback and no flag that reads a bot-provided source path.
func Run(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) error {
	flags := flag.NewFlagSet("ck3-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	logs := flags.String("engine-logs", "", "administrator-selected engine docs; default uses compiled rules")
	serve := flags.Bool("serve", false, "serve only ck3_check over MCP stdio")
	rule := flags.String("rule", "", "describe one documented command without SQL")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional argument; pass {files:[{path,content}]} JSON on stdin")
	}
	checker, err := indexer.NewStandaloneChecker(ctx, *logs)
	if err != nil {
		return err
	}
	if *serve {
		if *rule != "" {
			return fmt.Errorf("--rule cannot be combined with --serve")
		}
		return mcpserver.ServeCheck(ctx, checker, in, out)
	}
	if *rule != "" {
		return json.NewEncoder(out).Encode(checker.Rule(*rule))
	}
	// Escaped JSON may be up to six times larger than decoded UTF-8 source.
	reader := &io.LimitedReader{R: in, N: (128 << 20) + 1}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var request indexer.CheckRequest
	if err := decoder.Decode(&request); err != nil {
		return fmt.Errorf("invalid check JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("expected exactly one JSON request")
	}
	if reader.N == 0 {
		return fmt.Errorf("check JSON exceeds the 128 MiB envelope limit")
	}
	result, err := checker.Check(ctx, request)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return err
	}
	if !result.Passed {
		return ErrCheckFailed
	}
	return nil
}
