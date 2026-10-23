// Package httpjson is how A-NAS's HTTP APIs speak JSON: one error shape, one
// rule for request bodies, and a response that cannot be encoded logged
// instead of crashing the request (#192).
package httpjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
)

// Error is the body of every error answer.
type Error struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail names the error for programs (Code) and people (Message).
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Write answers status with value as JSON. A value that cannot be encoded is
// a programming error: it is logged and answered as 500 internal_error, since
// nothing has been sent yet. A client that went away is not an error.
func Write(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		slog.Error("JSON response could not be encoded", "status", status, "type", fmt.Sprintf("%T", value), "error", err)
		status, body = http.StatusInternalServerError, []byte(`{"error":{"code":"internal_error","message":"response could not be encoded"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// WriteError answers status with the error shape.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	Write(w, status, Error{Error: ErrorDetail{Code: code, Message: message}})
}

// Why a request body was refused.
var (
	ErrContentType = errors.New("Content-Type must be application/json")
	ErrTooLarge    = errors.New("request body is too large")
	ErrInvalid     = errors.New("request body must be one JSON value of the expected shape")
)

// Decode reads the request body into target. The body must be declared
// application/json (parameters such as charset are allowed), hold at most
// limit bytes, name no field target lacks, and end after one JSON value. It
// fails with ErrContentType, ErrTooLarge or ErrInvalid.
func Decode(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		return ErrContentType
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	if err == nil {
		if err = decoder.Decode(&json.RawMessage{}); errors.Is(err, io.EOF) {
			return nil
		}
		if err == nil {
			err = errors.New("more than one JSON value")
		}
	}
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		return ErrTooLarge
	}
	return fmt.Errorf("%w: %v", ErrInvalid, err)
}

// Read decodes like Decode and answers a refused body: 413 request_too_large
// for one over the limit, otherwise 400 invalid_request. It reports whether
// target was filled.
func Read(w http.ResponseWriter, r *http.Request, limit int64, target any) bool {
	if err := Decode(w, r, limit, target); err != nil {
		WriteDecodeError(w, err, "invalid_request", "request body is invalid")
		return false
	}
	return true
}

// WriteDecodeError answers a Decode failure: 413 request_too_large for a body
// over the limit, otherwise 400 with the API's own code and message.
func WriteDecodeError(w http.ResponseWriter, err error, code, message string) {
	if errors.Is(err, ErrTooLarge) {
		WriteError(w, http.StatusRequestEntityTooLarge, "request_too_large", "the request body is too large")
		return
	}
	WriteError(w, http.StatusBadRequest, code, message)
}

// Limit is the largest JSON body most requests may send.
const Limit = 1 << 20
