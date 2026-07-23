package mcpserver

import (
	"bytes"
	"encoding/json"
	"strconv"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	hasID   bool
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   any             `json:"error,omitempty"`
}

func (r *rpcRequest) UnmarshalJSON(data []byte) error {
	type reqAlias rpcRequest
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var aux reqAlias
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = rpcRequest(aux)
	_, r.hasID = raw["id"]
	return nil
}

func validRPCRequestID(raw json.RawMessage) bool {
	_, ok := normalizedRPCRequestID(raw)
	return ok
}

// normalizedRPCRequestID implements the MCP/JSON-RPC request-id contract:
// only strings and integer JSON numbers are accepted. Keys are based on the
// decoded value, so "a" and "\u0061" are the same session identifier.
func normalizedRPCRequestID(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return "string:" + typed, true
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return "", false
		}
		return "integer:" + strconv.FormatInt(integer, 10), true
	default:
		return "", false
	}
}
