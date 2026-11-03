package datadogapi

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// defaultSite is the Datadog site used when the config names none. The API
// is at api.<site>.
const defaultSite = "datadoghq.com"

// siteHost is the shape of a Datadog site: a DNS name with a dot, such as
// datadoghq.com, datadoghq.eu or us3.datadoghq.com.
var siteHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

// CheckConnection checks, without secrets or network calls, the keys that
// say where the API is and how to reach it: site and url cannot be set
// together, url must be https (or http on a loopback host), site must be a
// Datadog site, and both key secrets are required. Each error starts with
// the key of its field.
func CheckConnection(site, rawURL string, apiKey, appKey registry.SecretRef) []error {
	var errs []error

	switch {
	case site != "" && rawURL != "":
		errs = append(errs, errors.New("url: cannot be set together with site"))
	case rawURL != "":
		if err := checkURL(rawURL); err != nil {
			errs = append(errs, err)
		}
	case site != "" && !siteHost.MatchString(site):
		errs = append(errs, fmt.Errorf("site: %q must be a Datadog site, such as datadoghq.com or datadoghq.eu", site))
	}

	if apiKey == (registry.SecretRef{}) {
		errs = append(errs, errors.New("apiKeySecret: required"))
	}

	if appKey == (registry.SecretRef{}) {
		errs = append(errs, errors.New("appKeySecret: required"))
	}

	return errs
}

// APIURL returns the root of the API, without a trailing slash: rawURL when
// it is set, and otherwise api.<site>, with the default site when site is
// empty.
func APIURL(site, rawURL string) string {
	if rawURL != "" {
		return strings.TrimSuffix(rawURL, "/")
	}

	return "https://api." + resolveSite(site)
}

// AppHost returns the host of the Datadog web app for a site, with the
// default site when it is empty. Sites of two labels, such as datadoghq.com,
// use app.<site>; regional sites such as us3.datadoghq.com are the app host
// themselves.
func AppHost(site string) string {
	site = resolveSite(site)

	if strings.Count(site, ".") == 1 {
		return "app." + site
	}

	return site
}

// resolveSite returns the Datadog site, with the default filled in.
func resolveSite(site string) string {
	if site == "" {
		return defaultSite
	}

	return site
}

// checkURL checks that the URL is absolute and keeps the keys from crossing
// the network unencrypted: https, or http on a loopback host.
func checkURL(raw string) error {
	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return fmt.Errorf("url: %q must be an absolute https URL, such as https://api.datadoghq.eu", raw)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("url: %q must use https; plain http is only allowed for localhost", raw)
	default:
		return nil
	}
}
