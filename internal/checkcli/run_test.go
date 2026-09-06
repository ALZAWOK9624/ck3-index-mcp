package checkcli

import (
	"bytes"
	"ck3-index/internal/indexer"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCheckCLIWithoutConfigOrDatabase(t *testing.T) {
	for _, tc := range []struct {
		input  string
		failed bool
	}{
		{`{"files":[{"path":"common/scripted_effects/test.txt","content":"test={add_gold=5}"}]}`, false},
		{`{"files":[{"path":"common/scripted_effects/test.txt","content":"test={"}]}`, true},
	} {
		var output, stderr bytes.Buffer
		err := Run(context.Background(), nil, strings.NewReader(tc.input), &output, &stderr)
		if errors.Is(err, ErrCheckFailed) != tc.failed || err != nil && !tc.failed {
			t.Fatalf("unexpected exit: %v", err)
		}
		var result indexer.CheckResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.DatabaseUsed || result.Passed == tc.failed {
			t.Fatalf("bad result: %+v", result)
		}
	}
}

func TestCheckCLIRejectsUnknownFieldsAndTrailingRequest(t *testing.T) {
	for _, input := range []string{`{"files":[],"database":"private.sqlite"}`, `{"files":[{"path":"events/a.txt","content":"a={}","read_file":true}]}`, `{"files":[]} {"files":[]}`} {
		var output, stderr bytes.Buffer
		if err := Run(context.Background(), nil, strings.NewReader(input), &output, &stderr); err == nil {
			t.Fatalf("invalid input accepted: %s", input)
		}
	}
}
