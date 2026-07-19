package mcpserver

import (
	"bytes"
	"encoding/json"
	"strings"
)

const latestMCPProtocolVersion = "2025-11-25"

var supportedMCPProtocolVersions = map[string]bool{
	latestMCPProtocolVersion: true,
	"2025-06-18":             true,
}

func negotiateMCPProtocolVersion(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", newProtocolError(rpcInvalidParams, "initialize params require protocolVersion")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return "", newProtocolError(rpcInvalidParams, "initialize params must be a JSON object")
	}
	versionRaw, ok := fields["protocolVersion"]
	if !ok {
		return "", newProtocolError(rpcInvalidParams, "initialize params require protocolVersion")
	}
	var requested string
	if err := json.Unmarshal(versionRaw, &requested); err != nil || strings.TrimSpace(requested) == "" {
		return "", newProtocolError(rpcInvalidParams, "initialize protocolVersion must be a non-empty string")
	}
	requested = strings.TrimSpace(requested)
	if supportedMCPProtocolVersions[requested] {
		return requested, nil
	}
	return latestMCPProtocolVersion, nil
}
