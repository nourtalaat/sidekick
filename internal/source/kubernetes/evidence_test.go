package kubernetes

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestLinkerEvidence(t *testing.T) {
	t.Parallel()

	pod := object{Kind: kindPod, Resource: "pods", Namespace: "prod", Name: "checkout-1"}
	node := object{Kind: kindNode, Resource: "nodes", Name: "node a"}

	many := make([]object, 0, maxLinks+2)
	for i := range maxLinks + 2 {
		many = append(many, object{Kind: kindPod, Resource: "pods", Namespace: "prod", Name: fmt.Sprintf("p%d", i)})
	}

	tests := []struct {
		name    string
		console string
		objects []object
		want    []source.Evidence
		wantLen int
	}{
		{
			name:    "the command without a console",
			objects: []object{pod},
			want:    []source.Evidence{{Kind: source.EvidenceResource, Title: "kubectl get pods"}},
		},
		{
			name:    "the command when there is no object to link",
			console: testConsoleURL,
			want:    []source.Evidence{{Kind: source.EvidenceResource, Title: "kubectl get pods"}},
		},
		{
			name:    "a console link per object, escaped",
			console: testConsoleURL,
			objects: []object{pod, node},
			want: []source.Evidence{
				{Kind: source.EvidenceResource, Title: "Pod prod/checkout-1", URL: "https://console.example.com/pods/prod/checkout-1"},
				{Kind: source.EvidenceResource, Title: "Node node a", URL: "https://console.example.com/nodes//node%20a"},
			},
		},
		{name: "at most maxLinks links", console: testConsoleURL, objects: many, wantLen: maxLinks},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var l linker

			if tt.console != "" {
				tmpl, err := parseConsoleURL(tt.console)
				require.NoError(t, err)

				l.console = tmpl
			}

			got := l.evidence(source.EvidenceResource, tt.objects, "kubectl get pods")
			if tt.wantLen > 0 {
				require.Len(t, got, tt.wantLen)

				return
			}

			require.Equal(t, tt.want, got)
		})
	}
}

func TestKubectl(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "plain arguments", args: []string{"get", "pods", "-n", "prod"}, want: "kubectl get pods -n prod"},
		{name: "every namespace", args: append([]string{"get", "events"}, namespaceArgs("")...), want: "kubectl get events -A"},
		{name: "a selector with spaces", args: []string{"get", "pods", "-l", "tier in (web,api)"}, want: "kubectl get pods -l 'tier in (web,api)'"},
		{name: "a quote", args: []string{"logs", "it's"}, want: `kubectl logs 'it'\''s'`},
		{name: "empty arguments are left out", args: []string{"get", "", "nodes"}, want: "kubectl get nodes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, kubectl(tt.args...))
		})
	}
}

func TestResourceOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind string
		want string
	}{
		{kind: "Deployment", want: "deployments"},
		{kind: "Ingress", want: "ingresses"},
		{kind: "NetworkPolicy", want: "networkpolicies"},
		{kind: "Endpoints", want: "endpoints"},
		{kind: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, resourceOf(tt.kind))
		})
	}
}
