package kubernetes

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

func TestKubeconfigCredentials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		user    string
		cluster string
		want    []string
	}{
		{name: "an inline token", user: "{ token: not-a-real-token }"},
		{name: "inline client certificate and key", user: "{ client-certificate-data: Y2VydA==, client-key-data: a2V5 }", cluster: "certificate-authority-data: Y2E="},
		{name: "a username and password", user: "{ username: reader, password: not-a-real-password }"},
		{name: "a cluster that skips TLS verification", user: "{ token: not-a-real-token }", cluster: "insecure-skip-tls-verify: true"},
		{
			name: "an exec credential plugin",
			user: "{ exec: { apiVersion: client.authentication.k8s.io/v1, command: aws, args: [eks, get-token] } }",
			want: []string{"user reader uses an exec credential plugin, which Sidekick does not run; use a token or a client certificate"},
		},
		{
			name: "an auth provider",
			user: "{ auth-provider: { name: oidc } }",
			want: []string{`user reader uses the auth provider "oidc", which Sidekick does not run; use a token or a client certificate`},
		},
		{
			name: "a token file",
			user: "{ tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token }",
			want: []string{"user reader: tokenFile names a file, which Sidekick does not read; use token"},
		},
		{
			name: "client certificate and key files",
			user: "{ client-certificate: /etc/tls/tls.crt, client-key: /etc/tls/tls.key }",
			want: []string{
				"user reader: client-certificate names a file, which Sidekick does not read; use client-certificate-data",
				"user reader: client-key names a file, which Sidekick does not read; use client-key-data",
			},
		},
		{
			name:    "a CA file",
			user:    "{ token: not-a-real-token }",
			cluster: "certificate-authority: /etc/tls/ca.crt",
			want:    []string{"cluster main: certificate-authority names a file, which Sidekick does not read; use certificate-authority-data"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := restConfig(credentialsFile(tt.user, tt.cluster), "")
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, "check the kubeconfig: ")

			for _, want := range tt.want {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestInsecureKubeconfigWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cluster  string
		wantWarn bool
	}{
		{name: "TLS verification on", wantWarn: false},
		{name: "TLS verification off", cluster: "insecure-skip-tls-verify: true", wantWarn: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			secrets := registry.NewMockSecrets(ctrl)
			secrets.EXPECT().Resolve(gomock.Any(), kubeconfigRef).Return(credentialsFile("{ token: not-a-real-token }", tt.cluster), nil)

			var logs bytes.Buffer

			logger := slog.New(slog.NewTextHandler(&logs, nil))

			get, err := newClients(Config{KubeconfigSecret: &kubeconfigRef}, registry.Deps{Secrets: secrets, Logger: logger})
			require.NoError(t, err)

			_, err = get(t.Context())
			require.NoError(t, err)
			require.Equal(t, tt.wantWarn, strings.Contains(logs.String(), "turns off TLS verification"))
			require.NotContains(t, logs.String(), "not-a-real-token")
		})
	}
}

// credentialsFile returns a kubeconfig with one cluster, main, with the
// extra cluster keys, and one user, reader, with the user's keys.
func credentialsFile(user, cluster string) []byte {
	if cluster != "" {
		cluster = ", " + cluster
	}

	return fmt.Appendf(nil, `apiVersion: v1
kind: Config
current-context: main
clusters:
  - name: main
    cluster: { server: "https://main.example.com:6443"%s }
users:
  - name: reader
    user: %s
contexts:
  - name: main
    context: { cluster: main, user: reader }
`, cluster, user)
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
