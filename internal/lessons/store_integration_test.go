//go:build integration

package lessons

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The lessons of fixture/lessons.sql that the store tests change.
const (
	lessonProposed = 13 // drafted from feedback, for checkout-api, waits for review
	lessonActive   = 2  // for checkout-api, active
	lessonGlobal   = 8  // global, active
	lessonDisabled = 12
	lessonRejected = 17
)

// admin is the user who changes lessons in the tests.
var (
	adminID = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000a9")
	admin   = Actor{Kind: audit.ActorUser, ID: adminID.String(), UserID: &adminID}
)

// storeSetup returns a Store on a database seeded with the fixtures.
func storeSetup(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()

	pool := setup(t)
	storetest.LoadFixture(t, pool, "fixture/lessons.sql")

	return NewStore(pool), pool
}

// auditActions returns the audit actions written for lesson id, oldest
// first.
func auditActions(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT action FROM audit_log WHERE target_kind = 'lesson' AND target_id = $1 ORDER BY id", id.String())
	require.NoError(t, err)

	defer rows.Close()

	var out []string

	for rows.Next() {
		var a string

		require.NoError(t, rows.Scan(&a))

		out = append(out, a)
	}

	require.NoError(t, rows.Err())

	return out
}

func TestStoreCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		n     New
		actor Actor
		want  Stored
		// wantInvalid is the field of the *InvalidError, empty for none.
		wantInvalid string
	}{
		{
			name:  "a global lesson from the web UI",
			n:     New{Text: "  Always say what you could not check.\n", Origin: OriginUI, Status: StatusActive},
			actor: admin,
			want:  Stored{Text: "Always say what you could not check.", Status: StatusActive, Origin: OriginUI, CreatedBy: &adminID},
		},
		{
			name:  "a scoped lesson from Slack by someone who is no user",
			n:     New{Text: "Check the deploys.", Scope: Scope{Service: " checkout-api ", Route: "payments"}, Origin: OriginSlack, Status: StatusActive},
			actor: Actor{Kind: audit.ActorUser, ID: "U01ANA"},
			want:  Stored{Text: "Check the deploys.", Scope: Scope{Service: "checkout-api", Route: "payments"}, Status: StatusActive, Origin: OriginSlack},
		},
		{
			name:  "a lesson that waits for review",
			n:     New{Text: "Check the deploys.", Scope: Scope{Alert: "DiskFull"}, Origin: OriginUI, Status: StatusProposed},
			actor: admin,
			want:  Stored{Text: "Check the deploys.", Scope: Scope{Alert: "DiskFull"}, Status: StatusProposed, Origin: OriginUI, CreatedBy: &adminID},
		},
		{
			name:  "text of 400 characters",
			n:     New{Text: strings.Repeat("é", 400), Origin: OriginUI, Status: StatusActive},
			actor: admin,
			want:  Stored{Text: strings.Repeat("é", 400), Status: StatusActive, Origin: OriginUI, CreatedBy: &adminID},
		},
		{name: "no text", n: New{Text: " ", Origin: OriginUI, Status: StatusActive}, actor: admin, wantInvalid: "text"},
		{name: "text of 401 characters", n: New{Text: strings.Repeat("é", 401), Origin: OriginUI, Status: StatusActive}, actor: admin, wantInvalid: "text"},
		{name: "text with a control character", n: New{Text: "a\x00b", Origin: OriginUI, Status: StatusActive}, actor: admin, wantInvalid: "text"},
		{
			name:        "a scope value that is too long",
			n:           New{Text: "Check.", Scope: Scope{Route: strings.Repeat("r", 201)}, Origin: OriginUI, Status: StatusActive},
			actor:       admin,
			wantInvalid: "scope.route",
		},
		{
			name:        "a scope value with a control character",
			n:           New{Text: "Check.", Scope: Scope{Alert: "a\nb"}, Origin: OriginUI, Status: StatusActive},
			actor:       admin,
			wantInvalid: "scope.alert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, pool := storeSetup(t)

			got, err := store.Create(t.Context(), tt.actor, tt.n)

			if tt.wantInvalid != "" {
				invalid, ok := errors.AsType[*InvalidError](err)
				require.True(t, ok, "want an *InvalidError, got %v", err)
				require.Equal(t, tt.wantInvalid, invalid.Field)

				var n int

				require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_log WHERE action = 'lesson.created'").Scan(&n))
				require.Zero(t, n)

				return
			}

			require.NoError(t, err)

			tt.want.ID, tt.want.Version = got.ID, 1
			require.Equal(t, tt.want.Text, got.Text)
			require.Equal(t, tt.want.Scope, got.Scope)
			require.Equal(t, tt.want.Status, got.Status)
			require.Equal(t, tt.want.Origin, got.Origin)
			require.Equal(t, tt.want.CreatedBy, got.CreatedBy)
			require.Equal(t, 1, got.Version)
			require.Empty(t, got.Feedback)
			require.Equal(t, []string{"lesson.created"}, auditActions(t, pool, got.ID))
		})
	}
}

func TestStoreUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		lesson int
		edit   Edit
		// version is the version to send; zero means the current one.
		version     int
		wantStatus  string
		wantText    string
		wantScope   Scope
		wantAudit   string
		wantErr     error
		wantInvalid string
	}{
		{
			name:       "an edit of a lesson that waits keeps it waiting",
			lesson:     lessonProposed,
			edit:       Edit{Text: "Check the pool, then the deploy.", Scope: Scope{Service: "checkout-api", Route: "payments"}},
			wantStatus: StatusProposed, wantText: "Check the pool, then the deploy.", wantScope: Scope{Service: "checkout-api", Route: "payments"},
			wantAudit: "lesson.updated",
		},
		{
			name:       "rejecting a lesson that waits",
			lesson:     lessonProposed,
			edit:       Edit{Text: "Waiting for review.", Scope: Scope{Service: "checkout-api"}, Status: StatusRejected},
			wantStatus: StatusRejected, wantText: "Waiting for review.", wantScope: Scope{Service: "checkout-api"}, wantAudit: "lesson.rejected",
		},
		{
			name:       "disabling an active lesson",
			lesson:     lessonActive,
			edit:       Edit{Text: "Ask the payments team about holiday traffic.", Scope: Scope{Service: "checkout-api"}, Status: StatusDisabled},
			wantStatus: StatusDisabled, wantText: "Ask the payments team about holiday traffic.", wantScope: Scope{Service: "checkout-api"}, wantAudit: "lesson.disabled",
		},
		{
			name:       "enabling a disabled lesson",
			lesson:     lessonDisabled,
			edit:       Edit{Text: "Disabled.", Scope: Scope{Service: "checkout-api"}, Status: StatusActive},
			wantStatus: StatusActive, wantText: "Disabled.", wantScope: Scope{Service: "checkout-api"}, wantAudit: "lesson.enabled",
		},
		{
			name:       "a global lesson may stay global",
			lesson:     lessonGlobal,
			edit:       Edit{Text: "Say what you could not check."},
			wantStatus: StatusActive, wantText: "Say what you could not check.", wantAudit: "lesson.updated",
		},
		{
			name:        "a lesson drafted from feedback keeps a scope",
			lesson:      lessonProposed,
			edit:        Edit{Text: "No scope."},
			wantInvalid: "scope",
		},
		{
			name:        "a lesson that waits becomes active by approval",
			lesson:      lessonProposed,
			edit:        Edit{Text: "Check.", Scope: Scope{Service: "checkout-api"}, Status: StatusActive},
			wantInvalid: "status",
		},
		{
			name:        "a rejected lesson becomes active by approval too",
			lesson:      lessonRejected,
			edit:        Edit{Text: "Check.", Scope: Scope{Service: "checkout-api"}, Status: StatusActive},
			wantInvalid: "status",
		},
		{
			name:        "an active lesson is not rejected",
			lesson:      lessonActive,
			edit:        Edit{Text: "Check.", Scope: Scope{Service: "checkout-api"}, Status: StatusRejected},
			wantInvalid: "status",
		},
		{
			name:        "an unknown status",
			lesson:      lessonActive,
			edit:        Edit{Text: "Check.", Scope: Scope{Service: "checkout-api"}, Status: "archived"},
			wantInvalid: "status",
		},
		{
			name:        "text that is too long",
			lesson:      lessonActive,
			edit:        Edit{Text: strings.Repeat("a", 401), Scope: Scope{Service: "checkout-api"}},
			wantInvalid: "text",
		},
		{
			name:    "a version that is out of date",
			lesson:  lessonActive,
			edit:    Edit{Text: "Check.", Scope: Scope{Service: "checkout-api"}},
			version: 99,
			wantErr: ErrStale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, pool := storeSetup(t)
			id := lessonID(tt.lesson)

			before, err := store.Get(t.Context(), id)
			require.NoError(t, err)

			version := tt.version
			if version == 0 {
				version = before.Version
			}

			got, err := store.Update(t.Context(), admin, id, version, tt.edit)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantInvalid != "":
				invalid, ok := errors.AsType[*InvalidError](err)
				require.True(t, ok, "want an *InvalidError, got %v", err)
				require.Equal(t, tt.wantInvalid, invalid.Field)
			default:
				require.NoError(t, err)
				require.Equal(t, tt.wantStatus, got.Status)
				require.Equal(t, tt.wantText, got.Text)
				require.Equal(t, tt.wantScope, got.Scope)
				require.Equal(t, before.Version+1, got.Version)
				require.Equal(t, before.Origin, got.Origin)
				require.Equal(t, []string{tt.wantAudit}, auditActions(t, pool, id))

				return
			}

			after, err := store.Get(t.Context(), id)
			require.NoError(t, err)
			require.Equal(t, before, after, "a refused change leaves the lesson as it was")
			require.Empty(t, auditActions(t, pool, id))
		})
	}
}

func TestStoreUpdateNotFound(t *testing.T) {
	t.Parallel()

	store, _ := storeSetup(t)

	_, err := store.Update(t.Context(), admin, uuid.New(), 1, Edit{Text: "Check.", Scope: Scope{Service: "a"}})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestStoreApprove(t *testing.T) {
	t.Parallel()

	t.Run("a lesson that waits becomes active, with its approver", func(t *testing.T) {
		t.Parallel()

		store, pool := storeSetup(t)
		id := lessonID(lessonProposed)

		got, err := store.Approve(t.Context(), admin, id, 1)
		require.NoError(t, err)
		require.Equal(t, StatusActive, got.Status)
		require.Equal(t, &adminID, got.ApprovedBy)
		require.Equal(t, 2, got.Version)
		require.Equal(t, []string{"lesson.approved"}, auditActions(t, pool, id))
		require.Len(t, got.Feedback, 0, "the fixture's lesson has no feedback row")
	})

	t.Run("an approval by someone who is no user has no approver", func(t *testing.T) {
		t.Parallel()

		store, _ := storeSetup(t)

		got, err := store.Approve(t.Context(), Actor{Kind: audit.ActorToken, ID: "token-1"}, lessonID(lessonProposed), 1)
		require.NoError(t, err)
		require.Nil(t, got.ApprovedBy)
	})

	tests := []struct {
		name    string
		lesson  int
		id      uuid.UUID
		version int
		wantErr error
	}{
		{name: "a version that is out of date", lesson: lessonProposed, version: 5, wantErr: ErrStale},
		{name: "an active lesson", lesson: lessonActive, version: 1, wantErr: ErrNotProposed},
		{name: "a rejected lesson", lesson: lessonRejected, version: 1, wantErr: ErrNotProposed},
		{name: "a lesson that does not exist", id: uuid.New(), version: 1, wantErr: ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, pool := storeSetup(t)

			id := tt.id
			if id == uuid.Nil {
				id = lessonID(tt.lesson)
			}

			_, err := store.Approve(t.Context(), admin, id, tt.version)
			require.ErrorIs(t, err, tt.wantErr)
			require.Empty(t, auditActions(t, pool, id))
		})
	}
}

func TestStoreDelete(t *testing.T) {
	t.Parallel()

	store, pool := storeSetup(t)
	id := lessonID(lessonActive)

	_, err := pool.Exec(t.Context(), "INSERT INTO lesson_uses (lesson_id, investigation_id) VALUES ($1, $2)", id, finalInvestigation)
	require.NoError(t, err)

	require.NoError(t, store.Delete(t.Context(), admin, id))

	_, err = store.Get(t.Context(), id)
	require.ErrorIs(t, err, ErrNotFound)

	var uses int

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM lesson_uses").Scan(&uses))
	require.Zero(t, uses, "the record of use goes with the lesson")

	var text string

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT details->'old'->>'text' FROM audit_log WHERE action = 'lesson.deleted' AND target_id = $1", id.String()).Scan(&text))
	require.Equal(t, "Ask the payments team about holiday traffic.", text, "the audit entry keeps the text")

	require.ErrorIs(t, store.Delete(t.Context(), admin, id), ErrNotFound)
}

func TestStoreList(t *testing.T) {
	t.Parallel()

	store, _ := storeSetup(t)

	all, err := store.List(t.Context(), ListParams{PageSize: MaxPageSize})
	require.NoError(t, err)
	require.Len(t, all.Lessons, 19)
	require.Nil(t, all.Next)

	ids := func(p Page) []uuid.UUID {
		out := make([]uuid.UUID, len(p.Lessons))
		for i, l := range p.Lessons {
			out[i] = l.ID
		}

		return out
	}

	tests := []struct {
		name   string
		params ListParams
		// want are the numbers of the lessons, in the order of the list.
		want []int
	}{
		{name: "waiting for review", params: ListParams{Status: StatusProposed, PageSize: 50}, want: []int{lessonProposed}},
		{name: "rejected", params: ListParams{Status: StatusRejected, PageSize: 50}, want: []int{lessonRejected}},
		{name: "one service", params: ListParams{Service: "search-api", PageSize: 50}, want: []int{10}},
		{name: "words in the text", params: ListParams{Search: "playbook", PageSize: 50}, want: []int{7}},
		{name: "words and a status that exclude each other", params: ListParams{Search: "playbook", Status: StatusDisabled, PageSize: 50}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			page, err := store.List(t.Context(), tt.params)
			require.NoError(t, err)

			var want []uuid.UUID
			for _, n := range tt.want {
				want = append(want, lessonID(n))
			}

			require.Equal(t, want, append([]uuid.UUID(nil), ids(page)...))
		})
	}

	t.Run("pages follow each other without gaps or repeats", func(t *testing.T) {
		t.Parallel()

		var (
			got    []uuid.UUID
			params = ListParams{PageSize: 5}
			pages  int
		)

		for {
			page, err := store.List(t.Context(), params)
			require.NoError(t, err)

			pages++

			got = append(got, ids(page)...)

			if page.Next == nil {
				break
			}

			require.Len(t, page.Lessons, 5)

			params.AfterAt, params.AfterID = page.Next.CreatedAt, page.Next.ID
		}

		require.Equal(t, 4, pages)
		require.Equal(t, ids(all), got)
	})

	t.Run("a lesson comes with the feedback it was drafted from", func(t *testing.T) {
		t.Parallel()

		store, pool := storeSetup(t)

		_, err := pool.Exec(t.Context(), `
			INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, reason, user_id, channel, created_at, updated_at)
			VALUES ('0192d3a4-0000-7000-8000-0000000000f1', $1, 'verdict', 'run:1/verdict', 'wrong', 'The deploy.', $2, 'ui', now(), now())`,
			finalInvestigation, uuid.New())
		require.NoError(t, err)

		_, err = pool.Exec(t.Context(), "UPDATE lessons SET feedback_ids = ARRAY['0192d3a4-0000-7000-8000-0000000000f1']::uuid[] WHERE id = $1", lessonID(lessonProposed))
		require.NoError(t, err)

		got, err := store.Get(t.Context(), lessonID(lessonProposed))
		require.NoError(t, err)
		require.Equal(t, []FeedbackLink{{
			FeedbackID: uuid.MustParse("0192d3a4-0000-7000-8000-0000000000f1"), InvestigationID: finalInvestigation,
		}}, got.Feedback)
	})
}

func TestStoreCounts(t *testing.T) {
	t.Parallel()

	store, _ := storeSetup(t)

	got, err := store.Counts(t.Context())
	require.NoError(t, err)
	require.Equal(t, map[string]int{StatusActive: 16, StatusProposed: 1, StatusDisabled: 1, StatusRejected: 1}, got)
}

func TestUsed(t *testing.T) {
	t.Parallel()

	store, pool := storeSetup(t)

	for _, n := range []int{lessonGlobal, lessonActive, lessonDisabled} {
		_, err := pool.Exec(t.Context(), "INSERT INTO lesson_uses (lesson_id, investigation_id) VALUES ($1, $2)", lessonID(n), finalInvestigation)
		require.NoError(t, err)
	}

	got, err := Used(t.Context(), pool, finalInvestigation, 50)
	require.NoError(t, err)

	// In the order they were written, with the status they have now.
	require.Len(t, got, 3)
	require.Equal(t, []uuid.UUID{lessonID(lessonDisabled), lessonID(lessonActive), lessonID(lessonGlobal)}, []uuid.UUID{got[0].ID, got[1].ID, got[2].ID})
	require.Equal(t, StatusDisabled, got[0].Status)
	require.Equal(t, Scope{Service: "checkout-api"}, got[1].Scope)

	none, err := Used(t.Context(), pool, runningInvestigation, 50)
	require.NoError(t, err)
	require.Empty(t, none)

	_, err = store.Get(t.Context(), lessonID(lessonGlobal))
	require.NoError(t, err)
}
