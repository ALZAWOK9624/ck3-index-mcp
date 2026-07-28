package migrator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ck3-index/internal/indexer"
)

func TestWriteRebaseReportUsesStableClassificationValues(t *testing.T) {
	root := t.TempDir()
	transaction := RebaseTransaction{
		SchemaVersion: RebaseSchemaVersion,
		ID:            "ck3-rebase-0123456789abcdef",
		Status:        RebaseStatusNeedsReview,
		Counts: map[string]int{
			"both_changed":    2,
			"action_conflict": 2,
		},
		Files: []RebaseFileDecision{{
			Path:           "common/culture/test.txt",
			Classification: "both_changed",
			Adapter:        "jomini_objects",
			Action:         "write_candidate",
			Reason:         "non-overlapping semantic fields",
			Safe:           true,
			Base:           RebaseFileState{Exists: true, SHA256: "base-decision-sha"},
			Project:        RebaseFileState{Exists: true, SHA256: "project-decision-sha"},
			Target:         RebaseFileState{Exists: true, SHA256: "target-decision-sha"},
			Result:         RebaseFileState{Exists: true, SHA256: "result-decision-sha"},
			SemanticIDs:    []string{"culture:test"},
			ConflictIDs:    []string{"ck3-rebase-conflict-0123456789abcdef"},
		}},
		Conflicts: []RebaseConflict{{
			ID:             "ck3-rebase-conflict-0123456789abcdef",
			Code:           "semantic_overlap",
			Path:           "common/culture/test.txt",
			Message:        "both sides changed the same object",
			AllowedActions: []string{"keep_project", "use_target"},
		}},
	}
	if err := writeRebaseReport(root, transaction); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, transaction.ID, rebaseReportName))
	if err != nil {
		t.Fatal(err)
	}
	report := string(data)
	if !strings.Contains(report, "both_changed") || !strings.Contains(report, ">2</td>") {
		t.Fatalf("report did not render the real classification count:\n%s", report)
	}
	if strings.Contains(report, "$key") {
		t.Fatalf("report retained a template placeholder instead of a classification key:\n%s", report)
	}
	if !strings.Contains(report, "Content-Security-Policy") || !strings.Contains(report, "semantic_overlap") {
		t.Fatalf("report omitted CSP or conflict content:\n%s", report)
	}
	for _, expected := range []string{
		"Three-way decision and semantic evidence",
		"Coordinate map deltas",
		"Pixel conflict spatial evidence",
		"Game-version mode",
		"base-decision-sha",
		"project-decision-sha",
		"target-decision-sha",
		"result-decision-sha",
		"culture:test",
		"write_candidate",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("report omitted decision evidence %q:\n%s", expected, report)
		}
	}
}

func TestRebaseReviewServesHashPinnedCoordinateMapDelta(t *testing.T) {
	root := t.TempDir()
	cfg := indexer.Config{ArtifactRoot: filepath.Join(root, "artifacts")}
	rebaseRoot, err := rebaseArtifactRoot(cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	transaction := RebaseTransaction{
		SchemaVersion: RebaseSchemaVersion,
		ID:            "ck3-rebase-0123456789abcdef",
		Status:        RebaseStatusReadyToBuild,
		Counts:        map[string]int{},
		Files: []RebaseFileDecision{{
			Path:    "map_data/provinces.png",
			Adapter: "map_coordinate_delta",
			Action:  "write_candidate",
		}},
	}
	patch := testRebaseRaster(1, 1, 0xff, 0x00, 0x00, 0xff)
	patchData := mustEncodeTestRebasePNG(t, patch)
	if err := storeRebaseMapDelta(rebaseRoot, transaction.ID, transaction.Files[0].Path, patchData, RebaseMapDelta{
		OriginX: 0, OriginY: 0, Width: 1, Height: 1, ChangedPixels: 1,
	}, &transaction.Files[0]); err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseDecisions(rebaseRoot, transaction.ID, transaction.Files); err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseTransaction(rebaseRoot, &transaction); err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseResolutions(rebaseRoot, transaction.ID, nil); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	review, err := StartRebaseReview(ctx, cfg, transaction.ID, RebaseOptions{}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer review.Close()
	response, err := http.Get(rebaseReviewEndpoint(t, review, "/"+transaction.Files[0].MapDelta.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, patchData) {
		t.Fatalf("map delta response status=%d content-type=%q bytes_match=%v", response.StatusCode, response.Header.Get("Content-Type"), bytes.Equal(body, patchData))
	}
	report, err := os.ReadFile(filepath.Join(rebaseRoot, transaction.ID, rebaseReportName))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{transaction.Files[0].MapDelta.Path, transaction.Files[0].MapDelta.SHA256, "transparent Base-to-Ours"} {
		if !strings.Contains(string(report), expected) {
			t.Fatalf("report omitted map delta evidence %q", expected)
		}
	}
}

func TestRebaseReviewServerValidatesAndPersistsResolutions(t *testing.T) {
	root := t.TempDir()
	artifactRoot := filepath.Join(root, "artifacts")
	cfg := indexer.Config{ArtifactRoot: artifactRoot}
	transaction := RebaseTransaction{
		SchemaVersion: RebaseSchemaVersion,
		ID:            "ck3-rebase-0123456789abcdef",
		Status:        RebaseStatusNeedsReview,
		Counts:        map[string]int{"both_changed": 1},
		Conflicts: []RebaseConflict{{
			ID:             "ck3-rebase-conflict-0123456789abcdef",
			Code:           "semantic_overlap",
			Message:        "choose one side",
			AllowedActions: []string{"keep_project", "use_target"},
		}},
	}
	rebaseRoot, err := rebaseArtifactRoot(cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseDecisions(rebaseRoot, transaction.ID, transaction.Files); err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseTransaction(rebaseRoot, &transaction); err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseResolutions(rebaseRoot, transaction.ID, nil); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	review, err := StartRebaseReview(ctx, cfg, transaction.ID, RebaseOptions{}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer review.Close()

	response, err := http.Get(rebaseReviewEndpoint(t, review, "/api/transaction"))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("transaction API status = %d", response.StatusCode)
	}
	response.Body.Close()

	valid, err := json.Marshal([]RebaseResolution{{ConflictID: transaction.Conflicts[0].ID, Action: "keep_project"}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, rebaseReviewEndpoint(t, review, "/api/resolutions"), strings.NewReader(string(valid)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CK3-Rebase-Token", review.Token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("valid resolution status = %d", response.StatusCode)
	}
	response.Body.Close()
	resolutions, err := loadRebaseResolutions(rebaseRoot, transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolutions) != 1 || resolutions[0].Action != "keep_project" {
		t.Fatalf("saved resolutions = %+v", resolutions)
	}

	invalid, err := json.Marshal([]RebaseResolution{{ConflictID: transaction.Conflicts[0].ID, Action: "not_allowed"}})
	if err != nil {
		t.Fatal(err)
	}
	request, err = http.NewRequest(http.MethodPost, rebaseReviewEndpoint(t, review, "/api/resolutions"), strings.NewReader(string(invalid)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CK3-Rebase-Token", review.Token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest {
		response.Body.Close()
		t.Fatalf("invalid resolution status = %d", response.StatusCode)
	}
	response.Body.Close()
}

func TestRebaseReviewServerImportsManualCandidateWithCapability(t *testing.T) {
	root := t.TempDir()
	cfg := indexer.Config{ArtifactRoot: filepath.Join(root, "artifacts")}
	transaction := RebaseTransaction{
		SchemaVersion: RebaseSchemaVersion,
		ID:            "ck3-rebase-0123456789abcdef",
		Status:        RebaseStatusNeedsReview,
		Conflicts: []RebaseConflict{{
			ID:             "ck3-rebase-conflict-0123456789abcdef",
			Code:           "semantic_overlap",
			AllowedActions: []string{"manual"},
		}},
	}
	rebaseRoot, err := rebaseArtifactRoot(cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseDecisions(rebaseRoot, transaction.ID, transaction.Files); err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseTransaction(rebaseRoot, &transaction); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	review, err := StartRebaseReview(ctx, cfg, transaction.ID, RebaseOptions{}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer review.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("conflict_id", transaction.Conflicts[0].ID); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "candidate.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("culture_a = { ethos = manual }\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	payload := append([]byte(nil), body.Bytes()...)
	request, err := http.NewRequest(http.MethodPost, rebaseReviewEndpoint(t, review, "/api/manual"), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		response.Body.Close()
		t.Fatalf("manual upload without capability status=%d", response.StatusCode)
	}
	response.Body.Close()

	request, err = http.NewRequest(http.MethodPost, rebaseReviewEndpoint(t, review, "/api/manual"), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CK3-Rebase-Token", review.Token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("manual upload with capability status=%d", response.StatusCode)
	}
	var upload rebaseManualUploadResult
	if err := json.NewDecoder(response.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}
	if upload.ManualPath == "" || upload.SHA256 == "" || upload.Size == 0 {
		t.Fatalf("manual upload response incomplete: %+v", upload)
	}
	if _, err := rebaseCandidateFile(rebaseRoot, transaction.ID, upload.ManualPath); err != nil {
		t.Fatalf("manual upload did not create a transaction-scoped candidate: %v", err)
	}
}

func TestRebaseReviewWaitPreservesServeError(t *testing.T) {
	root := t.TempDir()
	cfg := indexer.Config{ArtifactRoot: filepath.Join(root, "artifacts")}
	transaction := RebaseTransaction{
		SchemaVersion: RebaseSchemaVersion,
		ID:            "ck3-rebase-0123456789abcdef",
		Status:        RebaseStatusNeedsReview,
	}
	rebaseRoot, err := rebaseArtifactRoot(cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseDecisions(rebaseRoot, transaction.ID, transaction.Files); err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseTransaction(rebaseRoot, &transaction); err != nil {
		t.Fatal(err)
	}
	review, err := StartRebaseReview(context.Background(), cfg, transaction.ID, RebaseOptions{}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer review.Close()
	if err := review.listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := review.Wait(ctx); err == nil {
		t.Fatal("Wait hid the underlying review server Serve error")
	}
}

func TestRebaseReviewServerBuildsPinnedPixelConflictMask(t *testing.T) {
	root := t.TempDir()
	baseRoot := filepath.Join(root, "base")
	projectRoot := filepath.Join(root, "project")
	targetRoot := filepath.Join(root, "target")

	base := testRebaseRaster(2, 1,
		0x00, 0x00, 0x00, 0xff,
		0x00, 0x00, 0x00, 0xff,
	)
	project := cloneTestRebaseRaster(base)
	setTestRebasePixel(&project, 0, 0, 0xff, 0x00, 0x00, 0xff)
	target := cloneTestRebaseRaster(base)
	setTestRebasePixel(&target, 0, 0, 0x00, 0x00, 0xff, 0xff)
	setTestRebasePixel(&target, 1, 0, 0x00, 0xff, 0x00, 0xff)
	baseData := mustEncodeTestRebasePNG(t, base)
	projectData := mustEncodeTestRebasePNG(t, project)
	targetData := mustEncodeTestRebasePNG(t, target)
	const rel = "gfx/map/test.png"
	writeRebaseReviewTestSourceFile(t, baseRoot, rel, baseData)
	writeRebaseReviewTestSourceFile(t, projectRoot, rel, projectData)
	writeRebaseReviewTestSourceFile(t, targetRoot, rel, targetData)

	cfg := indexer.Config{
		ArtifactRoot: filepath.Join(root, "artifacts"),
		Sources: []indexer.Source{
			{Name: "project", Path: projectRoot, Rank: 1, Role: indexer.SourceRoleProject},
			{Name: "base", Path: baseRoot, Rank: 2, Role: indexer.SourceRoleReference},
			{Name: "target", Path: targetRoot, Rank: 3, Role: indexer.SourceRoleReference},
		},
	}
	transaction := RebaseTransaction{
		SchemaVersion: RebaseSchemaVersion,
		ID:            "ck3-rebase-0123456789abcdef",
		Status:        RebaseStatusNeedsReview,
		Profile: RebaseProfile{
			SchemaVersion: RebaseSchemaVersion,
			Project:       "project",
			Base:          "base",
			Target:        "target",
		},
		Files: []RebaseFileDecision{{
			Path:           rel,
			Classification: "both_changed",
			Adapter:        "png_pixels",
			Action:         "conflict",
			Safe:           false,
			Base:           RebaseFileState{Exists: true, SHA256: hashBytes(baseData), Size: int64(len(baseData))},
			Project:        RebaseFileState{Exists: true, SHA256: hashBytes(projectData), Size: int64(len(projectData))},
			Target:         RebaseFileState{Exists: true, SHA256: hashBytes(targetData), Size: int64(len(targetData))},
		}},
		Conflicts: []RebaseConflict{{
			ID:             "ck3-rebase-conflict-pixel-mask-0123456789abcdef",
			Code:           "pixel_merge_conflict",
			Path:           rel,
			Message:        "project and target changed 1 pixel(s) differently",
			AllowedActions: []string{"keep_project", "use_target", "manual"},
		}},
	}
	rebaseRoot, err := rebaseArtifactRoot(cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseDecisions(rebaseRoot, transaction.ID, transaction.Files); err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseTransaction(rebaseRoot, &transaction); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	review, err := StartRebaseReview(ctx, cfg, transaction.ID, RebaseOptions{}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer review.Close()

	maskPath, err := rebaseReviewPixelMaskRelativePath(transaction.Conflicts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(rebaseReviewEndpoint(t, review, "/"+maskPath))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "image/png" {
		response.Body.Close()
		t.Fatalf("pixel mask response = status %d content-type %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	maskData, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	mask, err := decodeRebasePNG(maskData)
	if err != nil {
		t.Fatal(err)
	}
	assertTestRebasePixel(t, mask, 0, 0, 0xff, 0x00, 0xff, 0xff)
	assertTestRebasePixel(t, mask, 1, 0, 0x00, 0x00, 0x00, 0x00)
	response, err = http.Get(rebaseReviewEndpoint(t, review, "/pixel-conflicts/transaction.json"))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound {
		response.Body.Close()
		t.Fatalf("arbitrary pixel artifact status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
	response.Body.Close()

	report, err := os.ReadFile(filepath.Join(rebaseRoot, transaction.ID, rebaseReportName))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{maskPath, hashBytes(baseData), hashBytes(projectData), hashBytes(targetData), "Magenta mask pixels"} {
		if !strings.Contains(string(report), expected) {
			t.Fatalf("report omitted pixel evidence %q:\n%s", expected, report)
		}
	}
}

func TestRebaseReviewPixelMaskWithholdsDriftedInputs(t *testing.T) {
	root := t.TempDir()
	baseRoot := filepath.Join(root, "base")
	projectRoot := filepath.Join(root, "project")
	targetRoot := filepath.Join(root, "target")
	base := testRebaseRaster(1, 1, 0x00, 0x00, 0x00, 0xff)
	project := cloneTestRebaseRaster(base)
	setTestRebasePixel(&project, 0, 0, 0xff, 0x00, 0x00, 0xff)
	target := cloneTestRebaseRaster(base)
	setTestRebasePixel(&target, 0, 0, 0x00, 0x00, 0xff, 0xff)
	baseData := mustEncodeTestRebasePNG(t, base)
	projectData := mustEncodeTestRebasePNG(t, project)
	targetData := mustEncodeTestRebasePNG(t, target)
	const rel = "gfx/map/test.png"
	writeRebaseReviewTestSourceFile(t, baseRoot, rel, baseData)
	writeRebaseReviewTestSourceFile(t, projectRoot, rel, projectData)
	// Deliberately write a later target state while pinning the earlier
	// target hash in the transaction.  A count must not be converted into a
	// spatial map from this drifted source.
	drifted := cloneTestRebaseRaster(target)
	setTestRebasePixel(&drifted, 0, 0, 0x00, 0xff, 0x00, 0xff)
	writeRebaseReviewTestSourceFile(t, targetRoot, rel, mustEncodeTestRebasePNG(t, drifted))

	cfg := indexer.Config{
		ArtifactRoot: filepath.Join(root, "artifacts"),
		Sources: []indexer.Source{
			{Name: "project", Path: projectRoot, Rank: 1, Role: indexer.SourceRoleProject},
			{Name: "base", Path: baseRoot, Rank: 2, Role: indexer.SourceRoleReference},
			{Name: "target", Path: targetRoot, Rank: 3, Role: indexer.SourceRoleReference},
		},
	}
	transaction := RebaseTransaction{
		SchemaVersion: RebaseSchemaVersion,
		ID:            "ck3-rebase-0123456789abcdef",
		Status:        RebaseStatusNeedsReview,
		Profile:       RebaseProfile{SchemaVersion: RebaseSchemaVersion, Project: "project", Base: "base", Target: "target"},
		Files: []RebaseFileDecision{{
			Path: rel, Adapter: "png_pixels", Action: "conflict",
			Base:    RebaseFileState{Exists: true, SHA256: hashBytes(baseData)},
			Project: RebaseFileState{Exists: true, SHA256: hashBytes(projectData)},
			Target:  RebaseFileState{Exists: true, SHA256: hashBytes(targetData)},
		}},
		Conflicts: []RebaseConflict{{
			ID: "ck3-rebase-conflict-pixel-drift-0123456789abcdef", Code: "pixel_merge_conflict", Path: rel,
			Message: "project and target changed 1 pixel(s) differently", AllowedActions: []string{"manual"},
		}},
	}
	rebaseRoot, err := rebaseArtifactRoot(cfg, RebaseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseDecisions(rebaseRoot, transaction.ID, transaction.Files); err != nil {
		t.Fatal(err)
	}
	if err := writeRebaseTransaction(rebaseRoot, &transaction); err != nil {
		t.Fatal(err)
	}
	if err := materializeRebaseReviewPixelMasks(cfg, rebaseRoot, transaction); err == nil {
		t.Fatal("drifted source unexpectedly produced a pixel conflict mask")
	}
	if _, err := readRebaseReviewPixelMask(rebaseRoot, transaction.ID, transaction.Conflicts[0].ID); !os.IsNotExist(err) {
		t.Fatalf("drifted source left a mask behind: %v", err)
	}
}

func writeRebaseReviewTestSourceFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rebaseReviewEndpoint(t *testing.T, review *RebaseReviewServer, endpoint string) string {
	t.Helper()
	parsed, err := url.Parse(review.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = endpoint
	return parsed.String()
}
