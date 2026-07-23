package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalizationLintDetectsErrorLogSyntaxAndAcceptsVanillaMacros(t *testing.T) {
	content := "" +
		"l_english:\n" +
		"good:0 \"[Concept('war', 'war')|E]\"\n" +
		"bad_macro:0 \"[Concept('agot_rebels', '叛军领袖)\"\n" +
		"bad_bracket:0 \"unclosed [concept\"\n" +
		"bad_character:0 \"bad�text\"\n" +
		"bad_entry:0 \"unterminated\n"
	analysis, err := AnalyzeVirtualFile("localization/english/error_log_l_english.yml", "patch", 1, content)
	if err != nil {
		t.Fatal(err)
	}
	if !runtimeContractHasCode(analysis.Diagnostics, "localization_macro_syntax") ||
		!runtimeContractHasCode(analysis.Diagnostics, "localization_invalid_character") ||
		!runtimeContractHasCode(analysis.Diagnostics, "localization_entry_syntax") {
		t.Fatalf("expected localization syntax diagnostics, got:\n%s", runtimeContractDetails(analysis.Diagnostics))
	}
	for _, diagnostic := range analysis.Diagnostics {
		if diagnostic.Line == 2 {
			t.Fatalf("valid vanilla-style Concept macro was rejected: %+v", diagnostic)
		}
	}
	selector, err := AnalyzeVirtualFile("localization/english/error_log_selector_l_english.yml", "patch", 1, `target_character_liege: "[Select_CString(TARGET_CHARACTER.IsIndependentRuler, \"\", TARGET_CHARACTER.GetLiege.GetName)][Select_CString(TARGET_CHARACTER.IsIndependentRuler, 'Independent', 'Vassal of')]"`)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeContractHasCode(selector.Diagnostics, "localization_macro_syntax") {
		t.Fatalf("valid escaped Select_CString macro was rejected: %s", runtimeContractDetails(selector.Diagnostics))
	}
	possessive, err := AnalyzeVirtualFile("localization/english/error_log_possessive_l_english.yml", "patch", 1, `notice: "[SelectLocalization(Scope.IsValid, 'are', 'this subject\\'s')]"`)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeContractHasCode(possessive.Diagnostics, "localization_macro_syntax") {
		t.Fatalf("valid doubled-backslash apostrophe macro was rejected: %s", runtimeContractDetails(possessive.Diagnostics))
	}
	controlCharacters, err := AnalyzeVirtualFile(
		"localization/english/error_log_control_l_english.yml",
		"patch",
		1,
		"l_english:\ncomment_only:0 \"A'er\" # (\x0e阿儿)\nvalue_error:0 \"bad\x0evalue\"\n",
	)
	if err != nil {
		t.Fatal(err)
	}
	valueErrorFound := false
	for _, diagnostic := range controlCharacters.Diagnostics {
		if diagnostic.Code != "localization_invalid_character" {
			continue
		}
		if diagnostic.Line == 2 {
			t.Fatalf("trailing-comment control byte was rejected: %+v", diagnostic)
		}
		if diagnostic.Line == 3 {
			valueErrorFound = true
		}
	}
	if !valueErrorFound {
		t.Fatalf("control byte inside a localization value was not rejected: %s", runtimeContractDetails(controlCharacters.Diagnostics))
	}
}

func TestDecisionPictureContractRequiresReference(t *testing.T) {
	missing, err := AnalyzeVirtualFile("common/decisions/error_log_decisions.txt", "patch", 1, `missing_picture_decision = {
	ai_check_interval = 1
}
`)
	if err != nil {
		t.Fatal(err)
	}
	if !runtimeContractHasCode(missing.Diagnostics, "decision_picture_missing") {
		t.Fatalf("missing decision picture was not detected: %s", runtimeContractDetails(missing.Diagnostics))
	}
	valid, err := AnalyzeVirtualFile("common/decisions/error_log_decisions_valid.txt", "patch", 1, `valid_picture_decision = {
	picture = {
		reference = "gfx/interface/illustrations/decisions/decision_dynasty_house.dds"
	}
	ai_check_interval = 1
}
`)
	if err != nil {
		t.Fatal(err)
	}
	if runtimeContractHasCode(valid.Diagnostics, "decision_picture_missing") {
		t.Fatalf("valid decision picture was rejected: %s", runtimeContractDetails(valid.Diagnostics))
	}
}

func TestErrorLogHistoryNameAndVariableContracts(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "error-log-contracts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	historyPath := filepath.Join(t.TempDir(), "history.txt")
	if err := os.WriteFile(historyPath, []byte("dzn_embermaw = {\n\tname = Embermaw\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden)
		VALUES(1,'project',1,?,'history/characters/error_log.txt','script',0,'history',0)`, historyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO objects(id,object_type,name,file_id,source_name,source_rank,path,line,col)
		VALUES(1,'character','dzn_embermaw',1,'project',1,?,1,1)`, historyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO object_fields(id,object_type,object_name,field,value_shape,date_key,file_id,source_name,source_rank,path,line,raw)
		VALUES(1,'character','dzn_embermaw','name','atom',0,1,'project',1,?,2,'name = Embermaw')`, historyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO refs(id,from_object_type,from_object_name,ref_kind,ref_name,file_id,line,col,raw,relation)
		VALUES(1,'event','error_log','variable','dzn_nest_manager',1,8,2,'dzn_nest_manager','set_variable')`); err != nil {
		t.Fatal(err)
	}

	refresh := func() {
		t.Helper()
		tx, beginErr := db.sql.BeginTx(ctx, nil)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if validationErr := refreshErrorLogContractDiagnostics(ctx, tx, 1); validationErr != nil {
			tx.Rollback()
			t.Fatal(validationErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	refresh()
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='history_character_name_localization_missing'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("history name diagnostic=%d, want 1", count)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='variable_write_only'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("write-only variable diagnostic=%d, want 1", count)
	}

	if _, err := db.sql.Exec(`INSERT INTO localization(key,language,value,file_id,source_name,source_rank,path,line,replace_dir)
		VALUES('Embermaw','english','Embermaw',1,'project',1,?,1,0)`, historyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO refs(id,from_object_type,from_object_name,ref_kind,ref_name,file_id,line,col,raw,relation)
		VALUES(2,'event','error_log','variable','dzn_nest_manager',1,9,2,'dzn_nest_manager','read_variable')`); err != nil {
		t.Fatal(err)
	}
	refresh()
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code IN ('history_character_name_localization_missing','variable_write_only')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("resolved error-log contracts still reported: %d", count)
	}
}

func TestVariableWriteOnlyUsesAllLayersGlobalRefsAndLocalizationReads(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "variable-reads.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden)
		 VALUES(1,'project',1,'project.txt','events/project.txt','script',0,'project',0)`,
		`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden)
		 VALUES(2,'dependency',2,'dependency.txt','events/dependency.txt','script',0,'dependency',0)`,
		`INSERT INTO files(id,source_name,source_rank,path,rel_path,kind,mtime,sha256,overridden)
		 VALUES(3,'dependency',2,'dependency.yml','localization/english/dependency.yml','localization',0,'localization',0)`,
		`INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,line,col,raw,relation)
		 VALUES('event','writer','variable','cross_layer_read',1,1,1,'cross_layer_read','set_variable')`,
		`INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,line,col,raw,relation)
		 VALUES('event','writer','variable','global_prefix_read',1,2,1,'global_prefix_read','set_variable')`,
		`INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,line,col,raw,relation)
		 VALUES('event','writer','variable','localized_read',1,3,1,'localized_read','set_variable')`,
		`INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,line,col,raw,relation)
		 VALUES('event','writer','variable','truly_dead',1,4,1,'truly_dead','set_variable')`,
		`INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,line,col,raw,relation)
		 VALUES('event','reader','variable','cross_layer_read',2,1,1,'cross_layer_read','read_variable')`,
		`INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,line,col,raw,relation)
		 VALUES('event','reader','global_var','global_prefix_read',2,2,1,'global_var:global_prefix_read','')`,
		`INSERT INTO refs(from_object_type,from_object_name,ref_kind,ref_name,file_id,line,col,raw,relation)
		 VALUES('localization','test_key','variable','localized_read',3,1,1,'.Var(''localized_read'')','localization_read')`,
	} {
		if _, err := db.sql.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshVariableWriteOnlyDiagnostics(ctx, tx, 1); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.sql.QueryRow(`SELECT message FROM diagnostics WHERE code='variable_write_only'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(name, "truly_dead") {
		t.Fatalf("unexpected remaining write-only diagnostic: %q", name)
	}
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM diagnostics WHERE code='variable_write_only'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("variable_write_only=%d, want only truly_dead", count)
	}
}

func TestLocalizationRuntimeVariableRefsAreExtracted(t *testing.T) {
	analysis, err := AnalyzeVirtualFile(
		"localization/english/runtime_refs_l_english.yml",
		"patch",
		1,
		`l_english:
 runtime_ref:0 "[GetGlobalVariable('global_link').Var('nested_value').GetValue]"
`,
	)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, ref := range analysis.Refs {
		found[ref.Kind+":"+ref.Name+":"+ref.Relation] = true
	}
	if !found["global_var:global_link:localization_read"] || !found["variable:nested_value:localization_read"] {
		t.Fatalf("missing localization runtime refs: %+v", analysis.Refs)
	}
}
