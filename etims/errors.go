package etims

import (
	"errors"
	"fmt"
)

var (
	// ErrUnavailable means KRA could not be asked or its answer could not be
	// understood: unreachable, timed out, a 5xx, a body that does not parse.
	// The invoice was not signed; the caller retries with backoff.
	ErrUnavailable = errors.New("etims: service unavailable")

	// ErrRejected means KRA answered and said no: a validation error, an
	// unknown item code, a device that is not valid. Retrying the same
	// request will not help; a person must look at the result code.
	ErrRejected = errors.New("etims: rejected by KRA")

	// ErrDuplicate means KRA already holds an invoice with this number for
	// this branch ("overlapped data"). The caller fetches and stores the
	// existing signature instead of allocating a new number.
	ErrDuplicate = errors.New("etims: invoice number already used")

	// ErrInvalidInvoice means the invoice failed local validation and was
	// never sent.
	ErrInvalidInvoice = errors.New("etims: invalid invoice")

	// ErrNotInitialized means the client has no communication key for the
	// branch; Initialize the device first.
	ErrNotInitialized = errors.New("etims: device not initialized for this branch")
)

// ResultError carries KRA's result code and message. It wraps ErrRejected,
// ErrDuplicate or ErrUnavailable depending on the code, so callers can
// errors.Is against the contract and still log the code.
type ResultError struct {
	Code    string
	Message string
	Kind    error
}

func (e *ResultError) Error() string {
	return fmt.Sprintf("%v: result %s: %s", e.Kind, e.Code, e.Message)
}

func (e *ResultError) Unwrap() error { return e.Kind }
