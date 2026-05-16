// Package httpx is the shared HTTP-layer helper package: JSON response
// writers, the error envelope, and pagination plumbing. It must not import
// internal/api/repository, internal/api/middleware, or any other internal
// package that needs to call back into HTTP behaviour — otherwise import
// cycles will form when the feature sub-packages depend on it.
package httpx

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// errorBody is the unified error envelope. `Fields` is populated only by
// WriteValidationError; every other failure mode leaves it nil and `omitempty`
// keeps it out of the wire format.
type errorBody struct {
	Error   string            `json:"error"`
	Message string            `json:"message,omitempty"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// WriteJSON serializes v with status, setting Content-Type. Returned error is
// the json encoder's; callers almost always discard it because the response
// has already been started by the time it fires.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Add("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

// WriteError emits the standard {error, message} envelope.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	_ = WriteJSON(w, status, errorBody{Error: code, Message: message})
}

// WriteValidationError serializes per-field validation messages alongside the
// standard error envelope. Status is 400 and the error code stays
// `invalid_input` so clients that match on `error` keep working — `fields`
// is additive.
func WriteValidationError(w http.ResponseWriter, fields map[string]string) {
	_ = WriteJSON(w, http.StatusBadRequest, errorBody{
		Error:   "invalid_input",
		Message: "validation failed",
		Fields:  fields,
	})
}

// writeForbidden emits the minimal {"error":"forbidden"} body. Kept inline
// (rather than going through httpx.WriteError) so the wire format stays
// byte-for-byte identical to the pre-split version — clients should not be
// able to detect the refactor by sniffing 403 response bodies.
func WriteForbidden(w http.ResponseWriter) {
	_ = WriteJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
}

func ParseIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}
