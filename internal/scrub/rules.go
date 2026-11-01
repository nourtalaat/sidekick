package scrub

import (
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

// Kind is what a cleaned value was: it names the placeholder.
type Kind string

// The kinds of value the cleaner replaces. A placeholder is the kind, a
// hyphen and a number, for example host-1.
const (
	KindHost    Kind = "host"
	KindUser    Kind = "user"
	KindAccount Kind = "account"
	KindIP      Kind = "ip"
)

const (
	// minValueLen is the shortest value the cleaner learns from a key, so
	// that a one or two letter value does not rewrite ordinary words.
	minValueLen = 3
	// minAccountLen is the shortest account, tenant or project value.
	minAccountLen = 4
	// maxJSONDepth bounds how deep a string that holds JSON, that holds a
	// string that holds JSON, is read.
	maxJSONDepth = 4
	// minOpaqueLen is the shortest mixed letter and digit token that is
	// flagged as an opaque ID.
	minOpaqueLen = 24
)

// hostKeys, userKeys and accountKeys are the key names, lower-cased and
// without "_", "-" and ".", whose values are of that kind.
var (
	hostKeys = []string{
		"host", "hostname", "node", "nodename", "server", "servername", "instance", "domain", "fqdn",
		"remotehost", "clienthost", "vhost", "dbhost", "serverhost",
	}
	userKeys = []string{
		"user", "username", "login", "principal", "actor", "author", "committer", "assignee", "requester",
		"reporter", "creator", "createdby", "pusher", "triggeredby", "deployedby", "approver", "dbuser", "mail",
	}
	accountKeys = []string{
		"account", "accountid", "awsaccountid", "accountnumber", "tenant", "tenantid", "org", "orgid",
		"organization", "organizationid", "customer", "customerid", "customername", "subscriptionid",
		"project", "projectid", "company",
	}
)

// passthroughKeys are matcher operators: the value under one belongs to
// the argument name above it.
var passthroughKeys = []string{"equals", "oneof", "contains"}

// genericValues are values under an entity key that name no one, so they
// are kept.
var genericValues = []string{
	"none", "null", "nil", "unknown", "n/a", "na", "true", "false", "root", "admin", "administrator", "system",
	"nobody", "default", "anonymous", "guest", "public", "local", "localhost", "example", "test", "prod",
	"production", "staging", "dev", "development", "service", "user", "users", "host", "hosts", "server",
	"client", "kubernetes", "cluster", "unspecified", "undefined",
}

// publicHosts are hosts anyone can reach and that name no deployment. A
// host that ends in one of publicSuffixes is public too.
var (
	publicHosts    = []string{"github.com", "www.github.com", "api.github.com", "raw.githubusercontent.com", "gitlab.com", "bitbucket.org", "localhost"}
	publicSuffixes = []string{".example.com", ".example.org", ".example.net", ".test", ".invalid", ".example"}
)

// publicHostRoots are the reserved example domains themselves.
var publicHostRoots = []string{"example.com", "example.org", "example.net"}

// tlds are the last labels that make a dotted name a host name. Country
// codes are left out on purpose: main.go and app.py would match.
var tlds = []string{
	"com", "net", "org", "io", "dev", "app", "cloud", "tech", "ai", "co", "info", "biz", "edu", "gov", "int",
	"xyz", "online", "site", "internal", "local", "lan", "corp", "intranet", "home", "svc", "cluster",
	"localdomain", "example", "test", "invalid",
}

var (
	urlPattern  = regexp.MustCompile("(?i)\\b[a-z][a-z0-9+.-]*://[^\\s\"'<>\\\\^`{|}]+")
	hostPattern = regexp.MustCompile(`(?i)\b[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+\b`)
	// pairPattern finds key=value, key: value and "key":"value", also when
	// the quotes are escaped as they are in JSON inside a string.
	pairPattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])([A-Za-z][A-Za-z0-9_.-]{1,40})\\?["']?[ \t]*[:=][ \t]*\\?(?:"([^"\\\r\n]*)|'([^'\r\n]*)'|([^\s"',;&)}\]\\]+))`)
	arnPattern  = regexp.MustCompile(`arn:aws[a-z-]*:[a-z0-9-]+:[a-z0-9-]*:([0-9]{12}):`)
	// placeholderLike matches a value the cleaner or the redactor wrote.
	placeholderLike = regexp.MustCompile(`^(?:(?:host|user|account|ip)-[0-9]+|\[REDACTED:[a-z][a-z0-9_-]{0,31}:[0-9a-f]{6}\])$`)
	redactedToken   = regexp.MustCompile(`\[REDACTED:[a-z][a-z0-9_-]{0,31}:[0-9a-f]{6}\]`)
	hostValue       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
	userValue       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._@+' -]*[a-z0-9])?$`)
	accountValue    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
	portSuffix      = regexp.MustCompile(`:[0-9]{1,5}$`)
	uuidPattern     = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	opaquePattern   = regexp.MustCompile(`\b[A-Za-z0-9_-]{24,}\b`)
	hexPattern      = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	codeHostPath    = regexp.MustCompile(`(?i)\b(?:github\.com|gitlab\.com|bitbucket\.org)/([A-Za-z0-9_.-]+)/[A-Za-z0-9_.-]+`)
)

// kindOfKey returns the kind of value the key holds, or "".
func kindOfKey(key string) Kind {
	k := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(strings.ToLower(key))

	switch {
	case slices.Contains(hostKeys, k):
		return KindHost
	case slices.Contains(userKeys, k):
		return KindUser
	case slices.Contains(accountKeys, k):
		return KindAccount
	}

	return ""
}

// isPassthrough reports whether key is a matcher operator.
func isPassthrough(key string) bool {
	return slices.Contains(passthroughKeys, strings.ToLower(key))
}

// isPublicHost reports whether host (lower-case) is a public one.
func isPublicHost(host string) bool {
	if slices.Contains(publicHosts, host) || slices.Contains(publicHostRoots, host) {
		return true
	}

	return slices.ContainsFunc(publicSuffixes, func(s string) bool { return strings.HasSuffix(host, s) })
}

// hasHostSuffix reports whether the last label of a dotted name is one of
// tlds.
func hasHostSuffix(name string) bool {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return false
	}

	return slices.Contains(tlds, strings.ToLower(name[i+1:]))
}

// isIP reports whether s is an IP address.
func isIP(s string) bool {
	_, err := netip.ParseAddr(s)

	return err == nil
}

// normalize returns the value to learn for kind, or "" when v is not one:
// too short, generic, a placeholder, or not shaped like the kind.
func normalize(kind Kind, v string) string {
	v = strings.ToLower(strings.Trim(strings.TrimSpace(v), "\"'`<>[](){},;"))
	if v == "" || placeholderLike.MatchString(v) || strings.Contains(v, "[redacted:") {
		return ""
	}

	switch kind {
	case KindIP:
		if !isIP(v) {
			return ""
		}

		return v
	case KindHost:
		return normalizeHost(v)
	case KindUser:
		if len(v) < minValueLen || slices.Contains(genericValues, v) || !userValue.MatchString(v) || !strings.ContainsAny(v, "abcdefghijklmnopqrstuvwxyz") {
			return ""
		}

		return v
	case KindAccount:
		if len(v) < minAccountLen || slices.Contains(genericValues, v) || !accountValue.MatchString(v) {
			return ""
		}

		return v
	}

	return ""
}

// normalizeHost cuts the port and a trailing dot off a host and checks it.
func normalizeHost(v string) string {
	if !isIP(v) {
		v = portSuffix.ReplaceAllString(v, "")
	}

	v = strings.TrimSuffix(v, ".")

	switch {
	case placeholderLike.MatchString(v):
		return ""
	case len(v) < minValueLen, slices.Contains(genericValues, v), isPublicHost(v), !hostValue.MatchString(v):
		return ""
	case strings.Trim(v, "0123456789.") == "":
		return ""
	}

	return v
}
