package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestSamplePod(t *testing.T) {
	t.Parallel()

	system := pod("kube-system", "coredns-1", corev1.PodRunning, nil)
	pending := pod("prod", "worker-pending", corev1.PodPending, nil)
	done := pod("prod", "job-done", corev1.PodSucceeded, nil)
	api := pod("prod", "api-1", corev1.PodRunning, nil)
	web := pod("staging", "web-1", corev1.PodRunning, nil)

	tests := []struct {
		name      string
		cfg       Config
		objects   []runtime.Object
		reactors  []reactor
		wantNS    string
		wantName  string
		wantFound bool
		wantErr   string
	}{
		{name: "a running pod", objects: []runtime.Object{pending, api}, wantNS: "prod", wantName: "api-1", wantFound: true},
		{name: "the system namespaces and pods that are not running are skipped", objects: []runtime.Object{system, pending, done, web}, wantNS: "staging", wantName: "web-1", wantFound: true},
		{name: "the configured namespaces only", cfg: Config{Namespaces: []string{"staging"}}, objects: []runtime.Object{api, web}, wantNS: "staging", wantName: "web-1", wantFound: true},
		{name: "no running pod", objects: []runtime.Object{system, pending, done}},
		{name: "no pod at all"},
		{
			name:     "RBAC refuses the list",
			objects:  []runtime.Object{api},
			reactors: []reactor{failWith("list", "pods", forbidden("", "pods"))},
			wantErr:  `the "list" verb on "pods"`,
		},
		{
			name:     "the API server cannot be reached",
			objects:  []runtime.Object{api},
			reactors: []reactor{failWith("list", "pods", errConnection)},
			wantErr:  "connection refused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, tt.cfg, tt.objects, tt.reactors...)

			ns, name, found, err := s.SamplePod(t.Context())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantFound, found)
			require.Equal(t, tt.wantNS, ns)
			require.Equal(t, tt.wantName, name)
		})
	}
}
