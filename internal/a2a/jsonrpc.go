package a2a

import (
	"encoding/json"
	"net/http"

	"agenthub/internal/httputil"
)

// JSON-RPC 2.0 envelope.
const JSONRPCVersion = "2.0"

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// agenthub specific error codes, all inside the server-reserved range.
const (
	CodeTaskNotFound                 = -32001
	CodeTaskNotCancelable            = -32002
	CodePushNotificationNotSupported = -32003
	CodeUnsupportedOperation         = -32004
	CodeInvalidStateTransition       = -32005
	CodeResourceNotFound             = -32006
	CodeResourceUnreachable          = -32007
	CodeTaskNotResumable             = -32008
)

// Messages carried by RPCError are stable identifiers, never prose, so a client
// can branch on them and the console can localise them from its own copy.
const (
	MsgParseError                   = "parse_error"
	MsgInvalidRequest               = "invalid_request"
	MsgMethodNotFound               = "method_not_found"
	MsgInvalidParams                = "invalid_params"
	MsgInternalError                = "internal_error"
	MsgTaskNotFound                 = "task_not_found"
	MsgTaskNotCancelable            = "task_not_cancelable"
	MsgPushNotificationNotSupported = "push_notification_not_supported"
	MsgUnsupportedOperation         = "unsupported_operation"
	MsgInvalidStateTransition       = "invalid_state_transition"
	MsgResourceNotFound             = "resource_not_found"
	MsgResourceUnreachable          = "resource_unreachable"
	MsgTaskNotResumable             = "task_not_resumable"
)

// Request is an inbound JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Error is the JSON-RPC 2.0 error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// NewError builds an error object with a machine readable message.
func NewError(code int, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Error implements the error interface so RPC errors can be returned directly.
func (e *Error) Error() string { return e.Message }

// Response is an outbound JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

// NewResponse builds a successful response.
func NewResponse(id any, result any) Response {
	return Response{JSONRPC: JSONRPCVersion, ID: id, Result: result}
}

// NewErrorResponse builds an error response.
func NewErrorResponse(id any, err *Error) Response {
	return Response{JSONRPC: JSONRPCVersion, ID: id, Error: err}
}

// WriteResult writes a successful JSON-RPC response.
func WriteResult(w http.ResponseWriter, id any, result any) {
	httputil.WriteJSON(w, http.StatusOK, NewResponse(id, result))
}

// WriteRPCError writes a JSON-RPC error response. JSON-RPC always answers 200
// for a well formed envelope; protocol failures live in the error object.
func WriteRPCError(w http.ResponseWriter, id any, err *Error) {
	status := http.StatusOK
	switch err.Code {
	case CodeParseError, CodeInvalidRequest:
		status = http.StatusBadRequest
	case CodeMethodNotFound:
		status = http.StatusNotFound
	}
	httputil.WriteJSON(w, status, NewErrorResponse(id, err))
}

// WriteRPCErr maps a domain error onto its JSON-RPC representation.
func WriteRPCErr(w http.ResponseWriter, id any, err error) {
	switch e := err.(type) {
	case *Error:
		WriteRPCError(w, id, e)
	default:
		WriteRPCError(w, id, NewError(CodeInternalError, MsgInternalError))
	}
}
