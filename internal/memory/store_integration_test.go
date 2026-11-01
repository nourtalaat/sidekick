//go:build integration

package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/redact"
)

// The entries of fixture/entries.sql that the store tests use.
var (
	entryE1 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e1")
	entryE2 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e2")
	entryE3 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e3")
	entryE4 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e4")
	entryE5 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e5")
	entryE6 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e6")
	entryE7 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e7")
	entryE8 = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000e8")

	missingEntry = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000ee")
)

// adminID is the user who changes memory in the tests.
var (
	adminID = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000a9")
	admin   = Actor{Kind: audit.ActorUser, ID: adminID.String(), UserID: &adminID}
	token   = Actor{Kind: audit.ActorToken, ID: "0192d3a4-0000-7000-8000-0000000000aa"}
)

// storeSetup returns a Store on a database seeded with the entries and the
// investigations.
func storeSetup(t *testing.T, opts SyncOptions) (*Store, *pgxpool.Pool) {
	t.Helper()

	pool := setup(t, "fixture/investigations.sql", "fixture/entries.sql")

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key"), Custom: []redact.Rule{{Name: "ticket", Pattern: `TICKET-[0-9]+`}}})
	require.NoError(t, err)

	return NewStore(pool, redactor, opts), pool
}

func idsOf(p Page) []uuid.UUID {
	out := make([]uuid.UUID, len(p.Entries))
	for i, e := range p.Entries {
		out[i] = e.InvestigationID
	}

	return out
}

// memoryAudit is a row of the audit log about memory.
type memoryAudit struct {
	Action          string
	ActorKind       string
	TargetKind      string
	InvestigationID *uuid.UUID
	Old, New        map[string]any
}

// auditRows returns the audit rows about the memory entry id, oldest first.
func auditRows(t *testing.T, pool *pgxpool.Pool, kind, id string) []memoryAudit {
	t.Helper()

	rows, err := pool.Query(t.Context(),
		"SELECT action, actor_kind, target_kind, investigation_id, details FROM audit_log WHERE target_kind = $1 AND target_id = $2 ORDER BY id", kind, id)
	require.NoError(t, err)

	defer rows.Close()

	var out []memoryAudit

	for rows.Next() {
		var (
			a       memoryAudit
			details []byte
		)

		require.NoError(t, rows.Scan(&a.Action, &a.ActorKind, &a.TargetKind, &a.InvestigationID, &details))

		var d struct {
			Old, New map[string]any
		}

		require.NoError(t, json.Unmarshal(details, &d))

		a.Old, a.New = d.Old, d.New
		out = append(out, a)
	}

	require.NoError(t, rows.Err())

	return out
}

func TestStoreList(t *testing.T) {
	t.Parallel()

	s, _ := storeSetup(t, SyncOptions{})

	tests := []struct {
		name   string
		params ListParams
		want   []uuid.UUID
	}{
		{name: "every entry, newest confirmation first", want: []uuid.UUID{entryE3, entryE8, entryE5, entryE2, entryE4, entryE1, entryE6, entryE7}},
		{name: "one service", params: ListParams{Service: "checkout-api"}, want: []uuid.UUID{entryE5, entryE2, entryE4, entryE1}},
		{name: "one verdict", params: ListParams{Verdict: "flapping"}, want: []uuid.UUID{entryE2}},
		{name: "words", params: ListParams{Search: "debug logs"}, want: []uuid.UUID{entryE8, entryE7}},
		{name: "words find disabled entries", params: ListParams{Search: "ledger"}, want: []uuid.UUID{entryE3}},
		{name: "words in the cause", params: ListParams{Search: "memory node pool"}, want: []uuid.UUID{entryE4}},
		{name: "only disabled", params: ListParams{Disabled: new(true)}, want: []uuid.UUID{entryE3, entryE8, entryE5}},
		{name: "only enabled", params: ListParams{Disabled: new(false)}, want: []uuid.UUID{entryE2, entryE4, entryE1, entryE6, entryE7}},
		{name: "every filter", params: ListParams{Service: "billing", Verdict: "true_positive", Search: "disk", Disabled: new(true)}, want: []uuid.UUID{entryE8}},
		{name: "filters that exclude each other", params: ListParams{Service: "ledger", Verdict: "flapping"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			page, err := s.List(t.Context(), tt.params)
			require.NoError(t, err)
			require.Equal(t, tt.want, idsOf(page))
			require.Nil(t, page.Next)
		})
	}

	t.Run("pages follow each other, with ties broken by the ID", func(t *testing.T) {
		t.Parallel()

		s, pool := storeSetup(t, SyncOptions{})

		// The same confirmation time as e2, and an ID after it.
		_, err := pool.Exec(t.Context(), `
			INSERT INTO memory_entries (investigation_id, fingerprint, verdict, summary, confirmed_at)
			VALUES ('0192d3a4-0000-7000-8000-0000000000ea', 'fp-x', 'true_positive', 'Same time as e2.', '2026-10-22 10:00:00+00')`)
		require.NoError(t, err)

		var (
			got  []uuid.UUID
			from = ListParams{PageSize: 3}
		)

		for range 5 {
			page, err := s.List(t.Context(), from)
			require.NoError(t, err)

			got = append(got, idsOf(page)...)

			if page.Next == nil {
				break
			}

			from.AfterAt, from.AfterID = page.Next.ConfirmedAt, page.Next.InvestigationID
		}

		tie := uuid.MustParse("0192d3a4-0000-7000-8000-0000000000ea")
		require.Equal(t, []uuid.UUID{entryE3, entryE8, entryE5, tie, entryE2, entryE4, entryE1, entryE6, entryE7}, got)
	})

	t.Run("the fields of an entry", func(t *testing.T) {
		t.Parallel()

		page, err := s.List(t.Context(), ListParams{Service: "checkout-api", Verdict: "true_positive", Disabled: new(false), Search: "deploy"})
		require.NoError(t, err)
		require.Len(t, page.Entries, 1)

		e := page.Entries[0]
		require.Equal(t, entryE1, e.InvestigationID)
		require.Equal(t, "CheckoutLatencyP99", e.AlertName)
		require.Equal(t, "Deploy 4f2c1a added a slow query.", e.Cause)
		require.False(t, e.Disabled)
	})
}

func TestStoreGet(t *testing.T) {
	t.Parallel()

	s, _ := storeSetup(t, SyncOptions{})

	e, err := s.Get(t.Context(), entryE2)
	require.NoError(t, err)
	require.Equal(t, "flapping", e.Verdict)
	require.Empty(t, e.Cause)

	_, err = s.Get(t.Context(), missingEntry)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStoreUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		actor Actor
		id    uuid.UUID
		edit  Edit
		// want is the entry afterwards.
		wantSummary, wantCause string
		wantDisabled           bool
		// wantActions are the audit actions written.
		wantActions []string
	}{
		{
			name: "new text", actor: admin, id: entryE1, edit: Edit{Summary: " Pool ran out. ", Cause: "Deploy 9"},
			wantSummary: "Pool ran out.", wantCause: "Deploy 9", wantActions: []string{"memory.updated"},
		},
		{
			name: "a cause taken away", actor: admin, id: entryE1, edit: Edit{Summary: "p99 latency rose after a deploy."},
			wantSummary: "p99 latency rose after a deploy.", wantActions: []string{"memory.updated"},
		},
		{
			name: "disabled by a token", actor: token, id: entryE1,
			edit:        Edit{Summary: "p99 latency rose after a deploy.", Cause: "Deploy 4f2c1a added a slow query.", Disabled: true},
			wantSummary: "p99 latency rose after a deploy.", wantCause: "Deploy 4f2c1a added a slow query.", wantDisabled: true,
			wantActions: []string{"memory.disabled"},
		},
		{
			name: "enabled", actor: admin, id: entryE5, edit: Edit{Summary: "Disabled: p99 latency rose in the US region."},
			wantSummary: "Disabled: p99 latency rose in the US region.", wantActions: []string{"memory.enabled"},
		},
		{
			name: "new text and enabled is one update", actor: admin, id: entryE5, edit: Edit{Summary: "Back on."},
			wantSummary: "Back on.", wantActions: []string{"memory.updated"},
		},
		{
			name: "nothing changed", actor: admin, id: entryE1,
			edit:        Edit{Summary: "p99 latency rose after a deploy.", Cause: "Deploy 4f2c1a added a slow query."},
			wantSummary: "p99 latency rose after a deploy.", wantCause: "Deploy 4f2c1a added a slow query.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, pool := storeSetup(t, SyncOptions{})

			got, err := s.Update(t.Context(), tt.actor, tt.id, tt.edit)
			require.NoError(t, err)
			require.Equal(t, tt.wantSummary, got.Summary)
			require.Equal(t, tt.wantCause, got.Cause)
			require.Equal(t, tt.wantDisabled, got.Disabled)

			rows := auditRows(t, pool, "memory_entry", tt.id.String())
			actions := make([]string, len(rows))

			for i, r := range rows {
				actions[i] = r.Action

				require.Equal(t, tt.actor.Kind, r.ActorKind)
				require.NotNil(t, r.InvestigationID)
				require.Equal(t, tt.id, *r.InvestigationID)
			}

			require.ElementsMatch(t, tt.wantActions, actions)
		})
	}

	t.Run("the audit entry has the old and new values", func(t *testing.T) {
		t.Parallel()

		s, pool := storeSetup(t, SyncOptions{})

		_, err := s.Update(t.Context(), admin, entryE1, Edit{Summary: "New summary.", Cause: "New cause.", Disabled: true})
		require.NoError(t, err)

		rows := auditRows(t, pool, "memory_entry", entryE1.String())
		require.Len(t, rows, 1)
		require.Equal(t, map[string]any{"summary": "p99 latency rose after a deploy.", "cause": "Deploy 4f2c1a added a slow query.", "disabled": false}, rows[0].Old)
		require.Equal(t, map[string]any{"summary": "New summary.", "cause": "New cause.", "disabled": true}, rows[0].New)
	})

	t.Run("a long summary the model wrote can still be disabled", func(t *testing.T) {
		t.Parallel()

		s, pool := storeSetup(t, SyncOptions{})
		long := strings.Repeat("a", MaxSummaryChars+500)

		_, err := pool.Exec(t.Context(), "UPDATE memory_entries SET summary = $1 WHERE investigation_id = $2", long, entryE1)
		require.NoError(t, err)

		got, err := s.Update(t.Context(), admin, entryE1, Edit{Summary: long, Cause: "Deploy 4f2c1a added a slow query.", Disabled: true})
		require.NoError(t, err)
		require.Equal(t, long, got.Summary)
		require.True(t, got.Disabled)
	})

	t.Run("an entry that does not exist", func(t *testing.T) {
		t.Parallel()

		s, _ := storeSetup(t, SyncOptions{})

		_, err := s.Update(t.Context(), admin, missingEntry, Edit{Summary: "A summary."})
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestStoreUpdateRejectsBadText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		edit      Edit
		wantField string
	}{
		{name: "an empty summary", edit: Edit{Summary: "  "}, wantField: "summary"},
		{name: "a summary that is too long", edit: Edit{Summary: strings.Repeat("a", MaxSummaryChars+1)}, wantField: "summary"},
		{name: "a cause that is too long", edit: Edit{Summary: "A summary.", Cause: strings.Repeat("a", MaxCauseChars+1)}, wantField: "cause"},
		{name: "control characters", edit: Edit{Summary: "a\x00b"}, wantField: "summary"},
		{
			name: "a summary that is too long once the secret is a placeholder",
			edit: Edit{Summary: strings.Repeat("a", MaxSummaryChars-10) + " TICKET-12"}, wantField: "summary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, pool := storeSetup(t, SyncOptions{})

			_, err := s.Update(t.Context(), admin, entryE1, tt.edit)

			invalid, ok := errors.AsType[*InvalidError](err)
			require.True(t, ok, "want an *InvalidError, got %v", err)
			require.Equal(t, tt.wantField, invalid.Field)
			require.Empty(t, auditRows(t, pool, "memory_entry", entryE1.String()))
		})
	}
}

func TestStoreUpdateRedactsTheText(t *testing.T) {
	t.Parallel()

	s, pool := storeSetup(t, SyncOptions{})

	got, err := s.Update(t.Context(), admin, entryE1, Edit{Summary: "Fixed in TICKET-4711.", Cause: "See TICKET-4712."})
	require.NoError(t, err)
	require.NotContains(t, got.Summary, "TICKET-4711")
	require.Contains(t, got.Summary, "[REDACTED:ticket:")
	require.Contains(t, got.Cause, "[REDACTED:ticket:")

	rows := auditRows(t, pool, "memory_entry", entryE1.String())
	require.Len(t, rows, 1)
	require.NotContains(t, rows[0].New["summary"], "TICKET-4711")
}

func TestStoreUpdateEnqueuesTheEmbedJob(t *testing.T) {
	t.Parallel()

	jobs := func(pool *pgxpool.Pool) int {
		return countRows(t, pool, "SELECT count(*) FROM jobs WHERE kind = 'embed_memory'")
	}

	tests := []struct {
		name string
		opts SyncOptions
		edit Edit
		want int
	}{
		{name: "a new summary with vector memory on", opts: SyncOptions{Embed: true}, edit: Edit{Summary: "New."}, want: 1},
		{name: "a new cause with vector memory on", opts: SyncOptions{Embed: true}, edit: Edit{Summary: "p99 latency rose after a deploy.", Cause: "New."}, want: 1},
		{name: "a new summary with vector memory off", edit: Edit{Summary: "New."}, want: 0},
		{
			name: "disabling changes no text", opts: SyncOptions{Embed: true},
			edit: Edit{Summary: "p99 latency rose after a deploy.", Cause: "Deploy 4f2c1a added a slow query.", Disabled: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, pool := storeSetup(t, tt.opts)

			_, err := s.Update(t.Context(), admin, entryE1, tt.edit)
			require.NoError(t, err)
			require.Equal(t, tt.want, jobs(pool))
		})
	}
}

func TestStoreDelete(t *testing.T) {
	t.Parallel()

	t.Run("the entry is gone and the audit entry keeps what it said", func(t *testing.T) {
		t.Parallel()

		s, pool := storeSetup(t, SyncOptions{})

		require.NoError(t, s.Delete(t.Context(), admin, entryE2))

		_, err := s.Get(t.Context(), entryE2)
		require.ErrorIs(t, err, ErrNotFound)

		rows := auditRows(t, pool, "memory_entry", entryE2.String())
		require.Len(t, rows, 1)
		require.Equal(t, "memory.deleted", rows[0].Action)
		require.Equal(t, "flapping", rows[0].Old["verdict"])
		require.Equal(t, "p99 latency crossed the threshold and fell back every few minutes.", rows[0].Old["summary"])
		require.Empty(t, rows[0].New)
	})

	t.Run("an entry that does not exist", func(t *testing.T) {
		t.Parallel()

		s, pool := storeSetup(t, SyncOptions{})

		require.ErrorIs(t, s.Delete(t.Context(), admin, missingEntry), ErrNotFound)
		require.Zero(t, countRows(t, pool, "SELECT count(*) FROM audit_log WHERE action = 'memory.deleted'"))
	})
}

// syncEntry runs Sync for the investigation id.
func syncEntry(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()

	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		return Sync(t.Context(), tx, id, SyncOptions{})
	})
	require.NoError(t, err)
}

func TestAnEditedEntryIsReplacedByTheNextConfirmationChange(t *testing.T) {
	t.Parallel()

	s, pool := storeSetup(t, SyncOptions{})

	syncEntry(t, pool, confirmedInvestigation)

	_, err := s.Update(t.Context(), admin, confirmedInvestigation, Edit{Summary: "Edited by hand.", Cause: "Edited cause.", Disabled: true})
	require.NoError(t, err)

	syncEntry(t, pool, confirmedInvestigation)

	got, err := s.Get(t.Context(), confirmedInvestigation)
	require.NoError(t, err)
	require.Equal(t, "p99 latency rose after deploy 4f2c1a.", got.Summary, "the confirmed outcome wins")
	require.Equal(t, "Deploy 4f2c1a", got.Cause)
	require.True(t, got.Disabled, "the flag stays")
}

func TestADeletedEntryComesBackWithTheNextConfirmationChange(t *testing.T) {
	t.Parallel()

	s, pool := storeSetup(t, SyncOptions{})

	syncEntry(t, pool, confirmedInvestigation)
	require.NoError(t, s.Delete(t.Context(), admin, confirmedInvestigation))

	_, err := s.Get(t.Context(), confirmedInvestigation)
	require.ErrorIs(t, err, ErrNotFound)

	syncEntry(t, pool, confirmedInvestigation)

	got, err := s.Get(t.Context(), confirmedInvestigation)
	require.NoError(t, err)
	require.False(t, got.Disabled)
}

func TestDeletingAnEntryDeletesItsEmbedding(t *testing.T) {
	t.Parallel()

	pool := setupVector(t, testDimensions, "fixture/entries.sql")
	s := NewStore(pool, nil, SyncOptions{Embed: true})

	_, err := pool.Exec(t.Context(), `
		INSERT INTO memory_embeddings (investigation_id, model, text_hash, dimensions, embedding, embedded_at)
		VALUES ($1, 'test-model', 'hash', 3, '[1,2,3]', now())`, entryE1)
	require.NoError(t, err)

	require.NoError(t, s.Delete(t.Context(), admin, entryE1))
	require.Zero(t, countRows(t, pool, "SELECT count(*) FROM memory_embeddings"))
}
