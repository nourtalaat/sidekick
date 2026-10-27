package sinkhttp

import (
	"fmt"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// CheckURL checks that raw is an absolute https URL, so credentials and
// signed bodies never cross the network unencrypted. Plain http is allowed
// only for a loopback host, as httpclient.IsLoopbackHost decides, such as
// a local test server. A user name or password in the URL is refused,
// because the URL shows up in config listings and logs. key names the
// config field in the error.
func CheckURL(key, raw string) error {
	u, err := url.Parse(raw)

	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return fmt.Errorf("%s: must be an absolute https URL", key)
	case u.User != nil:
		return fmt.Errorf("%s: must not hold a user name or password; use the output's secret instead", key)
	case u.Scheme == "http" && !httpclient.IsLoopbackHost(u.Hostname()):
		return fmt.Errorf("%s: must use https; plain http is only allowed for localhost", key)
	default:
		return nil
	}
}
