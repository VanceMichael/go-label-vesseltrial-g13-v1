package fault

import (
	"errors"
	"fmt"
)

type Kind string

const (
	Invalid      Kind = "invalid"
	Unauthorized Kind = "unauthorized"
	Forbidden    Kind = "forbidden"
	NotFound     Kind = "not_found"
	Conflict     Kind = "conflict"
	Unavailable  Kind = "unavailable"
	Internal     Kind = "internal"
)

type Error struct {
	Kind    Kind
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *Error) Unwrap() error { return e.Cause }

func New(kind Kind, code, message string) error {
	return &Error{Kind: kind, Code: code, Message: message}
}

func Wrap(kind Kind, code, message string, cause error) error {
	if cause == nil {
		return nil
	}
	return &Error{Kind: kind, Code: code, Message: message, Cause: cause}
}

func Classify(err error) (Kind, string, string) {
	var target *Error
	if errors.As(err, &target) {
		return target.Kind, target.Code, target.Message
	}
	return Internal, "internal_error", "internal server error"
}

func IsKind(err error, kind Kind) bool {
	var target *Error
	return errors.As(err, &target) && target.Kind == kind
}
