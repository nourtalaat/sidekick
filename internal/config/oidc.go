package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/roles"
)

// DefaultGroupsClaim is the ID token claim that holds a user's groups when
// oidc.groupsClaim is empty.
const DefaultGroupsClaim = "groups"

// AnyGroup in a role's group list of oidc.roles matches every user.
const AnyGroup = "*"

// OIDC sets up web UI login through an OpenID Connect provider (UI-1). The
// web UI and the JSON API are served only when the section is set.
type OIDC struct {
	// Issuer is the provider's issuer URL, such as https://acme.okta.com.
	// Its discovery document is read from
	// <issuer>/.well-known/openid-configuration.
	Issuer string `yaml:"issuer"`
	// ClientID is the client ID of Sidekick's app at the provider.
	ClientID string `yaml:"clientID"`
	// ClientSecret holds the app's client secret.
	ClientSecret registry.SecretRef `yaml:"clientSecret"`
	// GroupsClaim is the ID token claim that lists the user's groups.
	GroupsClaim string `yaml:"groupsClaim"`
	// Scopes are asked for besides openid, email and profile, for
	// providers that send groups only for a scope such as groups.
	Scopes []string `yaml:"scopes"`
	// Roles maps each role to the groups that give it. A user gets the
	// highest role any of their groups gives; AnyGroup matches everyone.
	// A user no group matches cannot sign in.
	Roles map[RoleName][]string `yaml:"roles"`
}

// RoleOf returns the highest role that groups give in o.Roles, or 0 when
// none does.
func (o *OIDC) RoleOf(groups []string) roles.Role {
	var best roles.Role

	for name, want := range o.Roles {
		if slices.Contains(want, AnyGroup) || slices.ContainsFunc(want, func(g string) bool { return slices.Contains(groups, g) }) {
			best = max(best, name.Role())
		}
	}

	return best
}

func (o *OIDC) setDefaults() {
	o.GroupsClaim = DefaultGroupsClaim
}

// checkOIDC checks the oidc section, and the server.publicURL login needs.
func (v *validator) checkOIDC() {
	o := v.cfg.OIDC
	if o == nil {
		return
	}

	switch {
	case o.Issuer == "":
		v.errs.add("oidc.issuer", "required")
	case !isSecureURL(o.Issuer):
		v.errs.add("oidc.issuer", "expected an https URL, or http on a loopback address, got %q", o.Issuer)
	}

	if o.ClientID == "" {
		v.errs.add("oidc.clientID", "required")
	}

	if o.ClientSecret == (registry.SecretRef{}) {
		v.errs.add("oidc.clientSecret", "required")
	}

	if o.GroupsClaim == "" {
		v.errs.add("oidc.groupsClaim", "must not be empty")
	}

	for i, s := range o.Scopes {
		if s == "" {
			v.errs.add(fmt.Sprintf("oidc.scopes[%d]", i), "must not be empty")
		}
	}

	v.checkOIDCRoles(o.Roles)

	switch publicURL := v.cfg.Server.PublicURL; {
	case publicURL == "":
		v.errs.add("server.publicURL", "required with the oidc section: the provider sends users back to <publicURL>/auth/callback")
	case isHTTPURL(publicURL) && !isSecureURL(publicURL):
		v.errs.add("server.publicURL", "expected an https URL with the oidc section, or http on a loopback address, since the session cookie is only sent over https, got %q", publicURL)
	}
}

func (v *validator) checkOIDCRoles(m map[RoleName][]string) {
	if len(m) == 0 {
		v.errs.add("oidc.roles", "map at least one role to groups, or nobody can sign in")

		return
	}

	names := asStrings(knownRoleNames)

	for _, name := range sortedKeys(m) {
		at := joinPath("oidc.roles", string(name))
		if name.Role() == 0 {
			v.errs.add(at, "unknown role %q, expected one of %s%s", name, strings.Join(names, ", "), didYouMean(string(name), names))
		}

		if len(m[name]) == 0 {
			v.errs.add(at, "list at least one group, or %q for everyone", AnyGroup)
		}

		for i, g := range m[name] {
			if g == "" {
				v.errs.add(fmt.Sprintf("%s[%d]", at, i), "must not be empty")
			}
		}
	}
}

// isSecureURL reports whether s is an absolute https URL, or an http URL
// on a loopback address, which is only used for local testing.
func isSecureURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return false
	}

	return u.Scheme == "https" || (u.Scheme == "http" && httpclient.IsLoopbackHost(u.Hostname()))
}
