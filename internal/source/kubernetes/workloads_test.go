package kubernetes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestGetWorkload(t *testing.T) {
	t.Parallel()

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "prod", Name: "checkout", Generation: 8, CreationTimestamp: at(48 * time.Hour),
			Annotations: map[string]string{revisionAnnotation: "7"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(int32(3)),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
			Template: podTemplate("app=ghcr.io/acme/checkout:1.4.2"),
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 8, ReadyReplicas: 2, UpdatedReplicas: 3, AvailableReplicas: 2, UnavailableReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable",
				Message: "Deployment does not have minimum availability.", LastTransitionTime: at(5 * time.Minute),
			}},
		},
	}
	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "db"},
		Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(3)), Template: podTemplate("postgres=postgres:16")},
		Status:     appsv1.StatefulSetStatus{ReadyReplicas: 3, AvailableReplicas: 2, UpdatedReplicas: 3, UpdateRevision: "db-5d8"},
	}
	daemonSet := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "agent"},
		Spec:       appsv1.DaemonSetSpec{Template: podTemplate("agent=agent:2")},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 5, NumberReady: 4, UpdatedNumberScheduled: 5, NumberAvailable: 4, NumberUnavailable: 1},
	}
	autoscaler := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "checkout"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: kindDeployment, Name: "checkout"},
			MinReplicas:    ptr(int32(2)),
			MaxReplicas:    10,
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 3, DesiredReplicas: 3},
	}

	objects := []runtime.Object{deployment, statefulSet, daemonSet, autoscaler}

	tests := []struct {
		name         string
		cfg          Config
		reactors     []reactor
		input        string
		wantReplicas replicaCounts
		wantText     []string
		wantEvidence source.Evidence
		wantTool     []string
		wantErr      error
	}{
		{
			name:         "a deployment with its autoscaler",
			input:        `{"namespace":"prod","kind":"Deployment","name":"checkout"}`,
			wantReplicas: replicaCounts{Desired: 3, Ready: 2, Updated: 3, Available: 2, Unavailable: 1},
			wantText: []string{
				"Deployment prod/checkout, created 2026-10-24T12:00:00Z",
				"Replicas: 3 desired, 2 ready, 3 updated, 2 available, 1 unavailable",
				"Revision: 7",
				"  app: ghcr.io/acme/checkout:1.4.2",
				"  Available=False MinimumReplicasUnavailable: Deployment does not have minimum availability. (since 2026-10-26T11:55:00Z)",
				"Autoscaler checkout: 2 to 10 replicas, 3 now, 3 desired",
			},
			wantEvidence: source.Evidence{Kind: source.EvidenceResource, Title: "kubectl describe deployment/checkout -n prod"},
		},
		{
			name:         "a statefulset",
			input:        `{"namespace":"prod","kind":"StatefulSet","name":"db"}`,
			wantReplicas: replicaCounts{Desired: 3, Ready: 3, Updated: 3, Available: 2, Unavailable: 1},
			wantText:     []string{"StatefulSet prod/db", "Revision: db-5d8", "postgres: postgres:16"},
			wantEvidence: source.Evidence{Kind: source.EvidenceResource, Title: "kubectl describe statefulset/db -n prod"},
		},
		{
			name:         "a daemonset with a console link",
			cfg:          Config{ConsoleURL: testConsoleURL},
			input:        `{"namespace":"prod","kind":"DaemonSet","name":"agent"}`,
			wantReplicas: replicaCounts{Desired: 5, Ready: 4, Updated: 5, Available: 4, Unavailable: 1},
			wantText:     []string{"DaemonSet prod/agent"},
			wantEvidence: source.Evidence{Kind: source.EvidenceResource, Title: "DaemonSet prod/agent", URL: "https://console.example.com/daemonsets/prod/agent"},
		},
		{
			name:         "autoscalers that cannot be read",
			reactors:     []reactor{failWith("list", "horizontalpodautoscalers", forbidden("autoscaling", "horizontalpodautoscalers"))},
			input:        `{"namespace":"prod","kind":"Deployment","name":"checkout"}`,
			wantReplicas: replicaCounts{Desired: 3, Ready: 2, Updated: 3, Available: 2, Unavailable: 1},
			wantText:     []string{"Autoscaler: unknown, Sidekick's account may not list horizontalpodautoscalers in namespace prod"},
			wantEvidence: source.Evidence{Kind: source.EvidenceResource, Title: "kubectl describe deployment/checkout -n prod"},
		},
		{
			name:     "a workload that does not exist",
			input:    `{"namespace":"prod","kind":"Deployment","name":"missing"}`,
			wantTool: []string{`deployments.apps "missing" not found`},
		},
		{name: "an unknown kind", input: `{"namespace":"prod","kind":"Job","name":"x"}`, wantTool: []string{`kind: "Job" is not one of`}},
		{
			name:     "a namespace the config does not list",
			cfg:      Config{Namespaces: []string{"staging"}},
			input:    `{"namespace":"prod","kind":"Deployment","name":"checkout"}`,
			wantTool: []string{`namespace "prod" is not one Sidekick may read`},
		},
		{
			name:     "a missing permission",
			reactors: []reactor{failWith("get", "statefulsets", forbidden("apps", "statefulsets"))},
			input:    `{"namespace":"prod","kind":"StatefulSet","name":"db"}`,
			wantTool: []string{`the "get" verb on "statefulsets" in the apps API group`},
		},
		{
			name:     "an API server that cannot be reached",
			reactors: []reactor{failWith("get", "deployments", errConnection)},
			input:    `{"namespace":"prod","kind":"Deployment","name":"checkout"}`,
			wantErr:  errConnection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, tt.cfg, objects, tt.reactors...)

			res, err := call(t, s, toolGetWorkload, tt.input)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)

				return
			case tt.wantTool != nil:
				requireToolError(t, err, tt.wantTool...)

				return
			}

			require.NoError(t, err)
			require.False(t, res.NoData)
			require.Equal(t, []source.Evidence{tt.wantEvidence}, res.Evidence)

			var view workloadView
			require.NoError(t, json.Unmarshal(res.Structured, &view))
			require.Equal(t, tt.wantReplicas, view.Replicas)

			for _, want := range tt.wantText {
				require.Contains(t, res.Text, want)
			}
		})
	}
}

// podTemplate returns a pod template with one container per name=image
// pair.
func podTemplate(containers ...string) corev1.PodTemplateSpec {
	var spec corev1.PodSpec

	for _, c := range containers {
		name, image, _ := strings.Cut(c, "=")
		spec.Containers = append(spec.Containers, corev1.Container{Name: name, Image: image})
	}

	return corev1.PodTemplateSpec{Spec: spec}
}
