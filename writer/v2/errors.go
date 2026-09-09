package writer

import (
	"errors"
	"fmt"
)

// Sentinel errors for errors.Is. Concrete failures wrap these and carry an
// MS-PST section citation when the failure is a format invariant.
var (
	ErrInvariant      = errors.New("ms-pst invariant")
	ErrUnsupported    = errors.New("unsupported feature")
	ErrLimit          = errors.New("limit exceeded")
	ErrNotImplemented = errors.New("not implemented")
	ErrClosed         = errors.New("writer closed")
	ErrInvalidArg     = errors.New("invalid argument")
	ErrIO             = errors.New("sink i/o")
)

// Code classifies a writer or inspector failure.
type Code string

const (
	CodeInvariant      Code = "invariant"
	CodeUnsupported    Code = "unsupported"
	CodeLimit          Code = "limit"
	CodeNotImplemented Code = "not-implemented"
	CodeClosed         Code = "closed"
	CodeInvalidArg     Code = "invalid-argument"
	CodeIO             Code = "io"
)

// Error is the structured error taxonomy for writer v2.
// Section is an MS-PST citation (for example "2.2.2.6") when the failure is a
// format invariant; it is empty for contract/limit errors.
type Error struct {
	Code    Code
	Section string
	Field   string
	Detail  string
	Feature Feature
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	msg := string(e.Code)
	if e.Section != "" {
		msg += " (MS-PST " + e.Section
		if e.Field != "" {
			msg += " " + e.Field
		}
		msg += ")"
	} else if e.Field != "" {
		msg += " " + e.Field
	}
	if e.Feature != "" {
		msg += " feature=" + string(e.Feature)
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	} else if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	if e.Err != nil {
		return e.Err
	}
	return e.sentinel()
}

func (e *Error) sentinel() error {
	if e == nil {
		return nil
	}
	switch e.Code {
	case CodeInvariant:
		return ErrInvariant
	case CodeUnsupported:
		return ErrUnsupported
	case CodeLimit:
		return ErrLimit
	case CodeNotImplemented:
		return ErrNotImplemented
	case CodeClosed:
		return ErrClosed
	case CodeInvalidArg:
		return ErrInvalidArg
	case CodeIO:
		return ErrIO
	default:
		return ErrInvalidArg
	}
}

func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	if errors.Is(e.sentinel(), target) {
		return true
	}
	if e.Err != nil {
		return errors.Is(e.Err, target)
	}
	return false
}

func invariant(section, field, format string, args ...any) *Error {
	return &Error{
		Code:    CodeInvariant,
		Section: section,
		Field:   field,
		Detail:  fmt.Sprintf(format, args...),
		Err:     ErrInvariant,
	}
}

func unsupported(feature Feature, detail string) *Error {
	return &Error{
		Code:    CodeUnsupported,
		Feature: feature,
		Detail:  detail,
		Err:     ErrUnsupported,
	}
}

func limitErr(field, format string, args ...any) *Error {
	return &Error{
		Code:   CodeLimit,
		Field:  field,
		Detail: fmt.Sprintf(format, args...),
		Err:    ErrLimit,
	}
}

func invalidArg(field, format string, args ...any) *Error {
	return &Error{
		Code:   CodeInvalidArg,
		Field:  field,
		Detail: fmt.Sprintf(format, args...),
		Err:    ErrInvalidArg,
	}
}
