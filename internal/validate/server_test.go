package validate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHandler(t *testing.T) {
	t.Parallel()

	good := `{"format":"grafana_alert_file","files":[{"path":"a.yaml","content":"apiVersion: 1\n"}]}`

	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		busy           bool
		wantStatus     int
		wantBody       string
		wantRetryAfter string
	}{
		{name: "validation that passes", method: http.MethodPost, path: "/validate", body: good, wantStatus: http.StatusOK, wantBody: `"ok":true`},
		{
			name:       "validation that fails",
			method:     http.MethodPost,
			path:       "/validate",
			body:       `{"format":"grafana_alert_file","files":[{"path":"a.yaml","content":"groups: []\n"}]}`,
			wantStatus: http.StatusOK,
			wantBody:   `"ok":false`,
		},
		{name: "body that is not JSON", method: http.MethodPost, path: "/validate", body: "{", wantStatus: http.StatusBadRequest, wantBody: "decode the request"},
		{name: "unknown field", method: http.MethodPost, path: "/validate", body: `{"format":"terraform","file":[]}`, wantStatus: http.StatusBadRequest, wantBody: `unknown field \"file\"`},
		{name: "unknown format", method: http.MethodPost, path: "/validate", body: `{"format":"nagios","files":[{"path":"a"}]}`, wantStatus: http.StatusBadRequest, wantBody: "unknown format"},
		{name: "every slot busy", method: http.MethodPost, path: "/validate", body: good, busy: true, wantStatus: http.StatusServiceUnavailable, wantBody: "busy", wantRetryAfter: "5"},
		{name: "health", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusOK, wantBody: "ok"},
		{name: "wrong method", method: http.MethodGet, path: "/validate", wantStatus: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newService(t, Options{})
			s.slotWait = 10 * time.Millisecond

			if tt.busy {
				for range MaxConcurrent {
					s.slots <- struct{}{}
				}
			}

			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tt.wantBody)
			require.Equal(t, tt.wantRetryAfter, rec.Header().Get("Retry-After"))
		})
	}
}

func TestTerminationMessage(t *testing.T) {
	t.Parallel()

	many := make([]Problem, 100)
	for i := range many {
		many[i] = Problem{Path: "main.tf", Line: i + 1, Message: strings.Repeat("x", 100)}
	}

	tests := []struct {
		name          string
		res           Result
		wantTruncated bool
		wantErrors    int
	}{
		{name: "small result", res: Result{OK: false, Errors: many[:2]}, wantErrors: 2},
		{name: "many errors", res: Result{OK: false, Errors: many, NotChecked: many[:3]}, wantTruncated: true},
		{name: "one huge error", res: Result{OK: false, Errors: []Problem{{Path: "a", Message: strings.Repeat("é", 5000)}}}, wantTruncated: true, wantErrors: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := TerminationMessage(tt.res)
			require.NoError(t, err)
			require.LessOrEqual(t, len(raw), MaxTerminationMessage)

			var got Result
			require.NoError(t, json.Unmarshal(raw, &got))
			require.Equal(t, tt.wantTruncated, got.Truncated)
			require.NotEmpty(t, got.Errors)

			if tt.wantErrors > 0 {
				require.Len(t, got.Errors, tt.wantErrors)
			}
		})
	}
}
