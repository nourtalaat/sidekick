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
	body, err := json.Marshal(Details{
		Type: typeBlank, Title: http.StatusText(status), Status: status, Detail: detail, Instance: r.URL.Path,
		Code: code, Errors: errs,
	})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", ContentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
