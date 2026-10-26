// Package kubernetes is the native Kubernetes source (SRC-3). It registers
// itself as the source type "kubernetes".
//
// A source reads one cluster through client-go, with the pod's service
// account or with a kubeconfig file from a Secret. It offers six tools:
// events, workload status, pod problems, rollout history, node conditions
// and, only when logs are turned on, pod logs. Every tool only reads, and
// none touches Secrets or ConfigMaps. When namespaces are configured, the
// tools see only those namespaces. Evidence links come from the consoleURL
// template, or are the kubectl command that shows the same thing.
package kubernetes
