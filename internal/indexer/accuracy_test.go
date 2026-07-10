package indexer

import (
	"context"
	"testing"
)

func TestAccuracyFixtures(t *testing.T) {
	report, err := RunAccuracy(context.Background(), "../../testdata/accuracy")
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 0 {
		t.Fatalf("accuracy failures: %+v", report)
	}
	if report.Passed < 5 {
		t.Fatalf("expected at least 5 accuracy cases, got %+v", report)
	}
}
