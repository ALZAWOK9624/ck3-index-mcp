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

type mcpInitializeParams struct {
	ProtocolVersion string
	Capabilities    map[string]json.RawMessage
	ClientName      string
	ClientVersion   string
}

func parseMCPInitializeParams(raw json.RawMessage) (mcpInitializeParams, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return mcpInitializeParams{}, newProtocolError(rpcInvalidParams, "initialize params require protocolVersion, capabilities, and clientInfo")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return mcpInitializeParams{}, newProtocolError(rpcInvalidParams, "initialize params must be a JSON object")
	}
	versionRaw, ok := fields["protocolVersion"]
	if !ok {
		return mcpInitializeParams{}, newProtocolError(rpcInvalidParams, "initialize params require protocolVersion")
	}
	var requested string
	if err := json.Unmarshal(versionRaw, &requested); err != nil || strings.TrimSpace(requested) == "" {
		return mcpInitializeParams{}, newProtocolError(rpcInvalidParams, "initialize protocolVersion must be a non-empty string")
	}
	capabilitiesRaw, ok := fields["capabilities"]
	if !ok {
		return mcpInitializeParams{}, newProtocolError(rpcInvalidParams, "initialize params require capabilities")
	}
	var capabilities map[string]json.RawMessage
	if err := json.Unmarshal(capabilitiesRaw, &capabilities); err != nil || capabilities == nil {
		return mcpInitializeParams{}, newProtocolError(rpcInvalidParams, "initialize capabilities must be a JSON object")
	}
	clientRaw, ok := fields["clientInfo"]
	if !ok {
		return mcpInitializeParams{}, newProtocolError(rpcInvalidParams, "initialize params require clientInfo")
	}
	var client struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(clientRaw, &client); err != nil {
		return mcpInitializeParams{}, newProtocolError(rpcInvalidParams, "initialize clientInfo must be a JSON object")
	}
	client.Name = strings.TrimSpace(client.Name)
	client.Version = strings.TrimSpace(client.Version)
	if client.Name == "" || client.Version == "" {
		return mcpInitializeParams{}, newProtocolError(rpcInvalidParams, "initialize clientInfo requires non-empty name and version")
	}
	requested = strings.TrimSpace(requested)
	negotiated := latestMCPProtocolVersion
	if supportedMCPProtocolVersions[requested] {
		negotiated = requested
	}
	return mcpInitializeParams{
		ProtocolVersion: negotiated,
		Capabilities:    capabilities,
		ClientName:      client.Name,
		ClientVersion:   client.Version,
	}, nil
}
