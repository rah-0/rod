package cdp

import (
	"errors"
	"fmt"
)

// Error of the Response.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

// Error stdlib interface.
func (e *Error) Error() string {
	return fmt.Sprintf("%v", *e)
}

// Is stdlib interface.
func (e Error) Is(target error) bool {
	err, ok := target.(*Error)
	if !ok {
		return false
	}
	if err == ErrCtxDestroyed && e.Code == err.Code {
		return e.Message == err.Message || e.Message == "Inspected target navigated or closed"
	}
	return e == *err
}

// ErrCtxNotFound type.
var ErrCtxNotFound = &Error{
	Code:    -32000,
	Message: "Cannot find context with specified id",
}

// ErrSessionNotFound type.
var ErrSessionNotFound = &Error{
	Code:    -32001,
	Message: "Session with given id not found.",
}

// ErrSearchSessionNotFound type.
var ErrSearchSessionNotFound = &Error{
	Code:    -32000,
	Message: "No search session with given id found",
}

// ErrCtxDestroyed type.
var ErrCtxDestroyed = &Error{
	Code:    -32000,
	Message: "Execution context was destroyed.",
}

// ErrObjNotFound type.
var ErrObjNotFound = &Error{
	Code:    -32000,
	Message: "Could not find object with given id",
}

// ErrNodeNotFoundAtPos type.
var ErrNodeNotFoundAtPos = &Error{
	Code:    -32000,
	Message: "No node found at given location",
}

// ErrNotAttachedToActivePage type.
var ErrNotAttachedToActivePage = &Error{
	Code:    -32000,
	Message: "Not attached to an active page",
}

// ErrClientClosed indicates explicit closure of a CDP client.
var ErrClientClosed = errors.New("CDP client closed")

// ErrTransportNotClosable indicates that a custom transport cannot be interrupted.
var ErrTransportNotClosable = errors.New("CDP transport does not support Close")

// ErrWebSocketProtocol indicates invalid or unsupported WebSocket framing.
var ErrWebSocketProtocol = errors.New("invalid WebSocket protocol")

// ErrWebSocketMessageTooLarge indicates an incoming message larger than
// WebSocket.MaxMessageSize.
var ErrWebSocketMessageTooLarge = errors.New("WebSocket message too large")

// ErrWebSocketClosed indicates a peer's WebSocket close frame.
var ErrWebSocketClosed = errors.New("WebSocket closed by peer")

// WebSocketCloseError contains the close status and UTF-8 reason from the peer.
// Code 1005 means that the peer supplied no status code.
type WebSocketCloseError struct {
	Code   uint16
	Reason string
}

func (e *WebSocketCloseError) Error() string {
	return fmt.Sprintf("WebSocket closed: %d %s", e.Code, e.Reason)
}
func (e *WebSocketCloseError) Unwrap() error { return ErrWebSocketClosed }
