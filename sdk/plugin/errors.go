// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Code is a status code. The values are the gRPC/Connect codes.
type Code = connect.Code

// Codes plugins return most often.
const (
	CodeInvalidArgument    = connect.CodeInvalidArgument
	CodeNotFound           = connect.CodeNotFound
	CodeUnimplemented      = connect.CodeUnimplemented
	CodeUnavailable        = connect.CodeUnavailable
	CodeResourceExhausted  = connect.CodeResourceExhausted
	CodeFailedPrecondition = connect.CodeFailedPrecondition
	CodeInternal           = connect.CodeInternal
)

// Error is an error with a status code, carried across the process or
// network boundary between plugin and host.
type Error struct {
	Code    Code
	Message string
	// RetryAfter tells the caller when to try again, e.g. after the
	// provider rate-limited the plugin. Zero means no hint.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s: %s (retry after %s)", e.Code, e.Message, e.RetryAfter)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Errorf returns an *Error with the code and a formatted message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrNotFound says the requested record does not exist at the provider.
func ErrNotFound(format string, args ...any) *Error {
	return Errorf(CodeNotFound, format, args...)
}

// ErrInvalidArgument says the request (or settings) can't be used.
func ErrInvalidArgument(format string, args ...any) *Error {
	return Errorf(CodeInvalidArgument, format, args...)
}

// ErrUnimplemented says the plugin does not support a call.
func ErrUnimplemented(method string) *Error {
	return Errorf(CodeUnimplemented, "%s is not supported by this plugin", method)
}

// ErrRateLimited says the provider is rate-limiting the plugin.
func ErrRateLimited(retryAfter time.Duration, format string, args ...any) *Error {
	e := Errorf(CodeResourceExhausted, format, args...)
	e.RetryAfter = retryAfter
	return e
}

// ErrorCode returns the code of err: the code of an *Error, CodeCanceled or
// CodeDeadlineExceeded for context errors, CodeInternal otherwise.
func ErrorCode(err error) Code {
	if err == nil {
		return 0
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	if errors.Is(err, context.Canceled) {
		return connect.CodeCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return connect.CodeDeadlineExceeded
	}
	return CodeInternal
}

func asError(err error) *Error {
	var pe *Error
	if errors.As(err, &pe) {
		return pe
	}
	return &Error{Code: ErrorCode(err), Message: err.Error()}
}

// ToConnectError converts err for a Connect handler.
func ToConnectError(err error) error {
	if err == nil {
		return nil
	}
	pe := asError(err)
	ce := connect.NewError(pe.Code, errors.New(pe.Message))
	if pe.RetryAfter > 0 {
		if d, derr := connect.NewErrorDetail(&errdetails.RetryInfo{RetryDelay: durationpb.New(pe.RetryAfter)}); derr == nil {
			ce.AddDetail(d)
		}
	}
	return ce
}

// ToGRPCError converts err for a gRPC handler.
func ToGRPCError(err error) error {
	if err == nil {
		return nil
	}
	pe := asError(err)
	st := status.New(codes.Code(pe.Code), pe.Message)
	if pe.RetryAfter > 0 {
		if withDetails, derr := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(pe.RetryAfter)}); derr == nil {
			st = withDetails
		}
	}
	return st.Err()
}

// FromConnectError converts an error returned by a Connect client into an
// *Error. Context errors are returned unchanged.
func FromConnectError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	pe := &Error{Code: ce.Code(), Message: ce.Message()}
	for _, d := range ce.Details() {
		v, derr := d.Value()
		if derr != nil {
			continue
		}
		if ri, ok := v.(*errdetails.RetryInfo); ok {
			pe.RetryAfter = ri.GetRetryDelay().AsDuration()
		}
	}
	return pe
}

// FromGRPCError converts an error returned by a gRPC client into an *Error.
func FromGRPCError(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	pe := &Error{Code: Code(st.Code()), Message: st.Message()}
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok {
			pe.RetryAfter = ri.GetRetryDelay().AsDuration()
		}
	}
	return pe
}
