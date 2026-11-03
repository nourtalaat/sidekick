package awslambda

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

const secondAccount = "444455556666"

func TestValidate(t *testing.T) {
	t.Parallel()

	twoAccounts := func(c *Config) {
		c.Accounts = append(c.Accounts, testConfig().Accounts[0])
		c.Accounts[1].ID = secondAccount
		c.Accounts[1].RoleARN = "arn:aws:iam::" + secondAccount + ":role/sidekick-lambda-rollback"
	}

	tests := []struct {
		name      string
		edit      func(*Config)
		params    string
		wantErr   string
		wantErrIs error
	}{
		{name: "a function and alias in the limits", params: paramsBack1},
		{name: "a function that matches a pattern", params: `{"function":"cart-api","alias":"live"}`},
		{name: "the account and Region given", params: `{"account":"111122223333","region":"us-east-1","function":"checkout","alias":"live"}`},
		{name: "revisionsBack at the limit", edit: func(c *Config) { c.MaxRevisionsBack = new(3) }, params: `{"function":"checkout","alias":"live","revisionsBack":3}`},
		{
			name: "revisionsBack one over the limit", edit: func(c *Config) { c.MaxRevisionsBack = new(3) },
			params:  `{"function":"checkout","alias":"live","revisionsBack":4}`,
			wantErr: "4 revisions back is more than maxRevisionsBack allows (3)", wantErrIs: action.ErrLimit,
		},
		{
			name: "revisionsBack above the default limit", params: paramsBack2,
			wantErr: "2 revisions back is more than maxRevisionsBack allows (1)", wantErrIs: action.ErrLimit,
		},
		{
			name: "revisionsBack of zero", params: `{"function":"checkout","alias":"live","revisionsBack":0}`,
			wantErr: "0 revisions back is not a number of revisions", wantErrIs: action.ErrParams,
		},
		{
			name: "a function outside the limits", params: `{"function":"payments","alias":"live"}`,
			wantErr: `function "payments" is not one the action may change; allowed: checkout, cart-*`, wantErrIs: action.ErrLimit,
		},
		{
			name: "an alias outside the limits", params: `{"function":"checkout","alias":"prod"}`,
			wantErr: `alias "prod" is not one the action may change; allowed: live`, wantErrIs: action.ErrLimit,
		},
		{name: "the alias $LATEST", params: `{"function":"checkout","alias":"$LATEST"}`, wantErr: `alias "$LATEST" is not one`, wantErrIs: action.ErrLimit},
		{name: "a function given with a qualifier", params: `{"function":"checkout:live","alias":"live"}`, wantErr: `function "checkout:live" is not one`, wantErrIs: action.ErrLimit},
		{
			name:    "a function given by ARN",
			params:  `{"function":"arn:aws:lambda:us-east-1:999988887777:function:checkout","alias":"live"}`,
			wantErr: "is not one the action may change", wantErrIs: action.ErrLimit,
		},
		{
			name: "an account that is not listed", params: `{"account":"999988887777","function":"checkout","alias":"live"}`,
			wantErr: `account "999988887777" is not one Sidekick may change; allowed: 111122223333`, wantErrIs: action.ErrLimit,
		},
		{
			name: "the account left out with two accounts", edit: twoAccounts, params: paramsBack1,
			wantErr: "give account; allowed: 111122223333, 444455556666", wantErrIs: action.ErrParams,
		},
		{name: "a key the action does not have", params: `{"function":"checkout","alias":"live","version":"3"}`, wantErr: "version", wantErrIs: action.ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, tt.edit)

			err := r.Validate(t.Context(), json.RawMessage(tt.params))
			require.Empty(t, f.opsCalled(), "validation calls nothing")

			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, tt.wantErrIs)
		})
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	r, _ := newTestAction(t, func(c *Config) { c.MaxRevisionsBack = new(3) })

	spec := r.Spec()
	require.Equal(t, "lambda-rollback", spec.Name)
	require.Contains(t, spec.Description, "Functions: checkout, cart-*. Aliases: live.")
	require.Contains(t, spec.Description, "revisionsBack is at most 3.")
	require.Contains(t, spec.Description, "the split is cleared, and Undo restores it")
	require.JSONEq(t, `{"type":"integer","minimum":1,"maximum":3,"default":1,
		"description":"How many published versions of the function to go back from the one the alias serves. Default 1."}`,
		propertyOf(t, spec.ParamsSchema, "revisionsBack"))
}

// propertyOf returns one property of a JSON Schema.
func propertyOf(t *testing.T, schema json.RawMessage, name string) string {
	t.Helper()

	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(schema, &s))

	return string(s.Properties[name])
}

func TestNewIsApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(c *Config)
		want   bool
	}{
		{name: "without the opt-in", change: func(*Config) {}, want: true},
		{name: "with the opt-in", change: func(c *Config) { c.AllowReadWrite = true }, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, _ := newTestAction(t, tt.change)

			require.Equal(t, tt.want, action.ReadWriteBlocked(r), "the built action reports it")
		})
	}
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		edit   func(*Config)
		params string
		// want is the version it goes back to.
		want string
	}{
		{name: "one back", params: paramsBack1, want: "41"},
		{name: "two back skips a deleted version", edit: func(c *Config) { c.MaxRevisionsBack = new(3) }, params: paramsBack2, want: "40"},
		{name: "three back", edit: func(c *Config) { c.MaxRevisionsBack = new(3) }, params: `{"function":"checkout","alias":"live","revisionsBack":3}`, want: "38"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, tt.edit)

			snap, err := r.Snapshot(t.Context(), json.RawMessage(tt.params))
			require.NoError(t, err)

			require.Equal(t, "aws:lambda/111122223333/us-east-1/checkout/live", snap.Target)
			require.JSONEq(t, `{"revisionId":"rev-1"}`, snap.Version)
			require.True(t, snap.Revision)

			revs, err := snap.Revisions()
			require.NoError(t, err)
			require.Equal(t, "42", revs.Running.ID)
			require.Equal(t, tt.want, revs.GoesBackTo.ID)
			require.Equal(t, 1, f.count("GetAlias"))
			require.Equal(t, 2, f.count("GetFunctionConfiguration"))
			require.Equal(t, 3, f.count("ListVersionsByFunction"), "every page of the list is read, since it has no order")
			require.Empty(t, f.updateBodies(), "a snapshot changes nothing")
		})
	}
}

func TestSnapshotRevisions(t *testing.T) {
	t.Parallel()

	r, _ := newTestAction(t, nil)

	snap, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)

	revs, err := snap.Revisions()
	require.NoError(t, err)

	published := time.Date(2026, 5, 4, 9, 30, 0, 0, time.UTC)
	require.Equal(t, action.Revision{
		Kind: "Lambda version", ID: "42", Version: "sha256:" + hashOf(42)[:12] + " python3.12",
		DeployedAt: &published, Notes: "Description: build 42",
	}, revs.Running)
	require.Equal(t, action.Revision{
		Kind: "Lambda version", ID: "41", Version: "sha256:" + hashOf(41)[:12] + " python3.12",
		DeployedAt: &published, Notes: "Description: build 41",
	}, revs.GoesBackTo)
	require.Empty(t, revs.Running.DeployedBy, "Lambda does not record who published a version, and screens show unknown")
}

func TestSnapshotOfAnImage(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)
	f.versions[0].image = true

	snap, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)

	revs, err := snap.Revisions()
	require.NoError(t, err)
	require.Equal(t, "sha256:"+hashOf(41)[:12]+" container image", revs.GoesBackTo.Version)
}

func TestSnapshotOfASplitAlias(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)
	f.set("42", map[string]float64{"43": 0.07})

	snap, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)
	require.JSONEq(t, `{"revisionId":"rev-2","weights":{"43":0.07}}`, snap.Version, "the restore data is in the snapshot, not in the notes")

	revs, err := snap.Revisions()
	require.NoError(t, err)
	require.Equal(t, "42", revs.Running.ID, "the version the alias serves is the running one")
	require.Equal(t, "41", revs.GoesBackTo.ID)
	require.Contains(t, revs.Running.Notes, "The alias also sends 7% of requests to version 43. The rollback clears this split")
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		edit    func(*Config)
		fake    func(*fakeLambda)
		params  string
		wantErr string
		// wantNoCalls is true when nothing is sent to Lambda.
		wantNoCalls bool
	}{
		{
			name: "more versions back than the limit", params: paramsBack2, wantNoCalls: true,
			wantErr: "2 revisions back is more than maxRevisionsBack allows (1)",
		},
		{
			name: "a function outside the limits", params: `{"function":"payments","alias":"live"}`, wantNoCalls: true,
			wantErr: `function "payments" is not one the action may change`,
		},
		{
			name: "an alias outside the limits", params: `{"function":"checkout","alias":"prod"}`, wantNoCalls: true,
			wantErr: `alias "prod" is not one the action may change`,
		},
		{
			name: "an alias that does not exist", params: paramsBack1, fake: func(f *fakeLambda) { f.noAlias = true },
			wantErr: "Lambda function checkout in account 111122223333, Region us-east-1 has no alias live",
		},
		{
			name: "an alias on $LATEST", params: paramsBack1, fake: func(f *fakeLambda) { f.set("$LATEST", nil) },
			wantErr: `alias live of function checkout serves "$LATEST", which is not a published version`,
		},
		{
			name: "no earlier version", params: paramsBack1, fake: func(f *fakeLambda) { f.set("36", nil) },
			wantErr: "function checkout has no published version below 36, so there is nothing to go back to",
		},
		{
			name: "fewer earlier versions than asked", edit: func(c *Config) { c.MaxRevisionsBack = new(3) }, fake: func(f *fakeLambda) { f.set("38", nil) },
			params:  `{"function":"checkout","alias":"live","revisionsBack":3}`,
			wantErr: "function checkout has only 2 published version(s) below 38, so alias live cannot go 3 back",
		},
		{
			name: "a target that cannot be read", params: paramsBack1, fake: func(f *fakeLambda) { f.versions[0].hidden = true },
			wantErr: "version 41 of Lambda function checkout does not exist, so the alias cannot be put on it",
		},
		{
			name: "a function with more versions than are read", params: paramsBack1, fake: func(f *fakeLambda) {
				for n := 100; n < 100+maxListPages*versionsPerPage; n++ {
					f.versions = append(f.versions, fakeVersion{number: n})
				}
			},
			wantErr: "function checkout has more than 1000 versions, and Sidekick does not read them all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, tt.edit)
			if tt.fake != nil {
				tt.fake(f)
			}

			_, err := r.Snapshot(t.Context(), json.RawMessage(tt.params))
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit, "the model is shown the reason as a refusal")

			if tt.wantNoCalls {
				require.Empty(t, f.opsCalled())

				return
			}

			require.ErrorIs(t, err, action.ErrPermanent)
		})
	}
}

func TestSnapshotErrorsOfLambda(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		code          string
		wantPermanent bool
	}{
		{name: "a policy that refuses the call", status: 403, code: "AccessDeniedException", wantPermanent: true},
		{name: "a parameter Lambda rejects", status: 400, code: "InvalidParameterValueException", wantPermanent: true},
		{name: "a throttle", status: 429, code: "TooManyRequestsException"},
		{name: "a failure of the service", status: 500, code: "ServiceException"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, nil)
			f.failWith("GetAlias", tt.status, tt.code)

			_, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, errorsIsPermanent(err))
			require.NotErrorIs(t, err, action.ErrLimit, "an error that is not a refusal is not shown as one")
		})
	}
}
