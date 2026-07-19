package mcpserver

import "fmt"

const (
	rpcParseError      = -32700
	rpcInvalidRequest  = -32600
	rpcMethodNotFound  = -32601
	rpcInvalidParams   = -32602
	rpcInternalError   = -32603
	rpcMessageTooLarge = -32001
)

type protocolError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Fatal   bool   `json:"-"`
}

func (e *protocolError) Error() string {
	return e.Message
}

func newProtocolError(code int, message string) *protocolError {
	return &protocolError{Code: code, Message: message}
}

func unknownToolError(name string) *protocolError {
	return newProtocolError(rpcInvalidParams, fmt.Sprintf("unknown tool %q", name))
}
