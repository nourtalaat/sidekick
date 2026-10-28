package kubernetes

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// samplePodsPerNamespace is how many pods SamplePod asks for in each
// namespace.
const samplePodsPerNamespace = 100

// systemNamespaces are the namespaces SamplePod leaves out, since an alert
// about one of their pods is a poor example.
var systemNamespaces = []string{"kube-system", "kube-public", "kube-node-lease"}

// SamplePod returns the namespace and name of a running pod that the source
// may read, outside the system namespaces, for example to write a sample
// alert about a pod that exists. found is false when there is none. It
// makes one list call for each namespace the source reads, until it finds a
// pod, and needs the same permission as list_pod_problems.
func (s *Source) SamplePod(ctx context.Context) (namespace, name string, found bool, err error) {
	c, err := s.clients(ctx)
	if err != nil {
		return "", "", false, fmt.Errorf("get a Kubernetes client: %w", err)
	}

	namespaces, err := s.scope("")
	if err != nil {
		return "", "", false, fmt.Errorf("find the namespaces to read: %w", err)
	}

	opts := metav1.ListOptions{FieldSelector: "status.phase=" + string(corev1.PodRunning), Limit: samplePodsPerNamespace}

	for _, ns := range namespaces {
		pods, err := c.CoreV1().Pods(ns).List(ctx, opts)
		if err != nil {
			return "", "", false, apiError("list", resPods, ns, err)
		}

		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Status.Phase == corev1.PodRunning && !slices.Contains(systemNamespaces, p.Namespace) {
				return p.Namespace, p.Name, true, nil
			}
		}
	}

	return "", "", false, nil
}
