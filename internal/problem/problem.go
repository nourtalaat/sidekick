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
}

// Write answers r with problem details of status, with detail saying what
// went wrong with this request.
func Write(w http.ResponseWriter, r *http.Request, status int, detail string) {
	body, err := json.Marshal(Details{
		Type: typeBlank, Title: http.StatusText(status), Status: status, Detail: detail, Instance: r.URL.Path,
	})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", ContentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
