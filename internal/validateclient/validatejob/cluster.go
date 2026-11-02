package validatejob

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
)

// Client-side rate limits of the API client, as the Kubernetes source sets
// them. client-go's defaults (5 and 10) would make a burst of validations
// wait.
const (
	clientQPS   = 20
	clientBurst = 40
)

// namespaceFile is where Kubernetes puts the namespace of a pod's service
// account, and namespaceEnv is the variable the chart sets for the same.
const (
	namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	namespaceEnv  = "POD_NAMESPACE"
)

var errNoNamespace = errors.New("the namespace of the pod is unknown; set POD_NAMESPACE")

// inCluster returns a client for the cluster Sidekick runs in, built from
// the pod's service account, and the namespace to run Jobs in: namespace
// when it is set, and the pod's own otherwise.
func inCluster(namespace string) (kubernetes.Interface, string, error) {
	rc, err := rest.InClusterConfig()
	if err != nil {
		return nil, "", fmt.Errorf("job mode runs the validator in the cluster Sidekick runs in: %w", err)
	}

	rc.UserAgent = "sidekick/" + buildinfo.Version
	rc.QPS = clientQPS
	rc.Burst = clientBurst

	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, "", fmt.Errorf("build the client: %w", err)
	}

	if namespace == "" {
		namespace, err = podNamespace()
		if err != nil {
			return nil, "", fmt.Errorf("find the namespace: %w", err)
		}
	}

	return client, namespace, nil
}

// podNamespace returns the namespace of the pod Sidekick runs in.
func podNamespace() (string, error) {
	if raw, err := os.ReadFile(namespaceFile); err == nil {
		if ns := strings.TrimSpace(string(raw)); ns != "" {
			return ns, nil
		}
	}

	if ns := os.Getenv(namespaceEnv); ns != "" {
		return ns, nil
	}

	return "", errNoNamespace
}
