//go:build integration

package memory

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// notesStoreSetup is storeSetup with a user for the admin, and notes for
// two services written by that user and by a token.
func notesStoreSetup(t *testing.T) (*Store, func(sql string)) {
	t.Helper()

	s, pool := storeSetup(t, SyncOptions{})

	_, err := pool.Exec(t.Context(), `
		INSERT INTO users (id, oidc_issuer, oidc_subject, name, role, created_at, last_login_at)
		VALUES ($1, 'https://idp.example.com', 'ada', 'Ada Lovelace', 'admin', now(), now());
		INSERT INTO service_notes (service, text, updated_by, updated_at) VALUES
		  ('billing', 'Billing is quiet at night.', $1, '2026-10-20 10:00:00+00'),
		  ('checkout-api', 'Checkout pages the on-call twice a week for pool exhaustion.', NULL, '2026-10-21 10:00:00+00'),
		  ('orders-api', 'Orders runs a nightly export.', $1, '2026-10-22 10:00:00+00')`, adminID)
	require.NoError(t, err)

	return s, func(sql string) {
		t.Helper()

		_, err := pool.Exec(t.Context(), sql)
		require.NoError(t, err)
	}
}

func TestStoreNotes(t *testing.T) {
	t.Parallel()

	s, _ := notesStoreSetup(t)

	t.Run("notes with the name of their author", func(t *testing.T) {
		t.Parallel()

		n, err := s.Notes(t.Context(), "billing")
		require.NoError(t, err)
		require.Equal(t, "Billing is quiet at night.", n.Text)
		require.Equal(t, "Ada Lovelace", n.UpdatedByName)
		require.NotNil(t, n.UpdatedAt)
	})

	t.Run("notes a token wrote have no author name", func(t *testing.T) {
		t.Parallel()

		n, err := s.Notes(t.Context(), "checkout-api")
		require.NoError(t, err)
		require.Empty(t, n.UpdatedByName)
		require.NotNil(t, n.UpdatedAt)
	})

	t.Run("a service without notes", func(t *testing.T) {
		t.Parallel()

		n, err := s.Notes(t.Context(), "ledger")
		require.NoError(t, err)
		require.Equal(t, Notes{Service: "ledger"}, n)
	})

	t.Run("a name that cannot be right", func(t *testing.T) {
		t.Parallel()

		_, err := s.Notes(t.Context(), " ")
		require.Error(t, err)
	})
}

func TestStoreListNotes(t *testing.T) {
	t.Parallel()

	s, _ := notesStoreSetup(t)

	page, err := s.ListNotes(t.Context(), "", 2)
	require.NoError(t, err)
	require.Len(t, page.Services, 2)
	require.Equal(t, "billing", page.Services[0].Service)
	require.Equal(t, "Ada Lovelace", page.Services[0].UpdatedByName)
	require.Equal(t, "checkout-api", page.Services[1].Service)
	require.Equal(t, "Checkout pages the on-call twice a week for pool exhaustion.", page.Services[1].Preview)
	require.Equal(t, "checkout-api", page.Next)

	page, err = s.ListNotes(t.Context(), page.Next, 2)
	require.NoError(t, err)
	require.Len(t, page.Services, 1)
	require.Equal(t, "orders-api", page.Services[0].Service)
	require.Empty(t, page.Next)
}

func TestStoreListNotesCutsThePreview(t *testing.T) {
	t.Parallel()

	s, exec := notesStoreSetup(t)

	exec("UPDATE service_notes SET text = '" + strings.Repeat("a", 500) + "' WHERE service = 'billing'")

	page, err := s.ListNotes(t.Context(), "", 1)
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("a", 120), page.Services[0].Preview)
}

func TestStoreSetNotes(t *testing.T) {
	t.Parallel()

	t.Run("new notes", func(t *testing.T) {
		t.Parallel()

		s, _ := notesStoreSetup(t)

		n, err := s.SetNotes(t.Context(), admin, "ledger", "  Ledger lags after rebuilds.\n")
		require.NoError(t, err)
		require.Equal(t, "Ledger lags after rebuilds.", n.Text)
		require.Equal(t, "Ada Lovelace", n.UpdatedByName)
	})

	t.Run("a token writes notes with no author", func(t *testing.T) {
		t.Parallel()

		s, _ := notesStoreSetup(t)

		n, err := s.SetNotes(t.Context(), token, "billing", "Written by a token.")
		require.NoError(t, err)
		require.Equal(t, "Written by a token.", n.Text)
		require.Empty(t, n.UpdatedByName)
	})
}

func TestStoreSetNotesAudits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		service string
		text    string
		actor   Actor
		// want are the actions, then the old and new text of the last row.
		wantActions      []string
		wantOld, wantNew any
	}{
		{name: "new notes", service: "ledger", text: "Ledger lags.", actor: admin, wantActions: []string{"service_notes.updated"}, wantOld: nil, wantNew: "Ledger lags."},
		{
			name: "changed notes", service: "billing", text: "Billing is busy.", actor: admin,
			wantActions: []string{"service_notes.updated"}, wantOld: "Billing is quiet at night.", wantNew: "Billing is busy.",
		},
		{
			name: "emptied notes are deleted", service: "billing", text: "  ", actor: token,
			wantActions: []string{"service_notes.deleted"}, wantOld: "Billing is quiet at night.", wantNew: nil,
		},
		{name: "the same notes", service: "billing", text: " Billing is quiet at night. ", actor: admin},
		{name: "empty notes of a service without any", service: "ledger", text: "", actor: admin},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, _ := notesStoreSetup(t)
			pool := s.pool

			_, err := s.SetNotes(t.Context(), tt.actor, tt.service, tt.text)
			require.NoError(t, err)

			rows := auditRows(t, pool, "service_notes", tt.service)
			actions := make([]string, len(rows))

			for i, r := range rows {
				actions[i] = r.Action

				require.Equal(t, tt.actor.Kind, r.ActorKind)
				require.Nil(t, r.InvestigationID)
			}

			require.ElementsMatch(t, tt.wantActions, actions)

			if len(rows) == 0 {
				return
			}

			last := rows[len(rows)-1]
			require.Equal(t, tt.wantOld, last.Old["text"])
			require.Equal(t, tt.wantNew, last.New["text"])
		})
	}
}

func TestStoreSetNotesDeletes(t *testing.T) {
	t.Parallel()

	s, _ := notesStoreSetup(t)

	n, err := s.SetNotes(t.Context(), admin, "billing", "")
	require.NoError(t, err)
	require.Equal(t, Notes{Service: "billing"}, n)

	page, err := s.ListNotes(t.Context(), "", 10)
	require.NoError(t, err)
	require.Len(t, page.Services, 2)
}

func TestStoreSetNotesRedactsAndChecks(t *testing.T) {
	t.Parallel()

	s, _ := notesStoreSetup(t)

	t.Run("redacts before storing", func(t *testing.T) {
		t.Parallel()

		n, err := s.SetNotes(t.Context(), admin, "ledger", "Ask about TICKET-4711 first.")
		require.NoError(t, err)
		require.NotContains(t, n.Text, "TICKET-4711")
		require.Contains(t, n.Text, "[REDACTED:ticket:")
	})

	t.Run("the limit applies to the redacted text", func(t *testing.T) {
		t.Parallel()

		_, err := s.SetNotes(t.Context(), admin, "ledger2", strings.Repeat("a", MaxNotesChars-10)+" TICKET-12")
		require.Error(t, err)
	})

	t.Run("a bad service name", func(t *testing.T) {
		t.Parallel()

		_, err := s.SetNotes(t.Context(), admin, "a\nb", "Notes.")
		require.Error(t, err)
	})
}
