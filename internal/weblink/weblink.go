// Package weblink builds links to pages of the web UI from server.publicURL.
package weblink

import (
	"net/url"
	"strings"
)

// Investigation returns the address of the investigation page with id
// under publicURL, such as https://sidekick.example.com/investigations/<id>.
// It returns "" when publicURL is empty or is not an absolute http or
// https URL, so a link is left out instead of pointing nowhere.
func Investigation(publicURL, id string) string {
	u, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || id == "" {
		return ""
	}

	return strings.TrimRight(u.String(), "/") + "/investigations/" + url.PathEscape(id)
}
