package helmrollback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	helmaction "helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/kube"
	"helm.sh/helm/v4/pkg/storage/driver"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/nourtalaat/sidekick/internal/action/kubeaccess"
)

// opener opens Helm for one operation in a namespace. The action holds one
// so that a test can open Helm against a fake cluster.
type opener func(ctx context.Context, namespace string) (*session, error)

// session is Helm, acting with the token of the actions' service account, for
// the calls of one Snapshot, Execute or Undo.
type session struct {
	cfg *helmaction.Configuration
	// secrets are the tokens in use, which no error text may carry.
	secrets []string
}

// rollbackOptions are what a rollback needs besides the release.
type rollbackOptions struct {
	// target is the number of the revision to go back to.
	target int
	// timeout is how long Helm waits for the hooks, and for the objects when
	// wait is true.
	timeout time.Duration
	// wait says whether Helm waits until the objects are ready.
	wait bool
	// description is kept with the new revision.
	description string
}

// clusterOpener returns an opener that asks the cluster for a token of the
// actions' service account and builds Helm's configuration with it, for the
// namespace of the call and the place Helm keeps its records.
func clusterOpener(cluster *kubeaccess.Cluster, driverName string, logger *slog.Logger) opener {
	return func(ctx context.Context, namespace string) (*session, error) {
		access, err := cluster.Open(ctx)
		if err != nil {
			return nil, fmt.Errorf("get access: %w", err)
		}

		cfg := helmaction.NewConfiguration(helmaction.ConfigurationSetLogger(warningsOnly(logger)))

		if err := cfg.Init(&restGetter{config: access.Config, namespace: namespace}, namespace, driverName); err != nil {
			return nil, kubeaccess.Redact(fmt.Errorf("prepare Helm: %w", err), access.Secrets)
		}

		return &session{cfg: cfg, secrets: access.Secrets}, nil
	}
}

// history reads the revisions of a release, oldest first. A release with no
// records gives no revisions and no error.
func (s *session) history(name string) ([]revision, error) {
	records, err := helmaction.NewHistory(s.cfg).Run(name)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return nil, nil
	}

	if err != nil {
		return nil, kubeaccess.Redact(fmt.Errorf("read the history of release %s: %w", name, err), s.secrets)
	}

	revs, err := readHistory(records)
	if err != nil {
		return nil, fmt.Errorf("read the history of release %s: %w", name, err)
	}

	return revs, nil
}

// rollback runs Helm's rollback of a release. Force, recreate and the
// control of hooks keep Helm's defaults, which are off, so the hooks of the
// chart run. MaxHistory stays 0, so Helm removes no revision. A wait ends
// with ctx, and Helm ends it after timeout.
func (s *session) rollback(ctx context.Context, name string, o rollbackOptions) error {
	rb := helmaction.NewRollback(s.cfg)
	rb.Version = o.target
	rb.Timeout = o.timeout
	rb.Description = o.description
	rb.WaitOptions = []kube.WaitOption{kube.WithWaitContext(ctx)}
	rb.WaitStrategy = kube.HookOnlyStrategy

	if o.wait {
		rb.WaitStrategy = kube.StatusWatcherStrategy
	}

	if err := rb.Run(name); err != nil {
		return kubeaccess.Redact(fmt.Errorf("roll back release %s to revision %d: %w", name, o.target, err), s.secrets)
	}

	return nil
}

// restGetter gives Helm the client of one REST config, with one namespace.
// Helm's own getter, genericclioptions.ConfigFlags, reads a kubeconfig, or
// the pod's own account, so it could act with another account than the one
// whose token the action asked for.
type restGetter struct {
	config    *rest.Config
	namespace string
}

var _ genericclioptions.RESTClientGetter = (*restGetter)(nil)

// ToRESTConfig implements genericclioptions.RESTClientGetter.
func (g *restGetter) ToRESTConfig() (*rest.Config, error) {
	return rest.CopyConfig(g.config), nil
}

// ToDiscoveryClient implements genericclioptions.RESTClientGetter.
func (g *restGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	client, err := discovery.NewDiscoveryClientForConfig(rest.CopyConfig(g.config))
	if err != nil {
		return nil, fmt.Errorf("build the discovery client: %w", err)
	}

	return memory.NewMemCacheClient(client), nil
}

// ToRESTMapper implements genericclioptions.RESTClientGetter.
func (g *restGetter) ToRESTMapper() (meta.RESTMapper, error) {
	client, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, fmt.Errorf("build the REST mapper: %w", err)
	}

	return restmapper.NewShortcutExpander(restmapper.NewDeferredDiscoveryRESTMapper(client), client, nil), nil
}

// ToRawKubeConfigLoader implements genericclioptions.RESTClientGetter. Helm
// asks it for the namespace of the call.
func (g *restGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	const name = "sidekick"

	raw := clientcmdapi.NewConfig()
	raw.Clusters[name] = &clientcmdapi.Cluster{
		Server:                   g.config.Host,
		CertificateAuthority:     g.config.CAFile,
		CertificateAuthorityData: g.config.CAData,
		InsecureSkipTLSVerify:    g.config.Insecure,
		TLSServerName:            g.config.ServerName,
	}

	raw.AuthInfos[name] = &clientcmdapi.AuthInfo{Token: g.config.BearerToken}
	raw.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name, Namespace: g.namespace}
	raw.CurrentContext = name

	return clientcmd.NewDefaultClientConfig(*raw, &clientcmd.ConfigOverrides{})
}

// warnings passes only warnings and errors of Helm to a handler. Helm logs
// what it does at the debug level, which Sidekick has no use for.
type warnings struct {
	slog.Handler
}

// warningsOnly returns the handler of logger that drops what is below
// warnings, or a handler that drops everything when logger is nil.
func warningsOnly(logger *slog.Logger) slog.Handler {
	if logger == nil {
		return slog.DiscardHandler
	}

	return warnings{Handler: logger.Handler()}
}

// Enabled implements slog.Handler.
func (w warnings) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn && w.Handler.Enabled(ctx, level)
}

// WithAttrs implements slog.Handler.
func (w warnings) WithAttrs(attrs []slog.Attr) slog.Handler {
	return warnings{Handler: w.Handler.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (w warnings) WithGroup(name string) slog.Handler {
	return warnings{Handler: w.Handler.WithGroup(name)}
}
