package awsecs

import (
	"encoding/json"
	"strconv"
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
		c.Accounts[1].RoleARN = "arn:aws:iam::" + secondAccount + ":role/sidekick-ecs-rollback"
	}

	tests := []struct {
		name      string
		edit      func(*Config)
		params    string
		wantErr   string
		wantErrIs error
	}{
		{name: "a service and cluster in the limits", params: paramsBack1},
		{name: "a service that matches a pattern", params: `{"cluster":"shop","service":"cart-api"}`},
		{name: "the account and Region given", params: `{"account":"111122223333","region":"us-east-1","cluster":"shop","service":"checkout"}`},
		{name: "revisionsBack at the limit", edit: func(c *Config) { c.MaxRevisionsBack = new(3) }, params: `{"cluster":"shop","service":"checkout","revisionsBack":3}`},
		{
			name: "revisionsBack one over the limit", edit: func(c *Config) { c.MaxRevisionsBack = new(3) },
			params:  `{"cluster":"shop","service":"checkout","revisionsBack":4}`,
			wantErr: "4 revisions back is more than maxRevisionsBack allows (3)", wantErrIs: action.ErrLimit,
		},
		{
			name: "revisionsBack above the default limit", params: paramsBack2,
			wantErr: "2 revisions back is more than maxRevisionsBack allows (1)", wantErrIs: action.ErrLimit,
		},
		{
			name: "revisionsBack of zero", params: `{"cluster":"shop","service":"checkout","revisionsBack":0}`,
			wantErr: "0 revisions back is not a number of revisions", wantErrIs: action.ErrParams,
		},
		{
			name: "a cluster outside the limits", params: `{"cluster":"prod","service":"checkout"}`,
			wantErr: `cluster "prod" is not one the action may change; allowed: shop`, wantErrIs: action.ErrLimit,
		},
		{
			name: "a service outside the limits", params: `{"cluster":"shop","service":"payments"}`,
			wantErr: `service "payments" is not one the action may change; allowed: checkout, cart-*`, wantErrIs: action.ErrLimit,
		},
		{name: "a service that is a path", params: `{"cluster":"shop","service":"../x"}`, wantErr: `service "../x" is not one`, wantErrIs: action.ErrLimit},
		{name: "a cluster given by ARN", params: `{"cluster":"arn:aws:ecs:us-east-1:999988887777:cluster/shop","service":"checkout"}`, wantErr: "is not one the action may change", wantErrIs: action.ErrLimit},
		{
			name: "an account that is not listed", params: `{"account":"999988887777","cluster":"shop","service":"checkout"}`,
			wantErr: `account "999988887777" is not one Sidekick may change; allowed: 111122223333`, wantErrIs: action.ErrLimit,
		},
		{
			name: "the account left out with two accounts", edit: twoAccounts, params: paramsBack1,
			wantErr: "give account; allowed: 111122223333, 444455556666", wantErrIs: action.ErrParams,
		},
		{name: "a key the action does not have", params: `{"cluster":"shop","service":"checkout","desiredCount":0}`, wantErr: "desiredCount", wantErrIs: action.ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, tt.edit)

			err := r.Validate(t.Context(), json.RawMessage(tt.params))
			require.Zero(t, f.count("DescribeServices"), "validation calls nothing")

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
	require.Equal(t, "ecs-rollback", spec.Name)
	require.Contains(t, spec.Description, "Clusters: shop. Services: checkout, cart-*.")
	require.Contains(t, spec.Description, "revisionsBack is at most 3.")
	require.Contains(t, spec.Description, "may never have been deployed")
	require.JSONEq(t, `{"type":"integer","minimum":1,"maximum":3,"default":1,
		"description":"How many ACTIVE task definition revisions of the service's family to go back from the one it runs. Default 1."}`,
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

func TestSnapshot(t *testing.T) {
	t.Parallel()

	registered := time.Date(2026, 5, 4, 9, 30, 0, 0, time.UTC)
	revision := func(n int) action.Revision {
		return action.Revision{
			Kind: "task definition", ID: "checkout:" + strconv.Itoa(n), Version: imageFor(n),
			DeployedAt: &registered, DeployedBy: "registered by arn:aws:iam::111122223333:user/ana", Notes: registeredNote,
		}
	}

	tests := []struct {
		name   string
		edit   func(*Config)
		fake   func(*fakeECS)
		params string
		// want is the revision it goes back to.
		want int
	}{
		{name: "one back", params: paramsBack1, want: 41},
		{name: "two back skips an INACTIVE revision and other families", edit: func(c *Config) { c.MaxRevisionsBack = new(3) }, params: paramsBack2, want: 40},
		{name: "three back", edit: func(c *Config) { c.MaxRevisionsBack = new(3) }, params: `{"cluster":"shop","service":"checkout","revisionsBack":3}`, want: 38},
		{
			name: "a service with the ECS controller named", params: paramsBack1, want: 41,
			fake: func(f *fakeECS) { f.service.controller = "ECS" },
		},
		{
			name: "from the middle of the family", params: paramsBack1, want: 36,
			fake: func(f *fakeECS) { f.service.primary = "checkout:37" },
		},
		{
			name: "a family whose prefix is shared by many others", params: paramsBack1, want: 41,
			fake: func(f *fakeECS) { addFamily(f, "checkout-zzz", 130) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, tt.edit)
			if tt.fake != nil {
				tt.fake(f)
			}

			snap, err := r.Snapshot(t.Context(), json.RawMessage(tt.params))
			require.NoError(t, err)

			running := f.service.primary
			require.Equal(t, "aws:ecs/111122223333/us-east-1/shop/checkout", snap.Target)
			require.Equal(t, "ecs-svc/100 "+running, snap.Version)
			require.True(t, snap.Revision)

			revs, err := snap.Revisions()
			require.NoError(t, err)
			require.Equal(t, running, revs.Running.ID)
			require.Equal(t, revision(tt.want), revs.GoesBackTo)
			require.Equal(t, "task definition", revs.Running.Kind)
			require.Equal(t, 1, f.count("DescribeServices"))
			require.Equal(t, 2, f.count("DescribeTaskDefinition"))
		})
	}
}

func TestSnapshotRevisions(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)

	snap, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)

	revs, err := snap.Revisions()
	require.NoError(t, err)

	registered := time.Date(2026, 5, 4, 9, 30, 0, 0, time.UTC)
	require.Equal(t, action.Revision{
		Kind: "task definition", ID: "checkout:42", Version: imageFor(42), DeployedAt: &registered,
		DeployedBy: "registered by arn:aws:iam::111122223333:user/ana", Notes: registeredNote,
	}, revs.Running)

	require.Len(t, f.lists, 1)
	require.JSONEq(t, `{"familyPrefix":"checkout","status":"ACTIVE","sort":"DESC","maxResults":100}`, string(f.lists[0]))
}

func TestSnapshotUnknownFacts(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)

	for i := range f.definitions {
		f.definitions[i].noRegistrant = true
		f.definitions[i].images = []string{"a:1", "b:2"}
	}

	snap, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)

	revs, err := snap.Revisions()
	require.NoError(t, err)
	require.Empty(t, revs.Running.DeployedBy, "who registered it is left out, and screens show unknown")
	require.Equal(t, "a:1, b:2", revs.Running.Version)
}

func TestSnapshotPagesTheList(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)
	addFamily(f, "checkout-zzz", 130)

	_, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)
	require.Len(t, f.lists, 2)
	require.JSONEq(t, `{"familyPrefix":"checkout","status":"ACTIVE","sort":"DESC","maxResults":100,"nextToken":"100"}`, string(f.lists[1]))
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		edit    func(*Config)
		fake    func(*fakeECS)
		params  string
		wantErr string
		// wantNoCalls is true when nothing is sent to ECS.
		wantNoCalls bool
	}{
		{
			name: "more revisions back than the limit", params: paramsBack2, wantNoCalls: true,
			wantErr: "2 revisions back is more than maxRevisionsBack allows (1)",
		},
		{
			name: "a CodeDeploy service", params: paramsBack1, fake: func(f *fakeECS) { f.service.controller = "CODE_DEPLOY" },
			wantErr: "service checkout uses the CODE_DEPLOY deployment controller; this action rolls back only services that use the ECS deployment controller",
		},
		{
			name: "a service with an external controller", params: paramsBack1, fake: func(f *fakeECS) { f.service.controller = "EXTERNAL" },
			wantErr: "service checkout uses the EXTERNAL deployment controller",
		},
		{
			name: "a service with no primary deployment", params: paramsBack1, fake: func(f *fakeECS) { f.service.primary = "" },
			wantErr: "service checkout has no PRIMARY deployment, so Sidekick cannot tell which task definition it runs",
		},
		{
			name: "a service that is draining", params: paramsBack1, fake: func(f *fakeECS) { f.service.status = "DRAINING" },
			wantErr: "service checkout is DRAINING, not ACTIVE, so ECS cannot update it",
		},
		{
			name: "a service that does not exist", params: paramsBack1, fake: func(f *fakeECS) { f.noService = true },
			wantErr: "ECS cluster shop in account 111122223333, Region us-east-1 has no service checkout",
		},
		{
			name: "a cluster that does not exist", params: paramsBack1, fake: func(f *fakeECS) { f.noCluster = true },
			wantErr: "ECS cluster shop does not exist in account 111122223333, Region us-east-1",
		},
		{
			name: "no earlier revision", params: paramsBack1, fake: func(f *fakeECS) { f.service.primary = "checkout:36" },
			wantErr: "task definition family checkout has no ACTIVE revision below checkout:36, so there is nothing to go back to",
		},
		{
			name: "fewer earlier revisions than asked", edit: func(c *Config) { c.MaxRevisionsBack = new(3) }, fake: func(f *fakeECS) { f.service.primary = "checkout:38" },
			params:  `{"cluster":"shop","service":"checkout","revisionsBack":3}`,
			wantErr: "task definition family checkout has only 2 ACTIVE revisions below checkout:38, so the service cannot go 3 back",
		},
		{
			name: "one earlier revision when two are asked", edit: func(c *Config) { c.MaxRevisionsBack = new(2) }, fake: func(f *fakeECS) { f.service.primary = "checkout:37" },
			params:  paramsBack2,
			wantErr: "task definition family checkout has only 1 ACTIVE revision below checkout:37, so the service cannot go 2 back",
		},
		{
			name: "a target that is not ACTIVE when described", params: paramsBack1, fake: func(f *fakeECS) { f.describeStatus["checkout:41"] = "INACTIVE" },
			wantErr: "task definition checkout:41 is INACTIVE, so the service cannot be put on it",
		},
		{
			name: "a target ECS cannot describe", params: paramsBack1, fake: func(f *fakeECS) { f.definitions[2].hidden = true },
			wantErr: "task definition checkout:41 cannot be read from ECS, so the service cannot be put on it",
		},
		{
			name: "a family so crowded that the search is cut", params: paramsBack1, fake: func(f *fakeECS) { addFamily(f, "checkout-zzz", 1100) },
			wantErr: `looked through the newest 1000 task definitions that start with "checkout" and did not find 1 ACTIVE revision of family checkout below checkout:42; ` +
				"ask for fewer revisions back, or roll back in the ECS console",
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

			if !tt.wantNoCalls {
				require.ErrorIs(t, err, action.ErrPermanent)
			}

			if tt.wantNoCalls {
				require.Empty(t, f.ops)
			}
		})
	}
}

// addFamily adds n ACTIVE revisions of a family that starts with the prefix
// of checkout, and sorts before it in descending order.
func addFamily(f *fakeECS, family string, n int) {
	for rev := 1; rev <= n; rev++ {
		f.definitions = append(f.definitions, fakeTaskDefinition{family: family, revision: rev, status: "ACTIVE", images: []string{"registry.example.com/z:" + strconv.Itoa(rev)}})
	}
}
