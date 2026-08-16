package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/crypto-market-advisor/advisor/internal/logging"
)

// ErrorCode is the stable machine-readable error identifier returned to the UI.
type ErrorCode string

// Error codes used across the API.
const (
	CodeBadRequest     ErrorCode = "BAD_REQUEST"
	CodeValidation     ErrorCode = "VALIDATION_FAILED"
	CodeNotFound       ErrorCode = "NOT_FOUND"
	CodeConflict       ErrorCode = "CONFLICT"
	CodeRateLimited    ErrorCode = "RATE_LIMITED"
	CodeUpstream       ErrorCode = "UPSTREAM_UNAVAILABLE"
	CodeInternal       ErrorCode = "INTERNAL_ERROR"
	CodeNotImplemented ErrorCode = "NOT_IMPLEMENTED"
)

// APIError is the single error envelope returned by every endpoint.
type APIError struct {
	Code      ErrorCode         `json:"code"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
	RequestID string            `json:"request_id,omitempty"`

	status int
	cause  error
}

// Error implements the error interface.
func (e *APIError) Error() string { return string(e.Code) + ": " + e.Message }

// Unwrap exposes the underlying cause for errors.Is/As.
func (e *APIError) Unwrap() error { return e.cause }

// Status returns the HTTP status code for the error.
func (e *APIError) Status() int { return e.status }

// WithCause attaches an internal cause that is logged but never serialised.
func (e *APIError) WithCause(err error) *APIError {
	e.cause = err
	return e
}

// WithDetails attaches field-level details.
func (e *APIError) WithDetails(d map[string]string) *APIError {
	e.Details = d
	return e
}

// NewError builds an APIError.
func NewError(status int, code ErrorCode, msg string) *APIError {
	return &APIError{Code: code, Message: msg, status: status}
}

// ErrBadRequest reports a malformed request.
func ErrBadRequest(msg string) *APIError { return NewError(http.StatusBadRequest, CodeBadRequest, msg) }

// ErrValidation reports a well-formed request that violates a business rule.
func ErrValidation(msg string) *APIError {
	return NewError(http.StatusUnprocessableEntity, CodeValidation, msg)
}

// ErrNotFound reports a missing resource.
func ErrNotFound(msg string) *APIError { return NewError(http.StatusNotFound, CodeNotFound, msg) }

// ErrConflict reports a state conflict, such as closing a closed position.
func ErrConflict(msg string) *APIError { return NewError(http.StatusConflict, CodeConflict, msg) }

// ErrRateLimited reports that the caller must wait before retrying.
func ErrRateLimited(msg string) *APIError {
	return NewError(http.StatusTooManyRequests, CodeRateLimited, msg)
}

// ErrUpstream reports that a dependency (provider or LLM) is unavailable.
func ErrUpstream(msg string) *APIError {
	return NewError(http.StatusServiceUnavailable, CodeUpstream, msg)
}

// ErrInternal reports an unexpected server-side failure.
func ErrInternal(msg string) *APIError {
	return NewError(http.StatusInternalServerError, CodeInternal, msg)
}

// WriteJSON serialises v with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already written; nothing sensible remains to do.
		slog.Default().Error("failed to encode response", slog.String("error", err.Error()))
	}
}

// WriteError converts any error into the standard envelope and logs it.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		apiErr = ErrInternal("internal server error").WithCause(err)
	}
	apiErr.RequestID = logging.RequestID(r.Context())

	log := logging.FromContext(r.Context(), logger)
	attrs := []any{
		slog.String("code", string(apiErr.Code)),
		slog.Int("status", apiErr.status),
		slog.String("path", r.URL.Path),
	}
	if apiErr.cause != nil {
		attrs = append(attrs, slog.String("cause", apiErr.cause.Error()))
	}
	if apiErr.status >= 500 {
		log.Error(apiErr.Message, attrs...)
	} else {
		log.Warn(apiErr.Message, attrs...)
	}

	WriteJSON(w, apiErr.status, map[string]any{"error": apiErr})
}
