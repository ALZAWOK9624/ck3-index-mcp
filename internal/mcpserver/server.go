package mcpserver

import (
	"bufio"
	"context"
	"errors"
	"io"

	"ck3-index/internal/buildinfo"
	"ck3-index/internal/indexer"
)

// Serve runs the read-only MCP server over the provided streams.
func Serve(ctx context.Context, cfg indexer.Config, dbPath string, in io.Reader, out io.Writer) error {
	db, err := indexer.OpenReadOnly(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	r := bufio.NewReaderSize(in, 4*1024*1024)
	for {
		req, err := readMCPMessage(r)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			var rpcErr *protocolError
			if !errors.As(err, &rpcErr) {
				return err
			}
			if writeErr := writeMCPMessage(out, rpcResponse{JSONRPC: "2.0", ID: jsonNullID(), Error: rpcErr}); writeErr != nil {
				return writeErr
			}
			if rpcErr.Fatal {
				return nil
			}
			continue
		}
		if req.hasID && !validRPCRequestID(req.ID) {
			res := rpcResponse{JSONRPC: "2.0", ID: jsonNullID(), Error: newProtocolError(rpcInvalidRequest, "request id must be a string, number, or null")}
			if err := writeMCPMessage(out, res); err != nil {
				return err
			}
			continue
		}
		if !req.hasID {
			continue
		}
		res := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if req.JSONRPC != "2.0" || req.Method == "" {
			res.Error = newProtocolError(rpcInvalidRequest, "request must use jsonrpc=2.0 and include method")
			if err := writeMCPMessage(out, res); err != nil {
				return err
			}
			continue
		}
		switch req.Method {
		case "initialize":
			protocolVersion, err := negotiateMCPProtocolVersion(req.Params)
			if err != nil {
				res.Error = err
				break
			}
			res.Result = map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "ck3-index", "version": buildinfo.Version},
				"instructions":    "Primary CK3/Godherja semantic index. Use ck3_search when the exact id is unknown, ck3_inspect for one exact id, and ck3_review for proposed or dirty files. The standard profile exposes canonical tools; deprecated names remain callable and are discoverable only with CK3_INDEX_MCP_PROFILE=expert. Call ck3-index before raw text search; use rg only to inspect exact evidence paths returned by the index.",
				"capabilities": map[string]any{"tools": map[string]any{
					"listChanged": false,
				}},
			}
		case "ping":
			res.Result = map[string]any{}
		case "tools/list":
			res.Result = map[string]any{"tools": mcpTools()}
		case "tools/call":
			requestContext, cancel := context.WithCancel(ctx)
			result, err := callMCPTool(requestContext, db, cfg, req.Params)
			cancel()
			if err != nil {
				var rpcErr *protocolError
				if errors.As(err, &rpcErr) {
					res.Error = rpcErr
				} else {
					res.Error = newProtocolError(rpcInternalError, "internal MCP server error")
				}
			} else {
				res.Result = result
			}
		default:
			res.Error = newProtocolError(rpcMethodNotFound, "method not found")
		}
		if err := writeMCPMessage(out, res); err != nil {
			return err
		}
	}
}

func jsonNullID() []byte {
	return []byte("null")
}
