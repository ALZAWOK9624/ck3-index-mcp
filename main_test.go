package main

import (
	"reflect"
	"testing"
)

func TestMapPhysicalContextCLIRequest(t *testing.T) {
	request := mapPhysicalContextCLIRequest{
		TargetType:           "province",
		Target:               "1911",
		Targets:              []string{"1911"},
		Operation:            "surface",
		IncludeAdjacentWater: true,
		Limit:                6,
	}
	spec := request.spec()
	if spec.TargetType != request.TargetType ||
		spec.Target != request.Target ||
		!reflect.DeepEqual(spec.Targets, request.Targets) ||
		spec.Operation != request.Operation ||
		spec.IncludeAdjacentWater != request.IncludeAdjacentWater {
		t.Fatalf("CLI request did not preserve physical-context fields: request=%+v spec=%+v", request, spec)
	}
	if limit, err := request.normalizedLimit(); err != nil || limit != 6 {
		t.Fatalf("normalized limit = %d, %v; want 6", limit, err)
	}
}

func TestMapPhysicalContextCLILimit(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		want    int
		wantErr bool
	}{
		{name: "default", limit: 0, want: 16},
		{name: "minimum", limit: 1, want: 1},
		{name: "maximum", limit: 20, want: 20},
		{name: "negative", limit: -1, wantErr: true},
		{name: "too large", limit: 21, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (mapPhysicalContextCLIRequest{Limit: tt.limit}).normalizedLimit()
			if (err != nil) != tt.wantErr {
				t.Fatalf("normalizedLimit() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("normalizedLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}
