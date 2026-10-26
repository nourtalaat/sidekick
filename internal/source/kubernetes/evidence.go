package kubernetes

import (
	"strings"
	"text/template"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxLinks is the most console links one result carries.
const maxLinks = 10

// object names one Kubernetes object for an evidence link. It is also the
// data of the consoleURL template.
type object struct {
	// Kind is the object's kind, for example Deployment.
	Kind string
	// Resource is the lowercase plural resource name, for example
	// deployments.
	Resource string
	// Namespace is empty for cluster-scoped objects such as nodes.
	Namespace string
	Name      string
}

// title names the object for a person, for example "Deployment prod/checkout".
func (o object) title() string {
	if o.Namespace == "" {
		return o.Kind + " " + o.Name
	}

	return o.Kind + " " + o.Namespace + "/" + o.Name
}

// linker builds evidence: console links when a consoleURL template is set,
// otherwise the kubectl command that shows the same thing.
type linker struct {
	console *template.Template
}

// evidence returns a console link for each object, at most maxLinks of
// them, when there is a template and at least one object. Otherwise it
// returns the command, as evidence without a URL.
func (l linker) evidence(kind source.EvidenceKind, objects []object, command string) []source.Evidence {
	if l.console != nil {
		var links []source.Evidence

		for _, o := range objects {
			if len(links) == maxLinks {
				break
			}

			link, err := render(l.console, o)
			if err != nil {
				continue
			}

			links = append(links, source.Evidence{Kind: kind, Title: o.title(), URL: link})
		}

		if len(links) > 0 {
			return links
		}
	}

	return []source.Evidence{{Kind: kind, Title: command}}
}

// kubectl builds a kubectl command line, quoting arguments that a shell
// would split or expand.
func kubectl(args ...string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, "kubectl")

	for _, a := range args {
		if a != "" {
			parts = append(parts, shellQuote(a))
		}
	}

	return strings.Join(parts, " ")
}

// namespaceArgs returns the kubectl flags for one namespace, or for every
// namespace when ns is empty.
func namespaceArgs(ns string) []string {
	if ns == "" {
		return []string{"-A"}
	}

	return []string{"-n", ns}
}

// shellQuote quotes s in single quotes when it holds anything other than
// characters that are safe unquoted in a shell.
func shellQuote(s string) string {
	if strings.IndexFunc(s, unsafeInShell) < 0 {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// unsafeInShell reports whether a shell could split or expand on r.
func unsafeInShell(r rune) bool {
	safe := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,", r)

	return !safe
}
