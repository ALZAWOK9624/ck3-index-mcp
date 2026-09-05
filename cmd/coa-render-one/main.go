package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"ck3-index/internal/indexer"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: coa-render-one <config> <id> <out.png> <size>")
		os.Exit(2)
	}
	cfg, err := indexer.LoadConfig(os.Args[1])
	if err != nil {
		panic(err)
	}
	dbPath, err := indexer.ConfiguredDatabasePath(cfg)
	if err != nil {
		panic(err)
	}
	db, err := indexer.OpenReadOnlyWithOptions(dbPath, cfg.SQLiteReadOptions())
	if err != nil {
		panic(err)
	}
	defer db.Close()
	size, err := strconv.Atoi(os.Args[4])
	if err != nil {
		panic(err)
	}
	result, err := db.LLMCoatOfArms(context.Background(), indexer.CoatOfArmsSpec{
		Operation: "render",
		ID:        os.Args[2],
		Size:      size,
	}, indexer.LLMOptions{AllowProject: true})
	if err != nil {
		panic(err)
	}
	if len(result.PNG) == 0 {
		panic("renderer returned no PNG")
	}
	if err := os.WriteFile(os.Args[3], result.PNG, 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("%s\n", result.Summary)
}
