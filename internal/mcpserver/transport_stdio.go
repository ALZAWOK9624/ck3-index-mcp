package mcpserver

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The decoded ck3_package payload is capped at 32 MiB. Its Base64 JSON
// envelope can exceed 42 MiB, so the transport needs a separate bounded limit.
const maxMCPMessageBytes = 64 << 20
const maxMCPMessageMiB = maxMCPMessageBytes >> 20

// readMCPMessage supports both framed (Content-Length: N + blank + body) and
// newline-delimited JSON envelope modes, for client compatibility.
func readMCPMessage(r *bufio.Reader) (rpcRequest, error) {
	var req rpcRequest
	for {
		line, err := readLimitedLine(r, maxMCPMessageBytes)
		if err != nil && len(line) == 0 {
			if errors.Is(err, errMCPMessageTooLarge) {
				return req, &protocolError{Code: rpcMessageTooLarge, Message: fmt.Sprintf("MCP request exceeds the %d MiB message limit", maxMCPMessageMiB), Fatal: true}
			}
			return req, err
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
			n, perr := strconv.Atoi(strings.TrimSpace(trimmed[len("content-length:"):]))
			if perr != nil {
				return req, newProtocolError(rpcInvalidRequest, "invalid Content-Length header")
			}
			if n < 0 || n > maxMCPMessageBytes {
				return req, &protocolError{Code: rpcMessageTooLarge, Message: fmt.Sprintf("MCP request exceeds the %d MiB message limit", maxMCPMessageMiB), Fatal: true}
			}
			for {
				b, err := readLimitedLine(r, 64<<10)
				if err != nil && len(b) == 0 {
					return req, err
				}
				if strings.TrimSpace(string(b)) == "" {
					break
				}
			}
			body := make([]byte, n)
			if _, err := io.ReadFull(r, body); err != nil {
				return req, err
			}
			if err := decodeMCPRequest(body, &req); err != nil {
				return req, err
			}
			return req, nil
		}
		if err := decodeMCPRequest([]byte(trimmed), &req); err != nil {
			return req, err
		}
		return req, nil
	}
}

func decodeMCPRequest(data []byte, req *rpcRequest) error {
	if err := json.Unmarshal(data, req); err != nil {
		if json.Valid(data) {
			return newProtocolError(rpcInvalidRequest, "invalid JSON-RPC request")
		}
		return newProtocolError(rpcParseError, "parse error")
	}
	return nil
}

var errMCPMessageTooLarge = errors.New("MCP message too large")

func readLimitedLine(r *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, min(limit, 4096))
	for {
		fragment, err := r.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, errMCPMessageTooLarge
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

func writeMCPMessage(out io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	// MCP stdio transport uses newline-delimited JSON. Older Codex clients also
	// accepted LSP-style Content-Length framing, but current clients wait for a
	// complete JSON line and will otherwise time out during initialization.
	data = append(data, '\n')
	_, err = out.Write(data)
	return err
}
