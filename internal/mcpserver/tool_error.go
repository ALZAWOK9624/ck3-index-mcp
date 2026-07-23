package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"ck3-index/internal/indexer"
)

// Stable tool-error codes. Handlers can add narrower codes over time, but
// clients must never need to parse Message to recover from these conditions.
const (
	ErrorInvalidArguments           = "INVALID_ARGUMENTS"
	ErrorMissingRequiredArgument    = "MISSING_REQUIRED_ARGUMENT"
	ErrorUnknownOperation           = "UNKNOWN_OPERATION"
	ErrorObjectNotFound             = "OBJECT_NOT_FOUND"
	ErrorObjectAmbiguous            = "OBJECT_AMBIGUOUS"
	ErrorSourceNotFound             = "SOURCE_NOT_FOUND"
	ErrorPathOutsideProject         = "PATH_OUTSIDE_PROJECT"
	ErrorIndexNotReady              = "INDEX_NOT_READY"
	ErrorIndexFinalizing            = "INDEX_FINALIZING"
	ErrorIndexStale                 = "INDEX_STALE"
	ErrorIndexRefreshRequired       = "INDEX_REFRESH_REQUIRED"
	ErrorFullScanRequired           = "FULL_SCAN_REQUIRED"
	ErrorResultTruncated            = "RESULT_TRUNCATED"
	ErrorResponseTooLarge           = "RESPONSE_TOO_LARGE"
	ErrorMapDatabaseUnavailable     = "MAP_DATABASE_UNAVAILABLE"
	ErrorGISUnavailable             = "GIS_UNAVAILABLE"
	ErrorRuntimeEvidenceUnavailable = "RUNTIME_EVIDENCE_UNAVAILABLE"
	ErrorIncompleteFileContent      = "INCOMPLETE_FILE_CONTENT"
	ErrorInvalidPatch               = "INVALID_PATCH"
	ErrorConflictingGeneration      = "CONFLICTING_GENERATION"
	ErrorServerBusy                 = "SERVER_BUSY"
	ErrorOperationCancelled         = "OPERATION_CANCELLED"
	ErrorOperationTimeout           = "OPERATION_TIMEOUT"
	ErrorInternal                   = "INTERNAL_ERROR"
)

// ToolError is the stable, machine-readable error contract returned from every
// MCP tool call. Its code is intended for client branching; Message remains a
// concise human explanation and must never be parsed by a client.
type ToolError struct {
	Code      string
	Category  string
	Message   string
	Retryable bool
	Details   map[string]any
	Recovery  map[string]any
}

func (e *ToolError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func newToolError(code, category, message string, retryable bool, details, recovery map[string]any) *ToolError {
	if details == nil {
		details = map[string]any{}
	}
	if recovery == nil {
		recovery = map[string]any{}
	}
	return &ToolError{
		Code: code, Category: category, Message: message, Retryable: retryable,
		Details: details, Recovery: recovery,
	}
}

func invalidArgument(field, message string) *ToolError {
	details := map[string]any{}
	if field != "" {
		details["field"] = field
	}
	return newToolError(ErrorInvalidArguments, "invalid_arguments", message, false, details,
		map[string]any{"guidance": "Correct the documented argument and retry the same tool."})
}

func missingArgument(field string) *ToolError {
	return newToolError(ErrorMissingRequiredArgument, "invalid_arguments", fmt.Sprintf("missing required argument field %q", field), false,
		map[string]any{"field": field}, map[string]any{"guidance": "Provide the required field and retry the same tool."})
}

func unknownOperation(operation string) *ToolError {
	return newToolError(ErrorUnknownOperation, "invalid_arguments", fmt.Sprintf("unsupported operation %q", operation), false,
		map[string]any{"field": "operation", "operation": operation}, map[string]any{"guidance": "Use one of the operations advertised by the tool schema."})
}

func toolErrorFrom(err error) *ToolError {
	if err == nil {
		return nil
	}
	var typed *ToolError
	if errors.As(err, &typed) {
		return typed
	}
	var fullRequired *indexer.FullScanRequiredError
	var responseTooLarge *responseTooLargeError
	if errors.As(err, &fullRequired) {
		return newToolError(ErrorFullScanRequired, "index_state", "the requested incremental refresh cannot be completed safely", false,
			map[string]any{"reason": fullRequired.Reason, "paths": fullRequired.Paths},
			map[string]any{"operation": "full", "guidance": "Run ck3_refresh with operation=full, then retry status or files."})
	}
	if errors.As(err, &responseTooLarge) {
		return newToolError(ErrorResponseTooLarge, "response_size", "the encoded tool result exceeds the requested response budget", true,
			map[string]any{"actual_bytes": responseTooLarge.Actual, "max_response_bytes": responseTooLarge.Limit},
			map[string]any{"guidance": "Use a smaller limit or a later page, or retry with a larger max_response_bytes within the advertised cap."})
	}
	if errors.Is(err, indexer.ErrConflictingGeneration) {
		return newToolError(ErrorConflictingGeneration, "concurrency", "the published index changed while a staged refresh was being prepared", true, nil,
			map[string]any{"guidance": "Read ck3_refresh status and retry the full refresh from the new generation."})
	}
	if errors.Is(err, context.Canceled) {
		return newToolError(ErrorOperationCancelled, "operation_state", "the operation was cancelled before it completed", true, nil,
			map[string]any{"guidance": "Retry the same read-only operation when ready."})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return newToolError(ErrorOperationTimeout, "operation_state", "the operation exceeded its time limit", true, nil,
			map[string]any{"guidance": "Retry with a narrower request or a longer client timeout."})
	}
	return newToolError(ErrorInternal, "internal", err.Error(), false, nil, nil)
}
