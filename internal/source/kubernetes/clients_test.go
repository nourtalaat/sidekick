package kubernetes

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// kubeconfigRef is the Secret key of the kubeconfig in the tests.
var kubeconfigRef = registry.SecretRef{Name: "other-cluster", Key: "kubeconfig"}

func TestKubeconfigClients(t *testing.T) {
	t.Parallel()

	first := kubeconfigFile("https://first.example.com:6443")
	second := kubeconfigFile("https://second.example.com:6443")

	tests := []struct {
		name string
		// reads are the Secret's contents on each call, in order.
		reads   [][]byte
		context string
		// wantSame says, for each call after the first, whether it returns
		// the same client as the call before.
		wantSame []bool
		wantHost string
		wantErr  string
	}{
		{name: "one client while the file stays the same", reads: [][]byte{first, first, first}, wantSame: []bool{true, true}, wantHost: "https://first.example.com:6443"},
		{name: "a new client when the file changes", reads: [][]byte{first, second}, wantSame: []bool{false}, wantHost: "https://second.example.com:6443"},
		{name: "a named context", reads: [][]byte{first}, context: "other", wantHost: "https://other.example.com:6443"},
		{name: "a context the file does not have", reads: [][]byte{first}, context: "missing", wantErr: `context "missing" does not exist`},
		{name: "a file that is not a kubeconfig", reads: [][]byte{[]byte("{not yaml")}, wantErr: "kubeconfig secret other-cluster/kubeconfig: parse the kubeconfig:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			secrets := registry.NewMockSecrets(ctrl)

			calls := make([]any, 0, len(tt.reads))
			for _, raw := range tt.reads {
				calls = append(calls, secrets.EXPECT().Resolve(gomock.Any(), kubeconfigRef).Return(raw, nil))
			}

			gomock.InOrder(calls...)

			get, err := newClients(Config{KubeconfigSecret: &kubeconfigRef, Context: tt.context}, registry.Deps{Secrets: secrets})
			require.NoError(t, err)

			var previous kubernetes.Interface

			for i := range tt.reads {
				client, err := get(t.Context())
				if tt.wantErr != "" {
					require.ErrorContains(t, err, tt.wantErr)

					return
				}

				require.NoError(t, err)

				if i > 0 {
					require.Equal(t, tt.wantSame[i-1], client == previous, "call %d", i)
				}

				previous = client
			}

			clientset, ok := previous.(*kubernetes.Clientset)
			require.True(t, ok)
			require.Contains(t, clientset.CoreV1().RESTClient().Get().URL().String(), tt.wantHost)
		})
	}
}

func TestKubeconfigSecretError(t *testing.T) {
	t.Parallel()

	errRead := errors.New("open /var/run/secrets/sidekick/other-cluster/kubeconfig: no such file or directory")

	tests := []struct {
		name string
		err  error
	}{
		{name: "a secret that cannot be read", err: errRead},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			secrets := registry.NewMockSecrets(ctrl)
			secrets.EXPECT().Resolve(gomock.Any(), kubeconfigRef).Return(nil, tt.err)

			get, err := newClients(Config{KubeconfigSecret: &kubeconfigRef}, registry.Deps{Secrets: secrets})
			require.NoError(t, err)

			_, err = get(t.Context())
			require.ErrorIs(t, err, tt.err)
			require.ErrorContains(t, err, "read the kubeconfig secret other-cluster/kubeconfig")
		})
	}
}

// kubeconfigFile returns a kubeconfig whose current context points at
// server, with a second context called other.
func kubeconfigFile(server string) []byte {
	return fmt.Appendf(nil, `apiVersion: v1
kind: Config
current-context: main
clusters:
  - name: main
    cluster: { server: %q }
  - name: other
    cluster: { server: "https://other.example.com:6443" }
users:
  - name: reader
    user: { token: not-a-real-token }
contexts:
  - name: main
    context: { cluster: main, user: reader }
  - name: other
    context: { cluster: other, user: reader }
`, server)
}
