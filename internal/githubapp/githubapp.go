// Package githubapp authenticates to the GitHub REST API as one installation
// of a GitHub App. The GitHub source and the pull request action both use
// it, each with an app of its own.
//
// A client made here asks GitHub for installation tokens that carry only the
// permissions its caller names, whatever the app itself may do. It reads the
// app's private key from its secret on each token exchange, so a rotated key
// needs no restart, sends the token only to the API's own origin, and fails
// the read of any response body larger than a fixed limit.
package githubapp

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// accessRead and accessWrite are the access levels of an installation token.
const (
	accessRead  = "read"
	accessWrite = "write"
)

// ErrNoSecrets is returned by NewClient when it has no way to read the
// app's private key.
var ErrNoSecrets = errors.New("no secrets resolver to read the GitHub App private key with")

// App identifies one installation of a GitHub App.
type App struct {
	// AppID is the app's numeric ID, from its settings page.
	AppID int64 `yaml:"appID"`
	// InstallationID is the ID of the app's installation on the account
	// that owns the repositories.
	InstallationID int64 `yaml:"installationID"`
	// PrivateKeySecret holds the app's private key, in PEM form.
	PrivateKeySecret registry.SecretRef `yaml:"privateKeySecret"`
}

// Check returns an error for each part of the app that is missing. Each
// error starts with the key of its field below key, for example
// "app.appID: required" for the key "app".
func (a App) Check(key string) []error {
	var errs []error

	if a.AppID <= 0 {
		errs = append(errs, fmt.Errorf("%s.appID: required", key))
	}

	if a.InstallationID <= 0 {
		errs = append(errs, fmt.Errorf("%s.installationID: required", key))
	}

	if a.PrivateKeySecret == (registry.SecretRef{}) {
		errs = append(errs, fmt.Errorf("%s.privateKeySecret: required", key))
	}

	return errs
}

// Options set up a client. App, BaseURL, Secrets, UserAgent and Permissions
// are required.
type Options struct {
	App App
	// BaseURL is the REST API root, ending in a slash.
	BaseURL string
	// Secrets reads the app's private key.
	Secrets registry.Secrets
	// HTTP is the client to send requests with. Nil means httpclient.New().
	// Its transport is wrapped, not changed.
	HTTP      *http.Client
	UserAgent string
	// Permissions are the permissions each installation token is asked for.
	Permissions *gh.InstallationPermissions
	// Now is the clock the tokens are timed by. Nil means time.Now.
	Now func() time.Time
}

// NewClient returns a go-github client that authenticates as the app's
// installation. It makes no network call: the first token is made by the
// first request.
func NewClient(opts Options) (*gh.Client, error) {
	if opts.Secrets == nil {
		return nil, ErrNoSecrets
	}

	origin, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse the base URL: %w", err)
	}

	base := opts.HTTP
	if base == nil {
		base = httpclient.New()
	}

	next := base.Transport
	if next == nil {
		next = http.DefaultTransport
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	limited := limitTransport{next: next}

	exchange := *base
	exchange.Transport = limited

	source := &tokenSource{
		appID:          opts.App.AppID,
		installationID: opts.App.InstallationID,
		privateKey:     opts.App.PrivateKeySecret,
		permissions:    opts.Permissions,
		secrets:        opts.Secrets,
		client:         &exchange,
		baseURL:        opts.BaseURL,
		userAgent:      opts.UserAgent,
		now:            now,
	}

	api := *base
	api.Transport = &authTransport{next: limited, origin: origin, tokens: source}

	baseURL := opts.BaseURL

	client, err := gh.NewClient(gh.WithHTTPClient(&api), gh.WithURLs(&baseURL, nil), gh.WithUserAgent(opts.UserAgent))
	if err != nil {
		return nil, fmt.Errorf("make the go-github client: %w", err)
	}

	return client, nil
}

// ReadOnly returns the permissions of the GitHub source: read access to
// workflow runs, contents, pull requests and metadata. A token never gets
// more than these, even when the app was given more.
func ReadOnly() *gh.InstallationPermissions {
	read := accessRead

	return &gh.InstallationPermissions{
		Actions:      &read,
		Contents:     &read,
		Metadata:     &read,
		PullRequests: &read,
	}
}

// ReadWrite returns the permissions of the pull request action: write
// access to contents and pull requests, which is what it takes to push a
// branch and open a pull request, and read access to metadata.
func ReadWrite() *gh.InstallationPermissions {
	read, write := accessRead, accessWrite

	return &gh.InstallationPermissions{
		Contents:     &write,
		Metadata:     &read,
		PullRequests: &write,
	}
}
