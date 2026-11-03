package kubernetesrollback

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

const strategicMergeType = "application/strategic-merge-patch+json"

// params returns the parameters of a proposal. back 0 leaves revisionsBack out.
func params(kind, name string, back int) json.RawMessage {
	p := map[string]any{"kind": kind, "namespace": testNamespace, "name": name}
	if back > 0 {
		p["revisionsBack"] = back
	}

	raw, _ := json.Marshal(p)

	return raw
}

// invocation returns the invocation that runs a proposal.
func invocation(p json.RawMessage, snap action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: "proposal-1", Params: p, IdempotencyKey: "key-1", ApprovedBy: "user-1", Snapshot: &snap}
}

// proposed makes a proposal of a rollback of the Deployment, with the fake
// API and an action that allows going back back revisions.
func proposed(t *testing.T, kind, name string, back int) (*fakeAPI, *Rollback, json.RawMessage, action.Snapshot) {
	t.Helper()

	api, f := newFakeAPI(t)
	a := testRollback(t, api, func(c *Config) { c.MaxRevisionsBack = ptr(max(back, 1)) })
	p := params(kind, name, back)

	snap, err := a.Snapshot(t.Context(), p)
	require.NoError(t, err)

	return f, a, p, snap
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(c *Config)
		params  string
		wantErr error
	}{
		{name: "a Deployment", params: `{"kind":"Deployment","namespace":"shop","name":"checkout"}`},
		{name: "a StatefulSet", params: `{"kind":"StatefulSet","namespace":"shop","name":"ledger"}`},
		{name: "one revision back", params: `{"kind":"Deployment","namespace":"shop","name":"checkout","revisionsBack":1}`},
		{name: "a kind outside limits.kinds", change: func(c *Config) { c.Limits.Kinds = []string{"Deployment"} },
			params: `{"kind":"StatefulSet","namespace":"shop","name":"ledger"}`, wantErr: action.ErrLimit},
		{name: "a kind that is not one", params: `{"kind":"Job","namespace":"shop","name":"checkout"}`, wantErr: action.ErrLimit},
		{name: "a namespace outside the limits", params: `{"kind":"Deployment","namespace":"other","name":"checkout"}`, wantErr: action.ErrLimit},
		{name: "a name outside the limits", params: `{"kind":"Deployment","namespace":"shop","name":"cart"}`, wantErr: action.ErrLimit},
		{name: "zero revisions back", params: `{"kind":"Deployment","namespace":"shop","name":"checkout","revisionsBack":0}`, wantErr: action.ErrParams},
		{name: "a negative number of revisions", params: `{"kind":"Deployment","namespace":"shop","name":"checkout","revisionsBack":-1}`, wantErr: action.ErrParams},
		{name: "two back with a limit of one", params: `{"kind":"Deployment","namespace":"shop","name":"checkout","revisionsBack":2}`, wantErr: action.ErrLimit},
		{name: "two back with a limit of two", change: func(c *Config) { c.MaxRevisionsBack = ptr(2) },
			params: `{"kind":"Deployment","namespace":"shop","name":"checkout","revisionsBack":2}`},
		{name: "three back with a limit of two", change: func(c *Config) { c.MaxRevisionsBack = ptr(2) },
			params: `{"kind":"Deployment","namespace":"shop","name":"checkout","revisionsBack":3}`, wantErr: action.ErrLimit},
		{name: "a key that is not a parameter", params: `{"kind":"Deployment","namespace":"shop","name":"checkout","force":true}`, wantErr: action.ErrParams},
		{name: "no JSON", params: `[]`, wantErr: action.ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, _ := newFakeAPI(t)
			a := testRollback(t, api, tt.change)

			err := a.Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
			}

			require.Empty(t, api.Requests(), "Validate makes no call")
		})
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	api, _ := newFakeAPI(t)
	spec := testRollback(t, api, func(c *Config) { c.Limits.Kinds = []string{"Deployment"} }).Spec()

	require.Equal(t, "rollback", spec.Name)
	require.Contains(t, spec.Description, "Allowed namespaces: shop.")
	require.Contains(t, spec.Description, "Allowed kinds: Deployment.")
	require.Contains(t, spec.Description, "Allowed names: checkout, ledger, agent.")
	require.Contains(t, spec.Description, "at most 1)")
	require.JSONEq(t, string(paramsSchema), string(spec.ParamsSchema))
}

func TestNewFailsOutsideACluster(t *testing.T) {
	t.Parallel()

	_, err := New(t.Context(), "rollback", validConfig(), registry.Deps{})
	require.ErrorContains(t, err, "service account")
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		kind, object string
		back         int
		change       func(f *fakeAPI)
		wantKind     string
		wantRunning  string
		wantBack     string
		wantVersion  string
		wantBackVer  string
		wantBackBy   string
	}{
		{
			name: "a Deployment one back", kind: "Deployment", object: deploymentName, back: 1, wantKind: "ReplicaSet revision",
			wantRunning: "checkout-7:7", wantBack: "checkout-6:6", wantVersion: "shop/checkout:1.4.7", wantBackVer: "shop/checkout:1.4.6",
		},
		{
			name: "a Deployment two back", kind: "Deployment", object: deploymentName, back: 2, wantKind: "ReplicaSet revision",
			wantRunning: "checkout-7:7", wantBack: "checkout-5:5", wantVersion: "shop/checkout:1.4.7", wantBackVer: "shop/checkout:1.4.5",
		},
		{
			name: "a StatefulSet one back", kind: "StatefulSet", object: statefulSetName, back: 1, wantKind: "ControllerRevision",
			wantRunning: "ledger-3:3", wantBack: "ledger-2:2", wantVersion: "shop/ledger:1.4.7", wantBackVer: "shop/ledger:1.4.6",
		},
		{
			name: "a DaemonSet two back", kind: "DaemonSet", object: daemonSetName, back: 2, wantKind: "ControllerRevision",
			wantRunning: "agent-3:3", wantBack: "agent-1:1", wantVersion: "shop/agent:1.4.7", wantBackVer: "shop/agent:1.4.5",
		},
		{
			name: "default of one back", kind: "Deployment", object: deploymentName, back: 0, wantKind: "ReplicaSet revision",
			wantRunning: "checkout-7:7", wantBack: "checkout-6:6", wantVersion: "shop/checkout:1.4.7", wantBackVer: "shop/checkout:1.4.6",
		},
		{
			name: "a Deployment with revisions that are not in order of creation", kind: "Deployment", object: deploymentName, back: 1,
			change: func(f *fakeAPI) {
				f.replicaSets[0].CreationTimestamp.Time = f.replicaSets[2].CreationTimestamp.Add(time.Hour)
			},
			wantKind: "ReplicaSet revision", wantRunning: "checkout-7:7", wantBack: "checkout-6:6",
			wantVersion: "shop/checkout:1.4.7", wantBackVer: "shop/checkout:1.4.6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeAPI(t)
			if tt.change != nil {
				tt.change(f)
			}

			a := testRollback(t, api, func(c *Config) { c.MaxRevisionsBack = ptr(2) })

			snap, err := a.Snapshot(t.Context(), params(tt.kind, tt.object, tt.back))
			require.NoError(t, err)

			require.True(t, snap.Revision)
			require.Equal(t, "kubernetes:"+strings.ToLower(tt.kind)+"/shop/"+tt.object, snap.Target)
			require.Equal(t, tt.wantRunning, snap.Version)

			revs, err := snap.Revisions()
			require.NoError(t, err)
			require.Equal(t, tt.wantKind, revs.Running.Kind)
			require.Equal(t, tt.wantKind, revs.GoesBackTo.Kind)
			require.Equal(t, tt.wantRunning, revs.Running.ID)
			require.Equal(t, tt.wantBack, revs.GoesBackTo.ID)
			require.Equal(t, tt.wantVersion, revs.Running.Version)
			require.Equal(t, tt.wantBackVer, revs.GoesBackTo.Version)
			require.Empty(t, f.writes(), "a snapshot changes nothing")
			require.Equal(t, 1, f.mintedCount(), "one token for the snapshot")
		})
	}
}

func TestSnapshotShowsWhoDeployedAndWhen(t *testing.T) {
	t.Parallel()

	api, f := newFakeAPI(t)
	a := testRollback(t, api, nil)

	snap, err := a.Snapshot(t.Context(), params("Deployment", deploymentName, 1))
	require.NoError(t, err)

	revs, err := snap.Revisions()
	require.NoError(t, err)

	created := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	require.Equal(t, &created, revs.Running.DeployedAt)
	require.Equal(t, "kubectl-client-side-apply at 2026-11-03T10:15:00Z", revs.Running.DeployedBy,
		"the newest manager of spec.template, not the status writer")
	require.Empty(t, revs.Running.Notes)

	earlier := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	require.Equal(t, &earlier, revs.GoesBackTo.DeployedAt)
	require.Empty(t, revs.GoesBackTo.DeployedBy, "the cluster keeps no record of who made an earlier revision")
	require.Equal(t, "change cause: Bump the cart client", revs.GoesBackTo.Notes)

	f.edit(func() { f.deployment.ManagedFields = nil })

	snap, err = a.Snapshot(t.Context(), params("Deployment", deploymentName, 1))
	require.NoError(t, err)

	revs, err = snap.Revisions()
	require.NoError(t, err)
	require.Empty(t, revs.Running.DeployedBy, "unknown when the managed fields say nothing")
}

func TestSnapshotShowsTheImagesOfEveryContainer(t *testing.T) {
	t.Parallel()

	api, f := newFakeAPI(t)
	a := testRollback(t, api, nil)

	f.edit(func() {
		for _, template := range []*corev1.PodTemplateSpec{&f.deployment.Spec.Template, &f.replicaSets[2].Spec.Template} {
			template.Spec.Containers = append(template.Spec.Containers, corev1.Container{Name: "sidecar", Image: "envoy:1.2"})
			template.Spec.InitContainers = []corev1.Container{{Name: "migrate", Image: "shop/migrate:1"}}
		}
	})

	snap, err := a.Snapshot(t.Context(), params("Deployment", deploymentName, 1))
	require.NoError(t, err)

	revs, err := snap.Revisions()
	require.NoError(t, err)
	require.Equal(t, "app=shop/checkout:1.4.7, sidecar=envoy:1.2", revs.Running.Version)
	require.Equal(t, "init containers: migrate=shop/migrate:1", revs.Running.Notes)
	require.Equal(t, "shop/checkout:1.4.6", revs.GoesBackTo.Version)
}

func TestSnapshotRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		kind    string
		object  string
		back    int
		change  func(f *fakeAPI)
		wantErr string
	}{
		{
			name: "a paused Deployment", kind: "Deployment", object: deploymentName, back: 1,
			change:  func(f *fakeAPI) { f.deployment.Spec.Paused = true },
			wantErr: "Deployment shop/checkout is paused, so a rollback would not roll out until someone resumes it. Do not propose a rollback; say in the report that it is paused.",
		},
		{
			name: "no earlier revision", kind: "Deployment", object: deploymentName, back: 1,
			change: func(f *fakeAPI) { f.replicaSets = f.replicaSets[2:] },
			wantErr: "Deployment shop/checkout has no earlier revision to go back to: only its current revision is recorded " +
				"(check spec.revisionHistoryLimit, or it was deployed once). Do not propose a rollback for it",
		},
		{
			name: "no earlier revision of a StatefulSet", kind: "StatefulSet", object: statefulSetName, back: 1,
			change: func(f *fakeAPI) {
				f.revisions = slices.DeleteFunc(f.revisions, func(cr *appsv1.ControllerRevision) bool {
					return cr.OwnerReferences[0].UID == "sts-uid" && cr.Revision < 3
				})
			},
			wantErr: "StatefulSet shop/ledger has no earlier revision to go back to",
		},
		{
			name: "fewer earlier revisions than asked", kind: "Deployment", object: deploymentName, back: 2,
			change:  func(f *fakeAPI) { f.replicaSets = f.replicaSets[1:] },
			wantErr: "Deployment shop/checkout has only 1 earlier revision, so it cannot go back 2. Ask for revisionsBack 1 or less",
		},
		{
			name: "more asked than the limit", kind: "Deployment", object: deploymentName, back: 3,
			wantErr: "3 revisions back is more than maxRevisionsBack allows (2)",
		},
		{
			name: "two revisions with the same template count once", kind: "Deployment", object: deploymentName, back: 2,
			change: func(f *fakeAPI) {
				f.replicaSets[0].Spec.Template = *f.replicaSets[1].Spec.Template.DeepCopy()
			},
			wantErr: "has only 1 earlier revision, so it cannot go back 2",
		},
		{
			name: "managed by Argo CD through the tracking annotation", kind: "Deployment", object: deploymentName, back: 1,
			change: func(f *fakeAPI) {
				f.deployment.Annotations["argocd.argoproj.io/tracking-id"] = "shop:apps/Deployment:shop/checkout"
			},
			wantErr: "Deployment shop/checkout is managed by Argo CD (it has the annotation argocd.argoproj.io/tracking-id), and Argo CD would undo a live " +
				"rollback at its next sync. Propose argocd.rollback if it is offered, or a pull request that reverts the change in Git.",
		},
		{
			name: "managed by Argo CD through the instance label", kind: "StatefulSet", object: statefulSetName, back: 1,
			change:  func(f *fakeAPI) { f.statefulSet.Labels["argocd.argoproj.io/instance"] = "shop" },
			wantErr: "StatefulSet shop/ledger is managed by Argo CD (it has the label argocd.argoproj.io/instance)",
		},
		{
			name: "managed by Flux kustomize-controller", kind: "DaemonSet", object: daemonSetName, back: 1,
			change: func(f *fakeAPI) { f.daemonSet.Labels["kustomize.toolkit.fluxcd.io/name"] = "apps" },
			wantErr: "DaemonSet shop/agent is managed by Flux (it has the label kustomize.toolkit.fluxcd.io/name), and Flux would undo a live " +
				"rollback at its next reconcile. Propose a pull request that reverts the change in Git.",
		},
		{
			name: "managed by Flux helm-controller", kind: "Deployment", object: deploymentName, back: 1,
			change:  func(f *fakeAPI) { f.deployment.Labels["helm.toolkit.fluxcd.io/name"] = "checkout" },
			wantErr: "Deployment shop/checkout is managed by Flux (it has the label helm.toolkit.fluxcd.io/name)",
		},
		{
			name: "a controller that has not caught up", kind: "Deployment", object: deploymentName, back: 1,
			change: func(f *fakeAPI) { f.deployment.Status.ObservedGeneration = 2 },
			wantErr: "Deployment shop/checkout has a change its controller has not processed yet (generation 3, observed 2). " +
				"Read it again in a minute before proposing a rollback",
		},
		{
			name: "a current template that no ReplicaSet has", kind: "Deployment", object: deploymentName, back: 1,
			change: func(f *fakeAPI) { f.deployment.Spec.Template.Spec.Containers[0].Image = "shop/checkout:1.4.8" },
			wantErr: "Deployment shop/checkout has no ReplicaSet for its current pod template yet, so its running revision cannot be told. " +
				"Read it again in a minute",
		},
		{
			name: "a current template that no ControllerRevision has", kind: "DaemonSet", object: daemonSetName, back: 1,
			change:  func(f *fakeAPI) { f.daemonSet.Spec.Template.Spec.Containers[0].Image = "shop/agent:9" },
			wantErr: "DaemonSet shop/agent has no ControllerRevision for its current pod template yet",
		},
		{
			name: "a workload that does not exist", kind: "Deployment", object: deploymentName, back: 1,
			change:  func(f *fakeAPI) { f.gone = true },
			wantErr: "Deployment shop/checkout does not exist",
		},
		{
			name: "a ControllerRevision with data that is not a pod template", kind: "StatefulSet", object: statefulSetName, back: 1,
			change: func(f *fakeAPI) {
				f.revisions[0].Data.Raw = []byte(`{"spec":{}}`)
			},
			wantErr: "ControllerRevision ledger-1: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeAPI(t)
			if tt.change != nil {
				tt.change(f)
			}

			a := testRollback(t, api, func(c *Config) { c.MaxRevisionsBack = ptr(2) })

			_, err := a.Snapshot(t.Context(), params(tt.kind, tt.object, tt.back))
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, f.writes())
		})
	}
}

func TestSnapshotAllowsAWorkloadWithOnlyHelmLabels(t *testing.T) {
	t.Parallel()

	api, f := newFakeAPI(t)
	f.deployment.Labels = map[string]string{
		"app.kubernetes.io/managed-by": "Helm", "app.kubernetes.io/instance": "checkout",
	}
	f.deployment.Annotations["meta.helm.sh/release-name"] = "checkout"

	_, err := testRollback(t, api, nil).Snapshot(t.Context(), params("Deployment", deploymentName, 1))
	require.NoError(t, err)
}

func TestSnapshotErrorsHoldNoToken(t *testing.T) {
	t.Parallel()

	api, f := newFakeAPI(t)
	f.tokenStatus = http.StatusForbidden

	_, err := testRollback(t, api, nil).Snapshot(t.Context(), params("Deployment", deploymentName, 1))
	require.ErrorIs(t, err, action.ErrPermanent, "the account may not ask for a token, and asking again changes nothing")
	require.NotContains(t, err.Error(), ownToken)
	require.Contains(t, err.Error(), "[redacted]")
}

func TestExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		kind        string
		object      string
		back        int
		wantImageIs string
		wantNumber  map[string]int64
		wantSummary string
	}{
		{
			name: "a Deployment one back", kind: "Deployment", object: deploymentName, back: 1,
			wantImageIs: "shop/checkout:1.4.6", wantNumber: map[string]int64{"checkout-6": 8, "checkout-7": 7, "checkout-5": 5},
			wantSummary: "Rolled back Deployment shop/checkout from checkout-7:7 to checkout-6:6.",
		},
		{
			name: "a Deployment two back", kind: "Deployment", object: deploymentName, back: 2,
			wantImageIs: "shop/checkout:1.4.5", wantNumber: map[string]int64{"checkout-5": 8, "checkout-6": 6, "checkout-7": 7},
			wantSummary: "Rolled back Deployment shop/checkout from checkout-7:7 to checkout-5:5.",
		},
		{
			name: "a StatefulSet", kind: "StatefulSet", object: statefulSetName, back: 1,
			wantImageIs: "shop/ledger:1.4.6", wantNumber: map[string]int64{"ledger-2": 4, "ledger-3": 3, "ledger-1": 1},
			wantSummary: "Rolled back StatefulSet shop/ledger from ledger-3:3 to ledger-2:2.",
		},
		{
			name: "a DaemonSet", kind: "DaemonSet", object: daemonSetName, back: 2,
			wantImageIs: "shop/agent:1.4.5", wantNumber: map[string]int64{"agent-1": 4, "agent-2": 2, "agent-3": 3},
			wantSummary: "Rolled back DaemonSet shop/agent from agent-3:3 to agent-1:1.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, a, p, snap := proposed(t, tt.kind, tt.object, tt.back)
			version := f.version()

			res, err := a.Execute(t.Context(), invocation(p, snap))
			require.NoError(t, err)
			require.Equal(t, tt.wantSummary, res.Summary)
			require.Empty(t, res.Links)

			require.Equal(t, tt.wantImageIs, f.image(tt.kind))

			for name, number := range tt.wantNumber {
				require.Equal(t, number, f.revisionOf(name), "the revision %s", name)
			}

			writes := f.writes()
			require.Len(t, writes, 1)
			require.Equal(t, strategicMergeType, writes[0].contentType)
			require.Equal(t, "sidekick", writes[0].manager)
			require.Equal(t, map[string]any{"resourceVersion": version}, writes[0].body["metadata"], "the patch holds the version that was read")

			template := writes[0].body["spec"].(map[string]any)["template"].(map[string]any)
			require.Equal(t, "replace", template["$patch"])
			require.NotContains(t, template["metadata"].(map[string]any)["labels"], "pod-template-hash")
			require.Equal(t, 2, f.mintedCount(), "one token for the snapshot and one for the run")
		})
	}
}

func TestExecuteTwiceWithTheSameInvocationChangesOnce(t *testing.T) {
	t.Parallel()

	for _, kind := range []struct{ kind, name string }{
		{"Deployment", deploymentName}, {"StatefulSet", statefulSetName}, {"DaemonSet", daemonSetName},
	} {
		t.Run(kind.kind, func(t *testing.T) {
			t.Parallel()

			f, a, p, snap := proposed(t, kind.kind, kind.name, 1)
			inv := invocation(p, snap)

			first, err := a.Execute(t.Context(), inv)
			require.NoError(t, err)

			second, err := a.Execute(t.Context(), inv)
			require.NoError(t, err)
			require.Equal(t, first.Summary, second.Summary)
			require.Len(t, f.writes(), 1, "a crash after the write and a retry make the change once")
		})
	}
}

func TestExecuteConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(f *fakeAPI)
		wantErr string
	}{
		{
			name:    "someone deployed a new image",
			change:  func(f *fakeAPI) { f.deployment.Spec.Template.Spec.Containers[0].Image = "shop/checkout:1.4.8" },
			wantErr: "Deployment shop/checkout changed since the proposal: its pod template is no longer the one of checkout-7:7",
		},
		{
			name:    "someone went to another recorded revision",
			change:  func(f *fakeAPI) { f.deployment.Spec.Template.Spec.Containers[0].Image = "shop/checkout:1.4.5" },
			wantErr: "changed since the proposal",
		},
		{
			name: "someone edited the environment",
			change: func(f *fakeAPI) {
				f.deployment.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "A", Value: "b"}}
			},
			wantErr: "changed since the proposal",
		},
		{
			name:    "the revision to go back to was removed",
			change:  func(f *fakeAPI) { f.replicaSets = slices.Delete(f.replicaSets, 1, 2) },
			wantErr: "the revision to go back to (checkout-6:6) is no longer in the history of Deployment shop/checkout",
		},
		{
			name:    "the revision to go back to has other images",
			change:  func(f *fakeAPI) { f.replicaSets[1].Spec.Template.Spec.Containers[0].Image = "shop/checkout:other" },
			wantErr: "the revision to go back to (checkout-6:6) has other images than the approver read: shop/checkout:other",
		},
		{
			name:    "someone paused it",
			change:  func(f *fakeAPI) { f.deployment.Spec.Paused = true },
			wantErr: "Deployment shop/checkout is paused",
		},
		{
			name: "Argo CD took it over",
			change: func(f *fakeAPI) {
				f.deployment.Annotations["argocd.argoproj.io/tracking-id"] = "shop:apps/Deployment:shop/checkout"
			},
			wantErr: "Deployment shop/checkout is managed by Argo CD",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, a, p, snap := proposed(t, "Deployment", deploymentName, 1)
			f.edit(func() { tt.change(f) })

			_, err := a.Execute(t.Context(), invocation(p, snap))
			require.ErrorIs(t, err, action.ErrConflict)
			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, f.writes(), "nothing is written")
		})
	}
}

func TestExecuteWhenTheWorkloadChangesBetweenTheReadAndTheWrite(t *testing.T) {
	t.Parallel()

	f, a, p, snap := proposed(t, "Deployment", deploymentName, 1)
	f.beforeWrite = f.touch

	_, err := a.Execute(t.Context(), invocation(p, snap))
	require.ErrorIs(t, err, action.ErrConflict, "the API server answers 409")
	require.Len(t, f.writes(), 1)
	require.Equal(t, "shop/checkout:1.4.7", f.image("Deployment"))
}

func TestExecuteClassifiesWriteErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		permanent bool
	}{
		{name: "forbidden", status: http.StatusForbidden, permanent: true},
		{name: "not valid", status: http.StatusUnprocessableEntity, permanent: true},
		{name: "a server error", status: http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, a, p, snap := proposed(t, "Deployment", deploymentName, 1)
			f.writeStatus = tt.status

			_, err := a.Execute(t.Context(), invocation(p, snap))
			require.Error(t, err)
			require.Equal(t, tt.permanent, isPermanent(err))
			require.NotContains(t, err.Error(), "minted-token")
		})
	}
}

func TestExecuteChecksItsInputs(t *testing.T) {
	t.Parallel()

	f, a, p, snap := proposed(t, "Deployment", deploymentName, 1)

	tests := []struct {
		name    string
		params  json.RawMessage
		snap    *action.Snapshot
		wantErr error
	}{
		{name: "no snapshot", params: p, snap: nil, wantErr: action.ErrPermanent},
		{name: "a snapshot of another workload", params: params("StatefulSet", statefulSetName, 1), snap: &snap, wantErr: action.ErrPermanent},
		{name: "a snapshot that is not of a rollback", params: p, snap: &action.Snapshot{Target: snap.Target, Before: snap.Before, After: snap.After}, wantErr: action.ErrPermanent},
		{name: "parameters outside the limits", params: params("Deployment", "cart", 1), snap: &snap, wantErr: action.ErrLimit},
		{name: "revisions that cannot be read", params: p, snap: &action.Snapshot{Target: snap.Target, Before: []byte(`{}`), After: []byte(`{}`), Revision: true}, wantErr: action.ErrPermanent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inv := action.Invocation{ProposalID: "p", Params: tt.params, IdempotencyKey: "k", Snapshot: tt.snap}

			_, err := a.Execute(t.Context(), inv)
			require.ErrorIs(t, err, tt.wantErr)
			require.Empty(t, f.writes())
		})
	}
}

func TestUndo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		kind      string
		object    string
		back      int
		wantNum   string
		wantIs    string
		wantID    string
		wantRenum int64
	}{
		{
			name: "a Deployment", kind: "Deployment", object: deploymentName, back: 1, wantID: "checkout-7:7",
			wantIs: "shop/checkout:1.4.7", wantNum: "checkout-7", wantRenum: 9,
		},
		{
			name: "a StatefulSet", kind: "StatefulSet", object: statefulSetName, back: 1, wantID: "ledger-3:3",
			wantIs: "shop/ledger:1.4.7", wantNum: "ledger-3", wantRenum: 5,
		},
		{
			name: "a DaemonSet two back", kind: "DaemonSet", object: daemonSetName, back: 2, wantID: "agent-3:3",
			wantIs: "shop/agent:1.4.7", wantNum: "agent-3", wantRenum: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, a, p, snap := proposed(t, tt.kind, tt.object, tt.back)
			inv := invocation(p, snap)

			_, err := a.Execute(t.Context(), inv)
			require.NoError(t, err)
			require.NotEqual(t, tt.wantIs, f.image(tt.kind))

			res, err := a.Undo(t.Context(), inv, snap, false)
			require.NoError(t, err)
			require.Equal(t, fmt.Sprintf("Restored %s %s/%s to %s.", tt.kind, testNamespace, tt.object, tt.wantID), res.Summary)
			require.Equal(t, tt.wantIs, f.image(tt.kind))
			require.Equal(t, tt.wantRenum, f.revisionOf(tt.wantNum), "the revision that ran before has a new number, and is found by its name")
			require.Len(t, f.writes(), 2)

			again, err := a.Undo(t.Context(), inv, snap, false)
			require.NoError(t, err, "undo is safe to repeat")
			require.Equal(t, res.Summary, again.Summary)
			require.Len(t, f.writes(), 2, "the repeat writes nothing")
		})
	}
}

func TestUndoWhenTheWorkloadChangedSince(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		image    string
		wantID   string
		wantRevs string
	}{
		{name: "a template no revision has", image: "shop/checkout:1.4.9", wantID: "unrecorded", wantRevs: "shop/checkout:1.4.9"},
		{name: "another recorded revision", image: "shop/checkout:1.4.5", wantID: "checkout-5:5", wantRevs: "shop/checkout:1.4.5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, a, p, snap := proposed(t, "Deployment", deploymentName, 1)
			inv := invocation(p, snap)

			_, err := a.Execute(t.Context(), inv)
			require.NoError(t, err)

			f.edit(func() { f.deployment.Spec.Template.Spec.Containers[0].Image = tt.image })

			_, err = a.Undo(t.Context(), inv, snap, false)
			require.ErrorIs(t, err, action.ErrConflict)

			var conflict *action.ConflictError
			require.ErrorAs(t, err, &conflict)

			live := action.Snapshot{Target: snap.Target, Before: conflict.Live, After: conflict.Live, Revision: true}

			revs, err := live.Revisions()
			require.NoError(t, err)
			require.Equal(t, tt.wantID, revs.Running.ID)
			require.Equal(t, tt.wantRevs, revs.Running.Version)
			require.Len(t, f.writes(), 1, "only the rollback wrote")

			_, err = a.Undo(t.Context(), inv, snap, true)
			require.NoError(t, err, "force restores it anyway")
			require.Equal(t, "shop/checkout:1.4.7", f.image("Deployment"))
			require.Len(t, f.writes(), 2)
		})
	}
}

func TestUndoRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(f *fakeAPI)
		force   bool
		wantErr string
	}{
		{
			name:    "the revision to restore was removed",
			change:  func(f *fakeAPI) { f.replicaSets = slices.Delete(f.replicaSets, 2, 3) },
			wantErr: "the revision to restore (checkout-7:7) is no longer in the history of Deployment shop/checkout, so it cannot be restored",
		},
		{
			name:    "the revision to restore was removed, with force",
			change:  func(f *fakeAPI) { f.replicaSets = slices.Delete(f.replicaSets, 2, 3) },
			force:   true,
			wantErr: "no longer in the history",
		},
		{
			name:    "the Deployment was paused",
			change:  func(f *fakeAPI) { f.deployment.Spec.Paused = true },
			wantErr: "is paused",
		},
		{
			name: "the Deployment is managed by Flux",
			change: func(f *fakeAPI) {
				f.deployment.Labels["helm.toolkit.fluxcd.io/name"] = "checkout"
			},
			force:   true,
			wantErr: "is managed by Flux",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, a, p, snap := proposed(t, "Deployment", deploymentName, 1)
			inv := invocation(p, snap)

			_, err := a.Execute(t.Context(), inv)
			require.NoError(t, err)

			f.edit(func() { tt.change(f) })

			_, err = a.Undo(t.Context(), inv, snap, tt.force)
			require.ErrorIs(t, err, action.ErrPermanent)
			require.ErrorContains(t, err, tt.wantErr)
			require.Len(t, f.writes(), 1)
		})
	}
}

func TestUndoChecksItsInputs(t *testing.T) {
	t.Parallel()

	_, a, p, snap := proposed(t, "Deployment", deploymentName, 1)
	inv := invocation(p, snap)

	_, err := a.Undo(t.Context(), inv, action.Snapshot{}, false)
	require.ErrorIs(t, err, action.ErrPermanent, "an undo without a snapshot")

	_, err = a.Undo(t.Context(), inv, action.Snapshot{Target: snap.Target, Before: snap.Before, After: snap.After}, false)
	require.ErrorIs(t, err, action.ErrPermanent, "a snapshot that is not of a rollback")

	_, err = a.Undo(t.Context(), action.Invocation{Params: params("Deployment", "cart", 1)}, snap, false)
	require.ErrorIs(t, err, action.ErrLimit)
}

func TestIsNotAMonitorFixer(t *testing.T) {
	t.Parallel()

	var a action.Action = &Rollback{}

	_, ok := a.(action.MonitorFixer)
	require.False(t, ok, "a rollback fixes the cause, not a noisy monitor")
}

// isPermanent reports whether an Execute error says repeating cannot help.
func isPermanent(err error) bool {
	return errors.Is(err, action.ErrPermanent)
}
