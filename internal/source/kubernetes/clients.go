package kubernetes

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Client-side rate limits of the API client. client-go's defaults (5 and
// 10) would make a tool that reads several namespaces wait.
const (
	clientQPS   = 20
	clientBurst = 40
)

// errNoSecrets is returned when kubeconfigSecret is set but the source is
// built without a way to read secrets.
var errNoSecrets = errors.New("no secrets resolver in deps")

// clientFunc returns the client for one call.
type clientFunc func(ctx context.Context) (kubernetes.Interface, error)

// newClients returns how the source gets its client: one client for the
// pod's service account, or a client built from the kubeconfig Secret.
func newClients(cfg Config, deps registry.Deps) (clientFunc, error) {
	if cfg.KubeconfigSecret == nil {
		rc, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("use the pod's service account (set kubeconfigSecret to read another cluster): %w", err)
		}

		client, err := newClientset(rc)
		if err != nil {
			return nil, fmt.Errorf("service account client: %w", err)
		}

		return func(context.Context) (kubernetes.Interface, error) { return client, nil }, nil
	}

	if deps.Secrets == nil {
		return nil, errNoSecrets
	}

	logger := cmp.Or(deps.Logger, slog.Default())
	k := &kubeconfigClients{secrets: deps.Secrets, ref: *cfg.KubeconfigSecret, context: cfg.Context, logger: logger}

	return k.get, nil
}

// kubeconfigClients reads the kubeconfig Secret on each call and builds a
// new client only when the file changed.
type kubeconfigClients struct {
	secrets registry.Secrets
	ref     registry.SecretRef
	context string
	logger  *slog.Logger

	mu     sync.Mutex
	sum    [sha256.Size]byte
	client kubernetes.Interface
}

// get returns the client for the current content of the Secret.
func (k *kubeconfigClients) get(ctx context.Context) (kubernetes.Interface, error) {
	raw, err := k.secrets.Resolve(ctx, k.ref)
	if err != nil {
		return nil, fmt.Errorf("read the kubeconfig secret %s/%s: %w", k.ref.Name, k.ref.Key, err)
	}

	sum := sha256.Sum256(raw)

	k.mu.Lock()
	defer k.mu.Unlock()

	if k.client != nil && sum == k.sum {
		return k.client, nil
	}

	rc, err := restConfig(raw, k.context)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig secret %s/%s: %w", k.ref.Name, k.ref.Key, err)
	}

	if rc.Insecure {
		k.logger.Warn("the kubeconfig turns off TLS verification of the API server, so anyone on the network path can read and change Sidekick's requests",
			"secret", k.ref.Name, "key", k.ref.Key, "context", k.context)
	}

	client, err := newClientset(rc)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig secret %s/%s: %w", k.ref.Name, k.ref.Key, err)
	}

	k.sum, k.client = sum, client

	return client, nil
}

// restConfig reads a kubeconfig file and returns the client config of the
// named context, or of the file's current context when name is empty. It
// refuses a file whose credentials would make Sidekick run a command or
// read another file in its pod.
func restConfig(raw []byte, name string) (*rest.Config, error) {
	file, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("parse the kubeconfig: %w", err)
	}

	if err := checkKubeconfig(file); err != nil {
		return nil, fmt.Errorf("check the kubeconfig: %w", err)
	}

	overrides := &clientcmd.ConfigOverrides{CurrentContext: name}

	rc, err := clientcmd.NewNonInteractiveClientConfig(*file, name, overrides, nil).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("use the kubeconfig: %w", err)
	}

	return rc, nil
}

// checkKubeconfig allows only credentials written into the file: a token,
// a client certificate and key, a username and password, and CA data. An
// exec credential plugin or an auth provider would run code in Sidekick's
// pod, and a path would make Sidekick read another file in it.
func checkKubeconfig(file *clientcmdapi.Config) error {
	var errs []error

	for _, name := range slices.Sorted(maps.Keys(file.AuthInfos)) {
		user := file.AuthInfos[name]

		if user.Exec != nil {
			errs = append(errs, fmt.Errorf("user %s uses an exec credential plugin, which Sidekick does not run; use a token or a client certificate", name))
		}

		if user.AuthProvider != nil {
			errs = append(errs, fmt.Errorf("user %s uses the auth provider %q, which Sidekick does not run; use a token or a client certificate", name, user.AuthProvider.Name))
		}

		paths := []struct{ key, value, inline string }{
			{key: "tokenFile", value: user.TokenFile, inline: "token"},
			{key: "client-certificate", value: user.ClientCertificate, inline: "client-certificate-data"},
			{key: "client-key", value: user.ClientKey, inline: "client-key-data"},
		}

		for _, p := range paths {
			if p.value != "" {
				errs = append(errs, fmt.Errorf("user %s: %s names a file, which Sidekick does not read; use %s", name, p.key, p.inline))
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(file.Clusters)) {
		if file.Clusters[name].CertificateAuthority != "" {
			errs = append(errs, fmt.Errorf("cluster %s: certificate-authority names a file, which Sidekick does not read; use certificate-authority-data", name))
		}
	}

	return errors.Join(errs...)
}

// newClientset builds a client with Sidekick's user agent and rate limits.
func newClientset(rc *rest.Config) (kubernetes.Interface, error) {
	rc = rest.CopyConfig(rc)
	rc.UserAgent = "sidekick/" + buildinfo.Version
	rc.QPS = clientQPS
	rc.Burst = clientBurst

	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("build the client: %w", err)
	}

	return client, nil
}
