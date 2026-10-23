package tool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Johnny-Cheung/Johnny-Code/internal/model"
)

type Tool interface {
	Definition() model.ToolDefinition
	Execute(context.Context, json.RawMessage) (Result, error)
}

type Result struct {
	Content string
	IsError bool
}

type ErrorKind string

const (
	KindInvalidArguments ErrorKind = "invalid_arguments"
	KindExecution        ErrorKind = "execution"
	KindPermissionDenied ErrorKind = "permission_denied"
)

type Error struct {
	Kind    ErrorKind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return string(e.Kind)
}

func (e *Error) Unwrap() error { return e.Err }

func invalidArguments(message string, err error) error {
	return &Error{Kind: KindInvalidArguments, Message: message, Err: err}
}

func executionError(message string, err error) error {
	return &Error{Kind: KindExecution, Message: message, Err: err}
}

func permissionDenied(message string, err error) error {
	return &Error{Kind: KindPermissionDenied, Message: message, Err: err}
}

func ToModelResult(callID string, result Result, err error) model.ToolResult {
	content := result.Content
	if content == "" && err != nil {
		content = err.Error()
	}
	return model.ToolResult{
		ToolCallID: callID,
		Content:    content,
		IsError:    result.IsError || err != nil,
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func jsonObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}
