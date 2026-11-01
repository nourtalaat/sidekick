//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// serviceNotesBody is the notes about a service as the API returns them.
type serviceNotesBody struct {
	Service       string  `json:"service"`
	Text          string  `json:"text"`
	UpdatedAt     *string `json:"updatedAt"`
	UpdatedByName *string `json:"updatedByName"`
}

// serviceNotesListBody is the body of GET /services/notes.
type serviceNotesListBody struct {
	Services []struct {
		Service       string  `json:"service"`
		Preview       string  `json:"preview"`
		UpdatedAt     string  `json:"updatedAt"`
		UpdatedByName *string `json:"updatedByName"`
	} `json:"services"`
	NextAfter *string `json:"nextAfter"`
}

func decodeNotes(t *testing.T, rec *httptest.ResponseRecorder) serviceNotesBody {
	t.Helper()

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var n serviceNotesBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &n))

	return n
}

// notesPath is the path of the notes about service.
func notesPath(service string) string {
	return "/api/v1/services/" + url.PathEscape(service) + "/notes"
}

func TestListServiceNotes(t *testing.T) {
	t.Parallel()

	s := setupMemory(t)

	t.Run("every service with notes, by name", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, "/api/v1/services/notes", viSession, viCSRF, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var list serviceNotesListBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		require.Len(t, list.Services, 2)
		require.Equal(t, "checkout-api", list.Services[0].Service)
		require.Equal(t, "Checkout pages the on-call about pool exhaustion twice a week.", list.Services[0].Preview)
		require.Equal(t, "Ad", *list.Services[0].UpdatedByName)
		require.Equal(t, "orders-api", list.Services[1].Service)
		require.Nil(t, list.Services[1].UpdatedByName)
		require.Nil(t, list.NextAfter)
	})

	t.Run("pages follow each other", func(t *testing.T) {
		t.Parallel()

		var (
			got  []string
			path = "/api/v1/services/notes?limit=1"
		)

		for range 3 {
			var list serviceNotesListBody

			rec := s.do(t, http.MethodGet, path, viSession, viCSRF, nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))

			for _, n := range list.Services {
				got = append(got, n.Service)
			}

			if list.NextAfter == nil {
				break
			}

			path = "/api/v1/services/notes?limit=1&after=" + url.QueryEscape(*list.NextAfter)
		}

		require.Equal(t, []string{"checkout-api", "orders-api"}, got)
	})

	for _, query := range []string{"?limit=0", "?limit=501"} {
		t.Run("a bad limit "+query, func(t *testing.T) {
			t.Parallel()

			rec := s.do(t, http.MethodGet, "/api/v1/services/notes"+query, viSession, viCSRF, nil)
			require.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestGetServiceNotes(t *testing.T) {
	t.Parallel()

	s := setupMemory(t)

	t.Run("notes with the name of their author", func(t *testing.T) {
		t.Parallel()

		n := decodeNotes(t, s.do(t, http.MethodGet, notesPath("checkout-api"), viSession, viCSRF, nil))
		require.Equal(t, "Checkout pages the on-call about pool exhaustion twice a week.", n.Text)
		require.Equal(t, "Ad", *n.UpdatedByName)
		require.NotNil(t, n.UpdatedAt)
	})

	t.Run("notes a token wrote have no author", func(t *testing.T) {
		t.Parallel()

		n := decodeNotes(t, s.do(t, http.MethodGet, notesPath("orders-api"), viSession, viCSRF, nil))
		require.Nil(t, n.UpdatedByName)
		require.NotNil(t, n.UpdatedAt)
	})

	t.Run("a service without notes is empty", func(t *testing.T) {
		t.Parallel()

		n := decodeNotes(t, s.do(t, http.MethodGet, notesPath("ledger"), viSession, viCSRF, nil))
		require.Equal(t, serviceNotesBody{Service: "ledger"}, n)
	})

	t.Run("a service named notes", func(t *testing.T) {
		t.Parallel()

		n := decodeNotes(t, s.do(t, http.MethodGet, notesPath("notes"), viSession, viCSRF, nil))
		require.Equal(t, "notes", n.Service)
	})

	t.Run("a name with a slash", func(t *testing.T) {
		t.Parallel()

		n := decodeNotes(t, s.do(t, http.MethodGet, notesPath("team/checkout"), viSession, viCSRF, nil))
		require.Equal(t, "team/checkout", n.Service)
	})

	t.Run("a name that is too long", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodGet, notesPath(strings.Repeat("a", 201)), viSession, viCSRF, nil)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
		require.Contains(t, ruleProblemOf(t, rec).fieldMessages(), "service")
	})
}

func TestServiceNotesRoles(t *testing.T) {
	t.Parallel()

	s := setupMemory(t)

	for _, c := range []ruleCaller{viewerCaller, responderCaller, viewerTokenCaller, responderTokenCaller} {
		t.Run("write as "+c.name, func(t *testing.T) {
			t.Parallel()

			rec := s.sendRule(t, c, http.MethodPut, notesPath("checkout-api"), map[string]any{"text": "New notes."})
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		})
	}

	for _, c := range []ruleCaller{viewerCaller, responderCaller, viewerTokenCaller, adminCaller} {
		t.Run("read as "+c.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, "orders-api", decodeNotes(t, s.sendRule(t, c, http.MethodGet, notesPath("orders-api"), nil)).Service)
			require.Equal(t, http.StatusOK, s.sendRule(t, c, http.MethodGet, "/api/v1/services/notes", nil).Code)
		})
	}

	t.Run("nobody signed in", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, http.StatusUnauthorized, s.do(t, http.MethodGet, notesPath("orders-api"), "", "", nil).Code)
	})

	t.Run("a write without the CSRF token", func(t *testing.T) {
		t.Parallel()

		rec := s.do(t, http.MethodPut, notesPath("orders-api"), adSession, "", strings.NewReader(`{"text":"x"}`))
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("a refused write changes nothing", func(t *testing.T) {
		t.Parallel()

		require.Zero(t, s.auditCount(t, "service_notes.updated", "checkout-api"))
		require.Equal(t, "Checkout pages the on-call about pool exhaustion twice a week.", decodeNotes(t, s.do(t, http.MethodGet, notesPath("checkout-api"), viSession, viCSRF, nil)).Text)
	})
}

func TestPutServiceNotes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		caller     ruleCaller
		service    string
		text       string
		wantStatus int
		wantText   string
		wantAction string
		wantActor  string
		// wantAuthor is the name of the author in the answer, empty for none.
		wantAuthor string
		wantField  string
	}{
		{
			name: "new notes", caller: adminCaller, service: "ledger", text: "  Ledger lags after rebuilds.\n",
			wantStatus: http.StatusOK, wantText: "Ledger lags after rebuilds.", wantAction: "service_notes.updated", wantActor: "user", wantAuthor: "Ad",
		},
		{
			name: "changed notes", caller: adminCaller, service: "checkout-api", text: "Checkout is calm now.",
			wantStatus: http.StatusOK, wantText: "Checkout is calm now.", wantAction: "service_notes.updated", wantActor: "user", wantAuthor: "Ad",
		},
		{
			name: "notes written by a token have no author", caller: adminTokenCaller, service: "checkout-api", text: "Written by a bot.",
			wantStatus: http.StatusOK, wantText: "Written by a bot.", wantAction: "service_notes.updated", wantActor: "token",
		},
		{
			name: "empty text deletes the notes", caller: adminCaller, service: "orders-api", text: "  ",
			wantStatus: http.StatusOK, wantAction: "service_notes.deleted", wantActor: "user",
		},
		{
			name: "the same notes write nothing", caller: adminCaller, service: "orders-api", text: "Orders runs a nightly export.",
			wantStatus: http.StatusOK, wantText: "Orders runs a nightly export.",
		},
		{name: "empty text for a service without notes writes nothing", caller: adminCaller, service: "ledger", text: "", wantStatus: http.StatusOK},
		{
			name: "notes that are too long", caller: adminCaller, service: "ledger", text: strings.Repeat("a", 4001),
			wantStatus: http.StatusUnprocessableEntity, wantField: "text",
		},
		{
			name: "notes with control characters", caller: adminCaller, service: "ledger", text: "a\u0007b",
			wantStatus: http.StatusUnprocessableEntity, wantField: "text",
		},
		{
			name: "a service name that is too long", caller: adminCaller, service: strings.Repeat("a", 201), text: "Notes.",
			wantStatus: http.StatusUnprocessableEntity, wantField: "service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := setupMemory(t)

			rec := s.sendRule(t, tt.caller, http.MethodPut, notesPath(tt.service), map[string]any{"text": tt.text})
			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantField != "" {
				require.Contains(t, ruleProblemOf(t, rec).fieldMessages(), tt.wantField)
			}

			if tt.wantStatus == http.StatusOK {
				n := decodeNotes(t, rec)
				require.Equal(t, strings.TrimSpace(tt.service), n.Service)
				require.Equal(t, tt.wantText, n.Text)

				if tt.wantAuthor == "" {
					require.Nil(t, n.UpdatedByName)
				} else {
					require.Equal(t, tt.wantAuthor, *n.UpdatedByName)
				}

				stored := decodeNotes(t, s.do(t, http.MethodGet, notesPath(tt.service), viSession, viCSRF, nil))
				require.Equal(t, n, stored, "the answer is the stored notes")
			}

			var rows int

			require.NoError(t, s.pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_log WHERE target_kind = 'service_notes'").Scan(&rows))

			if tt.wantAction == "" {
				require.Zero(t, rows)

				return
			}

			require.Equal(t, 1, rows)

			var actor string

			require.NoError(t, s.pool.QueryRow(t.Context(),
				"SELECT actor_kind FROM audit_log WHERE action = $1 AND target_id = $2 AND investigation_id IS NULL", tt.wantAction, tt.service).Scan(&actor))
			require.Equal(t, tt.wantActor, actor)
		})
	}

	t.Run("the audit entry has the old and new text", func(t *testing.T) {
		t.Parallel()

		s := setupMemory(t)

		rec := s.sendRule(t, adminCaller, http.MethodPut, notesPath("checkout-api"), map[string]any{"text": "Checkout is calm now."})
		require.Equal(t, http.StatusOK, rec.Code)

		var oldText, newText string

		require.NoError(t, s.pool.QueryRow(t.Context(),
			"SELECT details->'old'->>'text', details->'new'->>'text' FROM audit_log WHERE action = 'service_notes.updated' AND target_id = 'checkout-api'").Scan(&oldText, &newText))
		require.Equal(t, "Checkout pages the on-call about pool exhaustion twice a week.", oldText)
		require.Equal(t, "Checkout is calm now.", newText)
	})

	t.Run("the text is redacted before it is stored", func(t *testing.T) {
		t.Parallel()

		s := setupMemory(t)

		const address = "ana@example.com"

		n := decodeNotes(t, s.sendRule(t, adminCaller, http.MethodPut, notesPath("ledger"), map[string]any{"text": "Ask " + address + " first."}))
		require.NotContains(t, n.Text, address)
		require.Contains(t, n.Text, "[REDACTED:")

		var audited int

		require.NoError(t, s.pool.QueryRow(t.Context(),
			"SELECT count(*) FROM audit_log WHERE target_kind = 'service_notes' AND details::text LIKE '%' || $1 || '%'", address).Scan(&audited))
		require.Zero(t, audited, "the audit log never holds the address")
	})

	t.Run("a name with a slash", func(t *testing.T) {
		t.Parallel()

		s := setupMemory(t)

		n := decodeNotes(t, s.sendRule(t, adminCaller, http.MethodPut, notesPath("team/checkout"), map[string]any{"text": "Team notes."}))
		require.Equal(t, "team/checkout", n.Service)
		require.Equal(t, "Team notes.", decodeNotes(t, s.do(t, http.MethodGet, notesPath("team/checkout"), viSession, viCSRF, nil)).Text)
	})
}
