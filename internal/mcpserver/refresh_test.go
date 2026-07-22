package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ck3-index/internal/indexer"
)

func writeRefreshFixture(t *testing.T) (indexer.Config, *indexer.DB, string) {
	t.Helper()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	rel := "common/traits/refresh_trait.txt"
	path := filepath.Join(project, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("refresh_old_trait = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "ck3-index.toml")
	if err := os.WriteFile(cfgPath, []byte("database = \"cache/test.sqlite\"\n[[source]]\nname = \"project\"\npath = \"project\"\nrank = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := indexer.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Scan(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return cfg, db, path
}

func TestRefreshStatusAndTransactionalFiles(t *testing.T) {
	cfg, db, path := writeRefreshFixture(t)
	status := callToolForTest(t, db, cfg, "ck3_refresh", map[string]any{"operation": "status"})
	if status["isError"] == true {
		t.Fatalf("refresh status failed: %+v", status)
	}
	statusBody := status["structuredContent"].(map[string]any)
	refreshStatus := statusBody["refresh_status"].(map[string]any)
	if refreshStatus["status"] != "ready" || refreshStatus["full_scan_available"] != true {
		t.Fatalf("unexpected refresh status: %+v", refreshStatus)
	}
	project := refreshStatus["project"].(map[string]any)
	if project["configured"] != true || project["accessible"] != true || project["refreshable"] != true {
		t.Fatalf("refresh status did not report an accessible project source: %+v", project)
	}

	if err := os.WriteFile(path, []byte("refresh_new_trait = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := callToolForTest(t, db, cfg, "ck3_refresh", map[string]any{"operation": "files", "paths": []string{"common/traits/refresh_trait.txt"}})
	if result["isError"] == true {
		t.Fatalf("incremental refresh failed: %+v", result)
	}
	body := result["structuredContent"].(map[string]any)
	if body["operation"] != "files" || body["is_scanning"] != false {
		t.Fatalf("refresh files response did not finish cleanly: %+v", body)
	}
	refresh := body["refresh"].(map[string]any)
	if refresh["changed_files"] != float64(1) {
		t.Fatalf("changed_files = %v, want 1; response=%+v", refresh["changed_files"], refresh)
	}
	if _, ok := refresh["diagnostic_delta"].(map[string]any); !ok {
		t.Fatalf("refresh omitted diagnostic_delta: %+v", refresh)
	}
	outcomes := body["path_outcomes"].([]any)
	if len(outcomes) != 1 || outcomes[0].(map[string]any)["status"] != "refreshed" {
		t.Fatalf("refresh did not report the path outcome: %+v", outcomes)
	}
	changed := refresh["changed_symbols"].([]any)
	found := false
	for _, value := range changed {
		found = found || value == "trait:refresh_new_trait"
	}
	if !found {
		t.Fatalf("refresh did not report changed symbol: %+v", changed)
	}
	object, err := db.QueryObject(context.Background(), "refresh_new_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(object.Definitions) != 1 {
		t.Fatalf("incremental refresh did not publish replacement trait: %+v", object)
	}
}

func TestRefreshAndValidationErrorsUseStableStructuredContract(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "empty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertError := func(name string, args map[string]any, wantCode string) map[string]any {
		t.Helper()
		result := callToolForTest(t, db, indexer.Config{}, name, args)
		if result["isError"] != true {
			t.Fatalf("%s did not return an error: %+v", name, result)
		}
		structured := result["structuredContent"].(map[string]any)
		if structured["code"] != wantCode || structured["category"] == "" || structured["message"] == "" {
			t.Fatalf("%s error contract = %+v, want code=%s", name, structured, wantCode)
		}
		if _, ok := structured["retryable"].(bool); !ok {
			t.Fatalf("%s error has no boolean retryable: %+v", name, structured)
		}
		if _, ok := structured["details"].(map[string]any); !ok {
			t.Fatalf("%s error has no details object: %+v", name, structured)
		}
		if _, ok := structured["recovery"].(map[string]any); !ok {
			t.Fatalf("%s error has no recovery object: %+v", name, structured)
		}
		return structured
	}
	assertError("ck3_search", map[string]any{}, "MISSING_REQUIRED_ARGUMENT")
	assertError("ck3_refresh", map[string]any{"operation": "files"}, "MISSING_REQUIRED_ARGUMENT")
	assertError("ck3_refresh", map[string]any{"operation": "files", "paths": []string{"common/traits/x.txt"}}, "INDEX_NOT_READY")
}

func TestWorkspaceCapabilitiesAreAvailableBeforeFirstScan(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "empty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result := callToolForTest(t, db, indexer.Config{}, "ck3_workspace", map[string]any{"operation": "capabilities", "domain": "semantic"})
	if result["isError"] == true {
		t.Fatalf("capabilities should be available before a full index: %+v", result)
	}
	body := result["structuredContent"].(map[string]any)
	if body["operation"] != "capabilities" || body["domain"] != "semantic" {
		t.Fatalf("unexpected capability response: %+v", body)
	}
	items := body["capabilities"].([]any)
	if len(items) != 1 {
		t.Fatalf("semantic capability filter = %+v", items)
	}
	item := items[0].(map[string]any)
	if item["id"] != "semantic_search" || item["available"] != false || item["reason"] == "" || item["requires_ready_index"] != true || item["side_effect"] != "none" || item["profile"] != "default" {
		t.Fatalf("capability did not report unavailable index explicitly: %+v", item)
	}
}

func TestRefreshCapabilityReportsFullAsAvailable(t *testing.T) {
	db, err := indexer.Open(filepath.Join(t.TempDir(), "empty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result := callToolForTest(t, db, indexer.Config{}, "ck3_workspace", map[string]any{"operation": "capabilities", "domain": "workspace"})
	if result["isError"] == true {
		t.Fatalf("workspace capability query failed: %+v", result)
	}
	items := result["structuredContent"].(map[string]any)["capabilities"].([]any)
	var refresh map[string]any
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["id"] == "index_refresh" {
			refresh = item
			break
		}
	}
	if refresh == nil {
		t.Fatalf("index_refresh missing from workspace capabilities: %+v", items)
	}
	var full map[string]any
	for _, raw := range refresh["mode_details"].([]any) {
		item := raw.(map[string]any)
		if item["id"] == "full" {
			full = item
			break
		}
	}
	if full == nil || full["available"] != true {
		t.Fatalf("full availability was not explicit: %+v", refresh)
	}
}

func TestRefreshFullPublishesStagedGeneration(t *testing.T) {
	ctx := context.Background()
	cfg, db, path := writeRefreshFixture(t)
	before, err := db.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("refresh_full_trait = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := callToolForTest(t, db, cfg, "ck3_refresh", map[string]any{"operation": "full"})
	if result["isError"] == true {
		t.Fatalf("full staged refresh failed: %+v", result)
	}
	body := result["structuredContent"].(map[string]any)
	if body["operation"] != "full" || body["is_scanning"] != false {
		t.Fatalf("full refresh response did not finish cleanly: %+v", body)
	}
	after, err := db.IndexState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Ready() || after.Generation != before.Generation+1 || after.Revision == before.Revision {
		t.Fatalf("full refresh did not publish a new generation: before=%+v after=%+v", before, after)
	}
	object, err := db.QueryObject(ctx, "refresh_full_trait")
	if err != nil {
		t.Fatal(err)
	}
	if len(object.Definitions) != 1 {
		t.Fatalf("full refresh did not publish replacement source: %+v", object)
	}
}

func TestToolErrorsUseCancellationAndTimeoutCodes(t *testing.T) {
	for _, tt := range []struct {
		err  error
		code string
	}{
		{err: context.Canceled, code: ErrorOperationCancelled},
		{err: context.DeadlineExceeded, code: ErrorOperationTimeout},
		{err: errors.New("other"), code: ErrorInternal},
	} {
		result := encodeToolError(tt.err, nil)
		body := result["structuredContent"].(map[string]any)
		if body["code"] != tt.code || result["isError"] != true {
			t.Fatalf("error %v encoded as %+v, want %s", tt.err, body, tt.code)
		}
	}
}

func TestPublicVisibilityHonorsExplicitSourcePrivacyPolicy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	game := filepath.Join(dir, "game")
	write := func(root, rel, contents string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(project, "common/traits/private_policy_trait.txt", "private_policy_trait = {}\n")
	write(game, "common/traits/public_policy_trait.txt", "public_policy_trait = {}\n")
	cfg := indexer.Config{
		ConfigPath: filepath.Join(dir, "ck3-index.toml"),
		Database:   "cache/test.sqlite",
		Sources: []indexer.Source{
			{Name: "project-layer", Path: project, Rank: 4, Role: indexer.SourceRoleProject, Private: true},
			{Name: "game-layer", Path: game, Rank: 8, Role: indexer.SourceRoleGame, Private: false},
		},
	}
	if _, err := indexer.Scan(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := indexer.Open(filepath.Join(dir, "cache", "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	privateResult := callToolForTest(t, db, cfg, "ck3_inspect", map[string]any{"id": "private_policy_trait", "visibility": "public"})
	if privateResult["isError"] == true {
		t.Fatalf("public private-source query failed: %+v", privateResult)
	}
	privateBody := privateResult["structuredContent"].(map[string]any)
	privateEvidence, _ := privateBody["evidence"].([]any)
	if len(privateEvidence) != 0 {
		t.Fatalf("private source leaked into public output: %+v", privateBody)
	}
	publicResult := callToolForTest(t, db, cfg, "ck3_inspect", map[string]any{"id": "public_policy_trait", "visibility": "public"})
	if publicResult["isError"] == true {
		t.Fatalf("public game-source query failed: %+v", publicResult)
	}
	publicBody := publicResult["structuredContent"].(map[string]any)
	evidence := publicBody["evidence"].([]any)
	foundGameDefinition := false
	for _, raw := range evidence {
		item := raw.(map[string]any)
		if item["kind"] == "definition" && item["source"] == "game-layer" {
			foundGameDefinition = true
		}
	}
	if !foundGameDefinition {
		t.Fatalf("explicitly public source was redacted: %+v", publicBody)
	}
	workspace := callToolForTest(t, db, cfg, "ck3_workspace", map[string]any{"operation": "overview", "visibility": "public"})
	if workspace["isError"] == true {
		t.Fatalf("public workspace overview failed: %+v", workspace)
	}
	workspaceData, err := json.Marshal(workspace["structuredContent"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(workspaceData), "project-layer") || !strings.Contains(string(workspaceData), "game-layer") {
		t.Fatalf("workspace public filtering did not remove only private provenance: %s", workspaceData)
	}
	mapResult := callToolForTest(t, db, cfg, "map_title_context", map[string]any{"id": "c_private", "visibility": "public"})
	if mapResult["isError"] != true || mapResult["structuredContent"].(map[string]any)["code"] != ErrorInvalidArguments {
		t.Fatalf("public map-cache request was not safely rejected: %+v", mapResult)
	}
}
