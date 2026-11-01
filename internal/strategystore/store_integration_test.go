//go:build integration

package strategystore_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/strategystore"
)

func TestSave(t *testing.T) {
	t.Parallel()

	const current = "name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check what changed.\n"

	tests := []struct {
		name  string
		input strategystore.SaveInput
		// wantErr is checked with errors.Is.
		wantErr error
		// wantInvalid is the reason problem and the body problem paths of
		// an *InvalidError.
		wantInvalid *invalid
		wantVersion int
		wantCreated bool
		wantAction  string
	}{
		{
			name:        "a new strategy",
			input:       strategystore.SaveInput{Actor: sam, Name: "disk-full", Body: body("disk-full", "A disk is full."), Reason: "New strategy"},
			wantVersion: 1, wantCreated: true, wantAction: strategystore.ActionCreated,
		},
		{
			name:        "a new version of a strategy",
			input:       strategystore.SaveInput{Actor: sam, Name: "checkout-latency", Body: body("checkout-latency", "Checkout is very slow."), Reason: "Clearer description"},
			wantVersion: 3, wantAction: strategystore.ActionUpdated,
		},
		{
			name: "a new version when the caller read the current one",
			input: strategystore.SaveInput{
				Actor: sam, Name: "checkout-latency", Body: body("checkout-latency", "Checkout is very slow."), Reason: "Clearer description", BaseVersion: new(2),
			},
			wantVersion: 3, wantAction: strategystore.ActionUpdated,
		},
		{
			name: "a new strategy when the caller read no version",
			input: strategystore.SaveInput{
				Actor: sam, Name: "disk-full", Body: body("disk-full", "A disk is full."), Reason: "New strategy", BaseVersion: new(0),
			},
			wantVersion: 1, wantCreated: true, wantAction: strategystore.ActionCreated,
		},
		{
			name:        "a save by a token has no user name",
			input:       strategystore.SaveInput{Actor: strategystore.Actor{Kind: audit.ActorToken, ID: uuid.NewString()}, Name: "disk-full", Body: body("disk-full", "A disk is full."), Reason: "From CI"},
			wantVersion: 1, wantCreated: true, wantAction: strategystore.ActionCreated,
		},
		{
			name:        "an actor that is not a UUID has no author",
			input:       strategystore.SaveInput{Actor: strategystore.Actor{Kind: audit.ActorUser, ID: "U01SLACK"}, Name: "disk-full", Body: body("disk-full", "A disk is full."), Reason: "From Slack"},
			wantVersion: 1, wantCreated: true, wantAction: strategystore.ActionCreated,
		},
		{
			name:        "no reason",
			input:       strategystore.SaveInput{Actor: sam, Name: "disk-full", Body: body("disk-full", "A disk is full.")},
			wantInvalid: &invalid{reason: true},
		},
		{
			name:        "a reason of spaces",
			input:       strategystore.SaveInput{Actor: sam, Name: "disk-full", Body: body("disk-full", "A disk is full."), Reason: " \n\t "},
			wantInvalid: &invalid{reason: true},
		},
		{
			name:        "a reason that is too long",
			input:       strategystore.SaveInput{Actor: sam, Name: "disk-full", Body: body("disk-full", "A disk is full."), Reason: longReason()},
			wantInvalid: &invalid{reason: true},
		},
		{
			name:        "a body that fails the checks, and no reason",
			input:       strategystore.SaveInput{Actor: sam, Name: "disk-full", Body: "name: Disk Full\n"},
			wantInvalid: &invalid{reason: true, body: []string{"name", "description"}},
		},
		{
			name:        "a body that is not YAML",
			input:       strategystore.SaveInput{Actor: sam, Name: "disk-full", Body: "name: [\n", Reason: "Broken"},
			wantInvalid: &invalid{body: []string{""}},
		},
		{
			name:        "a body with another name",
			input:       strategystore.SaveInput{Actor: sam, Name: "disk-full", Body: body("disk-empty", "A disk is empty."), Reason: "Wrong name"},
			wantInvalid: &invalid{body: []string{"name"}},
		},
		{
			name:    "a stale base version",
			input:   strategystore.SaveInput{Actor: sam, Name: "checkout-latency", Body: body("checkout-latency", "Other."), Reason: "Late", BaseVersion: new(1)},
			wantErr: strategystore.ErrStale,
		},
		{
			name:    "a base version for a strategy that does not exist",
			input:   strategystore.SaveInput{Actor: sam, Name: "disk-full", Body: body("disk-full", "A disk is full."), Reason: "Late", BaseVersion: new(3)},
			wantErr: strategystore.ErrStale,
		},
		{
			name:    "the body of the current version",
			input:   strategystore.SaveInput{Actor: sam, Name: "checkout-latency", Body: current, Reason: "Again"},
			wantErr: strategystore.ErrUnchanged,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool, store := setup(t)
			before := auditRows(t, pool)

			saved, err := store.Save(t.Context(), tt.input)

			switch {
			case tt.wantInvalid != nil:
				var got *strategystore.InvalidError

				require.ErrorAs(t, err, &got)
				require.Equal(t, tt.wantInvalid.reason, got.Reason != "")
				require.Len(t, got.Body, len(tt.wantInvalid.body))

				for i, path := range tt.wantInvalid.body {
					require.Equal(t, path, got.Body[i].Path)
				}
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
				require.Equal(t, tt.wantCreated, saved.Created)
				require.Equal(t, tt.wantVersion, saved.Version.Number)
				require.Equal(t, "runtime:"+itoa(tt.wantVersion), saved.Strategy.Version)

				stored, err := store.Version(t.Context(), tt.input.Name, tt.wantVersion)
				require.NoError(t, err)
				require.Equal(t, tt.input.Body, stored.Body)
				require.Equal(t, tt.input.Reason, stored.Reason)

				wantAuthor := authorOf(tt.input.Actor.ID)
				require.Equal(t, wantAuthor, stored.Author)

				rows := auditRows(t, pool)
				require.Len(t, rows, len(before)+1)
				last := rows[len(rows)-1]
				require.Equal(t, tt.input.Actor.ID, last.Actor)
				require.Equal(t, tt.wantAction, last.Action)
				require.Equal(t, "strategy:"+tt.input.Name, last.Target)
				require.Contains(t, last.Details, `"reason": "`+tt.input.Reason+`"`)

				return
			}

			require.Equal(t, before, auditRows(t, pool), "a refused save writes no audit entry")

			versions, err := store.Versions(t.Context(), tt.input.Name, nil, 10)
			require.NoError(t, err)

			wantCount := 0
			if tt.input.Name == "checkout-latency" {
				wantCount = 2
			}

			require.Len(t, versions, wantCount, "a refused save adds no version")
		})
	}
}

func TestSaveAtTheSameMoment(t *testing.T) {
	t.Parallel()

	const saves = 8

	t.Run("one strategy", func(t *testing.T) {
		t.Parallel()

		_, store := setup(t)

		errs := saveAll(t, store, saves, "checkout-latency")

		for _, err := range errs {
			require.NoError(t, err)
		}

		versions, err := store.Versions(t.Context(), "checkout-latency", nil, 50)
		require.NoError(t, err)
		require.Len(t, versions, 2+saves)
		require.Equal(t, 2+saves, versions[0].Number, "version numbers have no gaps")
	})

	t.Run("a new strategy", func(t *testing.T) {
		t.Parallel()

		_, store := setup(t)

		errs := saveAll(t, store, saves, "disk-full")

		succeeded := 0

		for _, err := range errs {
			if err == nil {
				succeeded++

				continue
			}

			require.ErrorIs(t, err, strategystore.ErrConflict)
		}

		require.NotZero(t, succeeded)

		versions, err := store.Versions(t.Context(), "disk-full", nil, 50)
		require.NoError(t, err)
		require.Len(t, versions, succeeded)
	})
}

func TestReads(t *testing.T) {
	t.Parallel()

	t.Run("current versions with their bodies and author names", func(t *testing.T) {
		t.Parallel()

		_, store := setup(t)

		current, err := store.Current(t.Context())
		require.NoError(t, err)
		require.Len(t, current, 1)
		require.Equal(t, "checkout-latency", current[0].Name)
		require.Equal(t, 2, current[0].Number)
		require.Equal(t, "Sam", current[0].AuthorName)
		require.Contains(t, current[0].Body, "Check what changed.")
		require.Equal(t, "Add the first check", current[0].Reason)
	})

	t.Run("versions newest first, a page at a time, without bodies", func(t *testing.T) {
		t.Parallel()

		_, store := setup(t)

		page, err := store.Versions(t.Context(), "checkout-latency", nil, 1)
		require.NoError(t, err)
		require.Len(t, page, 1)
		require.Equal(t, 2, page[0].Number)
		require.Empty(t, page[0].Body)
		require.Equal(t, "Sam", page[0].AuthorName)

		page, err = store.Versions(t.Context(), "checkout-latency", new(2), 5)
		require.NoError(t, err)
		require.Len(t, page, 1)
		require.Equal(t, 1, page[0].Number)
	})

	t.Run("a strategy that is not stored has no versions", func(t *testing.T) {
		t.Parallel()

		_, store := setup(t)

		page, err := store.Versions(t.Context(), "missing", nil, 5)
		require.NoError(t, err)
		require.Empty(t, page)
	})

	t.Run("one version", func(t *testing.T) {
		t.Parallel()

		_, store := setup(t)

		v, err := store.Version(t.Context(), "checkout-latency", 1)
		require.NoError(t, err)
		require.Equal(t, "First draft", v.Reason)
		require.Equal(t, body("checkout-latency", "Checkout is slow."), v.Body)

		_, err = store.Version(t.Context(), "checkout-latency", 3)
		require.ErrorIs(t, err, strategystore.ErrNotFound)
	})
}

// invalid says what an *InvalidError is expected to hold.
type invalid struct {
	reason bool
	// body lists the path of each problem in the body.
	body []string
}

// saveAll saves n different bodies of the strategy name at the same time
// and returns the error of each save.
func saveAll(t *testing.T, store *strategystore.Store, n int, name string) []error {
	t.Helper()

	var (
		wg   sync.WaitGroup
		errs = make([]error, n)
	)

	for i := range n {
		wg.Go(func() {
			_, errs[i] = store.Save(t.Context(), strategystore.SaveInput{
				Actor: sam, Name: name, Body: body(name, "Description "+itoa(i)), Reason: "Save " + itoa(i),
			})
		})
	}

	wg.Wait()

	return errs
}

// longReason returns a reason over the limit.
func longReason() string {
	reason := make([]byte, strategystore.MaxReasonLength+1)
	for i := range reason {
		reason[i] = 'x'
	}

	return string(reason)
}

// authorOf returns the UUID in id, or nil when it is not one.
func authorOf(id string) *uuid.UUID {
	u, err := uuid.Parse(id)
	if err != nil {
		return nil
	}

	return &u
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
