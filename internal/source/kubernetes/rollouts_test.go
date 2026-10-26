package kubernetes

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestGetRolloutHistory(t *testing.T) {
	t.Parallel()

	const uid = types.UID("checkout-uid")

	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout"}}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "checkout", UID: uid, Annotations: map[string]string{revisionAnnotation: "3"}},
		Spec:       appsv1.DeploymentSpec{Selector: selector, Template: podTemplate("app=checkout:1.4.2")},
	}
	empty := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "new", UID: "new-uid"},
		Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "new"}}},
	}

	objects := []runtime.Object{
		deployment, empty,
		replicaSet("checkout-a", uid, 1, 72*time.Hour, "", "app=checkout:1.4.0"),
		replicaSet("checkout-b", uid, 2, 24*time.Hour, "", "app=checkout:1.4.1", "proxy=envoy:1.30"),
		replicaSet("checkout-c", uid, 3, 10*time.Minute, "lower the DB pool to 5", "app=checkout:1.4.1", "proxy=envoy:1.30"),
		replicaSet("other", "other-uid", 9, time.Minute, "", "app=checkout:9"),
	}

	tests := []struct {
		name          string
		cfg           Config
		reactors      []reactor
		input         string
		wantRevisions []int64
		wantChanges   [][]string
		wantText      []string
		wantEvidence  source.Evidence
		wantNoData    bool
		wantTool      []string
		wantErr       error
	}{
		{
			name:          "newest first with what changed",
			input:         `{"namespace":"prod","name":"checkout"}`,
			wantRevisions: []int64{3, 2, 1},
			wantChanges: [][]string{
				{"same images; another part of the pod template changed"},
				{"app: checkout:1.4.0 -> checkout:1.4.1", "proxy: added with envoy:1.30"},
				nil,
			},
			wantText: []string{
				"Rollout history of Deployment prod/checkout, current revision 3:",
				"Revision 3, ReplicaSet checkout-c, created 2026-10-26T11:50:00Z, 1/2 ready, current",
				"  Change cause: lower the DB pool to 5",
				"  Images: app=checkout:1.4.1, proxy=envoy:1.30",
				"  Changed: app: checkout:1.4.0 -> checkout:1.4.1",
			},
			wantEvidence: source.Evidence{Kind: source.EvidenceDeploy, Title: "kubectl rollout history deployment/checkout -n prod"},
		},
		{
			name:          "a limit and a console link",
			cfg:           Config{ConsoleURL: testConsoleURL},
			input:         `{"namespace":"prod","name":"checkout","limit":1}`,
			wantRevisions: []int64{3},
			wantChanges:   [][]string{{"same images; another part of the pod template changed"}},
			wantText:      []string{"current revision 3, newest 1 of 3 revisions:"},
			wantEvidence:  source.Evidence{Kind: source.EvidenceDeploy, Title: "Deployment prod/checkout", URL: "https://console.example.com/deployments/prod/checkout"},
		},
		{
			name:         "a deployment without revisions",
			input:        `{"namespace":"prod","name":"new"}`,
			wantNoData:   true,
			wantEvidence: source.Evidence{Kind: source.EvidenceDeploy, Title: "kubectl rollout history deployment/new -n prod"},
		},
		{name: "a deployment that does not exist", input: `{"namespace":"prod","name":"missing"}`, wantTool: []string{`deployments.apps "missing" not found`}},
		{
			name:     "replicasets that cannot be read",
			reactors: []reactor{failWith("list", "replicasets", forbidden("apps", "replicasets"))},
			input:    `{"namespace":"prod","name":"checkout"}`,
			wantTool: []string{`the "list" verb on "replicasets" in the apps API group`},
		},
		{
			name:     "an API server that cannot be reached",
			reactors: []reactor{failWith("list", "replicasets", errConnection)},
			input:    `{"namespace":"prod","name":"checkout"}`,
			wantErr:  errConnection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, tt.cfg, objects, tt.reactors...)

			res, err := call(t, s, toolGetRolloutHistory, tt.input)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)

				return
			case tt.wantTool != nil:
				requireToolError(t, err, tt.wantTool...)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantNoData, res.NoData)
			require.Equal(t, []source.Evidence{tt.wantEvidence}, res.Evidence)

			if tt.wantNoData {
				return
			}

			var out rolloutOutput
			require.NoError(t, json.Unmarshal(res.Structured, &out))

			revisions := make([]int64, 0, len(out.Revisions))
			changed := make([][]string, 0, len(out.Revisions))

			for _, r := range out.Revisions {
				revisions = append(revisions, r.Revision)
				changed = append(changed, r.Changes)
			}

			require.Equal(t, tt.wantRevisions, revisions)
			require.Equal(t, tt.wantChanges, changed)

			for _, want := range tt.wantText {
				require.Contains(t, res.Text, want)
			}
		})
	}
}

func TestChanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		older, newer []containerView
		want         []string
	}{
		{
			name:  "a container removed",
			older: []containerView{{Name: "app", Image: "a:1"}, {Name: "proxy", Image: "envoy"}},
			newer: []containerView{{Name: "app", Image: "a:1"}},
			want:  []string{"proxy: removed"},
		},
		{
			name:  "an image changed",
			older: []containerView{{Name: "app", Image: "a:1"}},
			newer: []containerView{{Name: "app", Image: "a:2"}},
			want:  []string{"app: a:1 -> a:2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, changes(revisionView{Containers: tt.older}, revisionView{Containers: tt.newer}))
		})
	}
}

// replicaSet returns a ReplicaSet of the Deployment with the UID, labelled
// app=checkout, with 2 replicas of which 1 is ready.
func replicaSet(name string, owner types.UID, revision int, ago time.Duration, cause string, containers ...string) *appsv1.ReplicaSet {
	annotations := map[string]string{revisionAnnotation: strconv.Itoa(revision)}
	if cause != "" {
		annotations[changeCauseAnnotation] = cause
	}

	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "prod", Name: name, Labels: map[string]string{"app": "checkout"}, Annotations: annotations, CreationTimestamp: at(ago),
			OwnerReferences: []metav1.OwnerReference{{Kind: kindDeployment, Name: "checkout", UID: owner, Controller: ptr(true)}},
		},
		Spec:   appsv1.ReplicaSetSpec{Template: podTemplate(containers...)},
		Status: appsv1.ReplicaSetStatus{Replicas: 2, ReadyReplicas: 1},
	}
}
