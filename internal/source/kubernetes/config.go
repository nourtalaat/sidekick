package kubernetes

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/template"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Config is the config section of one Kubernetes source.
type Config struct {
	source.Settings `yaml:",inline"`

	// Namespaces limits every tool to these namespaces. Empty means every
	// namespace, which needs a ClusterRole.
	Namespaces []string `yaml:"namespaces"`
	// KubeconfigSecret is a Secret key that holds a kubeconfig file, for a
	// cluster other than the one Sidekick runs in. Nil means the pod's
	// service account.
	KubeconfigSecret *registry.SecretRef `yaml:"kubeconfigSecret"`
	// Context is the kubeconfig context to use. Empty means the file's
	// current context. Only with KubeconfigSecret.
	Context string `yaml:"context"`
	// Logs turns on the get_pod_logs tool, which needs get on pods/log.
	Logs bool `yaml:"logs"`
	// ConsoleURL is a Go template for evidence links to one object, with
	// the fields Kind, Resource, Namespace and Name, for example
	// "https://headlamp.example.com/c/main/{{.Resource}}/{{.Namespace}}/{{.Name}}".
	// Empty means the evidence is the matching kubectl command.
	ConsoleURL string `yaml:"consoleURL"`
}

// sampleObject fills the consoleURL template when the config is checked.
var sampleObject = object{Kind: "Deployment", Resource: "deployments", Namespace: "default", Name: "example"}

// Validate checks the section without secrets or network calls: the
// namespaces are valid and listed once, context comes with
// kubeconfigSecret, and consoleURL renders an absolute http or https URL.
// Each error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	seen := map[string]bool{}

	for i, ns := range c.Namespaces {
		key := fmt.Sprintf("namespaces[%d]", i)

		switch problems := validation.IsDNS1123Label(ns); {
		case len(problems) > 0:
			errs = append(errs, fmt.Errorf("%s: %q is not a valid namespace name: %s", key, ns, strings.Join(problems, "; ")))
		case seen[ns]:
			errs = append(errs, fmt.Errorf("%s: %q is listed twice", key, ns))
		}

		seen[ns] = true
	}

	if c.Context != "" && c.KubeconfigSecret == nil {
		errs = append(errs, errors.New("context: only with kubeconfigSecret"))
	}

	if c.ConsoleURL != "" {
		if _, err := parseConsoleURL(c.ConsoleURL); err != nil {
			errs = append(errs, fmt.Errorf("consoleURL: %w", err))
		}
	}

	return errors.Join(errs...)
}

// parseConsoleURL parses the consoleURL template and checks that it
// renders an absolute http or https URL for a sample object.
func parseConsoleURL(text string) (*template.Template, error) {
	tmpl, err := template.New("consoleURL").Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("parse the template: %w", err)
	}

	link, err := render(tmpl, sampleObject)
	if err != nil {
		return nil, fmt.Errorf("render the template: %w", err)
	}

	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q must render an absolute http or https URL, not %q", text, link)
	}

	return tmpl, nil
}

// render fills the template with the object's fields, each escaped for a
// URL path.
func render(tmpl *template.Template, o object) (string, error) {
	escaped := object{
		Kind:      url.PathEscape(o.Kind),
		Resource:  url.PathEscape(o.Resource),
		Namespace: url.PathEscape(o.Namespace),
		Name:      url.PathEscape(o.Name),
	}

	var b strings.Builder
	if err := tmpl.Execute(&b, escaped); err != nil {
		return "", fmt.Errorf("execute: %w", err)
	}

	return b.String(), nil
}
