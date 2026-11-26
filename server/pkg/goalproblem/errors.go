package goalproblem

import (
	"errors"
	"fmt"
)

// Canonical sentinel errors aligned to HTTP status mapping:
//   - ErrInvalidInput -> 400 Bad Request
//   - ErrForbidden    -> 403 Forbidden
//   - ErrNotFound     -> 404 Not Found
//   - ErrConflict     -> 409 Conflict
var (
	ErrInvalidInput = errors.New("invalid input")
	ErrForbidden    = errors.New("forbidden")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
)

// Domain-specific sentinel errors. Each error returned by the domain wraps both
// its canonical HTTP category sentinel and the specific domain reason where
// applicable, so callers can check either errors.Is(err, ErrConflict) or
// errors.Is(err, ErrStaleProposal).
var (
	ErrUninitialized             = errors.New("goal state is uninitialized")
	ErrDuplicateID               = errors.New("duplicate identifier")
	ErrStaleRevision             = errors.New("stale revision reference")
	ErrStaleProposal             = errors.New("stale charter revision proposal")
	ErrRevisionMismatch          = errors.New("revision reference mismatch")
	ErrIndependentReviewRequired = errors.New("independent reviewer required")
	ErrInsufficientEvidence      = errors.New("insufficient evidence for charter revision")
	ErrReassessmentRequired      = errors.New("branch requires reassessment")
	ErrInvalidTransition         = errors.New("invalid state transition")
	ErrNotImplemented            = errors.New("not implemented")
)

type domainError struct {
	category error
	reason   error
	msg      string
}

func (e *domainError) Error() string {
	return e.msg
}

func (e *domainError) Unwrap() []error {
	if e.reason == nil {
		return []error{e.category}
	}
	return []error{e.category, e.reason}
}

func errInvalid(format string, args ...any) error {
	return &domainError{
		category: ErrInvalidInput,
		msg:      fmt.Sprintf(format, args...),
	}
}

func errForbidden(reason error, format string, args ...any) error {
	return &domainError{
		category: ErrForbidden,
		reason:   reason,
		msg:      fmt.Sprintf(format, args...),
	}
}

func errNotFound(format string, args ...any) error {
	return &domainError{
		category: ErrNotFound,
		msg:      fmt.Sprintf(format, args...),
	}
}

func errConflict(reason error, format string, args ...any) error {
	return &domainError{
		category: ErrConflict,
		reason:   reason,
		msg:      fmt.Sprintf(format, args...),
	}
}
