package problem_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/problem"
)

func TestWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		write func(w http.ResponseWriter, r *http.Request)
		want  map[string]any
	}{
		{
			name: "a plain answer has no extension members",
			write: func(w http.ResponseWriter, r *http.Request) {
				problem.Write(w, r, http.StatusNotFound, "No rule has this name.")
			},
			want: map[string]any{
				"type": "about:blank", "title": "Not Found", "status": float64(http.StatusNotFound),
				"detail": "No rule has this name.", "instance": "/api/v1/routes/x",
			},
		},
		{
			name: "a coded answer lists the refused fields",
			write: func(w http.ResponseWriter, r *http.Request) {
				problem.WriteCoded(w, r, http.StatusUnprocessableEntity, "The route fails the checks.", "validation",
					[]problem.FieldError{{Field: "budgets.maxTokens", Message: "above the ceiling"}})
			},
			want: map[string]any{
				"type": "about:blank", "title": "Unprocessable Entity", "status": float64(http.StatusUnprocessableEntity),
				"detail": "The route fails the checks.", "instance": "/api/v1/routes/x", "code": "validation",
				"errors": []any{map[string]any{"field": "budgets.maxTokens", "message": "above the ceiling"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			tt.write(rec, httptest.NewRequest(http.MethodPut, "/api/v1/routes/x", nil))

			require.Equal(t, problem.ContentType, rec.Header().Get("Content-Type"))

			var got map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, tt.want, got)
		})
	}
}
