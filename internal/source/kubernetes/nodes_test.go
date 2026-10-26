package kubernetes

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestListNodes(t *testing.T) {
	t.Parallel()

	healthy := node("node-a", corev1.ConditionTrue)
	healthy.Labels = map[string]string{roleLabelPrefix + "control-plane": "", "kubernetes.io/os": "linux"}
	pressure := node("node-b", corev1.ConditionTrue, corev1.NodeCondition{
		Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue, Message: "kubelet has insufficient memory available",
	})
	notReady := node("node-c", corev1.ConditionFalse)
	notReady.Spec.Unschedulable = true
	notReady.Spec.Taints = []corev1.Taint{{Key: "node.kubernetes.io/unreachable", Effect: corev1.TaintEffectNoExecute}}
	silent := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-d"}}

	objects := []runtime.Object{healthy, pressure, notReady, silent}

	tests := []struct {
		name         string
		cfg          Config
		reactors     []reactor
		objects      []runtime.Object
		input        string
		wantNodes    []string
		wantText     []string
		wantEvidence []source.Evidence
		wantNoData   bool
		wantTool     []string
		wantErr      error
	}{
		{
			name:      "every node, problems first",
			input:     `{}`,
			wantNodes: []string{"node-c", "node-b", "node-d", "node-a"},
			wantText: []string{
				"4 matching nodes (4 nodes checked), those with problems first:",
				"node-a Ready=True, roles control-plane, kubelet v1.34.1, allocatable cpu 4, memory 16Gi, pods 110, created 2026-10-16T12:00:00Z",
				"  Taints: node.kubernetes.io/unreachable:NoExecute",
				"  Problem: Ready=False: kubelet stopped posting node status",
				"  Problem: cordoned: new pods are not scheduled here",
				"  Problem: MemoryPressure=True: kubelet has insufficient memory available",
				"  Problem: Ready: not reported",
			},
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "kubectl get nodes -o wide"}},
		},
		{
			name:         "problems only",
			input:        `{"problemsOnly":true,"limit":2}`,
			wantNodes:    []string{"node-c", "node-b"},
			wantText:     []string{"2 of 3 matching nodes (4 nodes checked)"},
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "kubectl get nodes -o wide"}},
		},
		{
			name:      "one node with a console link",
			cfg:       Config{ConsoleURL: "https://console.example.com/{{.Resource}}/{{.Name}}"},
			input:     `{"name":"node-a"}`,
			wantNodes: []string{"node-a"},
			wantEvidence: []source.Evidence{
				{Kind: source.EvidenceResource, Title: "Node node-a", URL: "https://console.example.com/nodes/node-a"},
			},
		},
		{
			name:         "no problems",
			objects:      []runtime.Object{healthy},
			input:        `{"problemsOnly":true}`,
			wantNoData:   true,
			wantText:     []string{"No node has a problem (1 node checked)."},
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "kubectl get nodes -o wide"}},
		},
		{
			name:         "no nodes",
			objects:      []runtime.Object{},
			input:        `{}`,
			wantNoData:   true,
			wantText:     []string{"The cluster lists no nodes."},
			wantEvidence: []source.Evidence{{Kind: source.EvidenceResource, Title: "kubectl get nodes -o wide"}},
		},
		{name: "a node that does not exist", input: `{"name":"node-z"}`, wantTool: []string{`nodes "node-z" not found`}},
		{
			name:     "a missing permission",
			reactors: []reactor{failWith("list", "nodes", forbidden("", "nodes"))},
			input:    `{}`,
			wantTool: []string{"Sidekick's account may not list nodes: its Role or ClusterRole needs", `"nodes" in the core API group`},
		},
		{name: "an API server that cannot be reached", reactors: []reactor{failWith("list", "nodes", errConnection)}, input: `{}`, wantErr: errConnection},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objs := objects
			if tt.objects != nil {
				objs = tt.objects
			}

			s := newTestSource(t, tt.cfg, objs, tt.reactors...)

			res, err := call(t, s, toolListNodes, tt.input)

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
			require.Equal(t, tt.wantEvidence, res.Evidence)

			for _, want := range tt.wantText {
				require.Contains(t, res.Text, want)
			}

			if tt.wantNoData {
				return
			}

			var out nodesOutput
			require.NoError(t, json.Unmarshal(res.Structured, &out))

			names := make([]string, 0, len(out.Nodes))
			for _, n := range out.Nodes {
				names = append(names, n.Name)
			}

			require.Equal(t, tt.wantNodes, names)
		})
	}
}

// node returns a node whose Ready condition has the status, with the other
// conditions, created ten days before testNow.
func node(name string, ready corev1.ConditionStatus, conditions ...corev1.NodeCondition) *corev1.Node {
	readyCondition := corev1.NodeCondition{Type: corev1.NodeReady, Status: ready, LastTransitionTime: at(time.Hour)}
	if ready != corev1.ConditionTrue {
		readyCondition.Message = "kubelet stopped posting node status"
	}

	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: at(10 * 24 * time.Hour)},
		Status: corev1.NodeStatus{
			Conditions: append([]corev1.NodeCondition{readyCondition}, conditions...),
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: "v1.34.1"},
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			},
		},
	}
}
