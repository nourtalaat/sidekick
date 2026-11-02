// Package problem writes problem details (RFC 9457), the body of every
// error answer of the JSON API and the login endpoints.
package problem

import (
	"encoding/json"
	"net/http"
)

// ContentType is the media type of problem details.
const ContentType = "application/problem+json"

// typeBlank is the problem type of every error: the status and the detail
// say what went wrong.
const typeBlank = "about:blank"

// InternalDetail is the detail of a 500, which never shows the cause.
const InternalDetail = "Something went wrong on the server. The logs have the details."

// Details is the body of a problem details answer. The JSON API's spec
// describes the same shape as its Problem schema.
type Details struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
	// Code names the kind of problem for a program, for example
	// validation. It is an extension member that only some answers have.
	Code string `json:"code,omitempty"`
	// Errors lists the fields of the request that were refused and why.
	Errors []FieldError `json:"errors,omitempty"`
	// Changes is the difference an undo would overwrite, on the answer with
	// the code conflict.
	Changes []Change `json:"changes,omitempty"`
}

// Change is one field of a definition that differs, with its values as
// text. A nil Before or After means the field is absent on that side.
type Change struct {
	Path   string  `json:"path"`
	Before *string `json:"before,omitempty"`
	After  *string `json:"after,omitempty"`
}

// FieldError is a field of a request body and what is wrong with it.
type FieldError struct {
	// Field is the path of the field in the body, such as
	// budgets.maxTokens or match.sender.bots[0]. It is empty for a
	// problem with the whole body.
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Write answers r with problem details of status, with detail saying what
// went wrong with this request.
func Write(w http.ResponseWriter, r *http.Request, status int, detail string) {
	WriteCoded(w, r, status, detail, "", nil)
}

// WriteCoded is Write with the extension members: a code that names the
// kind of problem, and the fields of the request that were refused.
func WriteCoded(w http.ResponseWriter, r *http.Request, status int, detail, code string, errs []FieldError) {
	WriteDetails(w, r, Details{Status: status, Detail: detail, Code: code, Errors: errs})
}

// WriteDetails answers r with d. It sets the type, the title and the
// instance, so a caller sets the status, the detail and the extension
// members.
func WriteDetails(w http.ResponseWriter, r *http.Request, d Details) {
	d.Type, d.Title, d.Instance = typeBlank, http.StatusText(d.Status), r.URL.Path

	body, err := json.Marshal(d)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", ContentType)
	w.WriteHeader(d.Status)
	_, _ = w.Write(body)
}
