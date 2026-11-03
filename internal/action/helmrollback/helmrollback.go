// Package helmrollback is the action helm.rollback: it puts a Helm release
// back on an earlier revision with Helm's own Go library, the code that
// "helm rollback" runs, after an approver approved it.
//
// It uses Helm v4.3.1 (module helm.sh/helm/v4). Helm v3 gets security fixes
// only until 2026-11-11 (https://helm.sh/blog/helm-4-released/), and both
// lines need the same k8s.io module versions as Sidekick.
package helmrollback

// Helm's Go library, which the action calls.
import _ "helm.sh/helm/v4/pkg/action"
