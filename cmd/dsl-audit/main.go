// dsl-audit produces reproducible coverage/evidence without any SQL database.
package main

import (
	"ck3-index/internal/indexer"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	root := flag.String("root", "", "CK3 game root to audit read-only")
	logs := flag.String("engine-logs", "", "optional engine documentation directory")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "--root is required")
		os.Exit(2)
	}
	c, err := indexer.NewStandaloneChecker(ctx, *logs)
	if err == nil {
		var report indexer.DSLAuditReport
		report, err = c.AuditDSL(ctx, *root)
		if err == nil {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			err = enc.Encode(report)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
