package jira

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
)

func goodConnection() Connection {
	return Connection{URL: "https://acme.atlassian.net", Email: testEmail, APITokenSecret: tokenRef}
}

func TestConnectionValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(c *Connection)
		wantErr string
	}{
		{name: "valid", mutate: func(*Connection) {}},
		{name: "http on localhost", mutate: func(c *Connection) { c.URL = "http://127.0.0.1:9000" }},
		{name: "no URL", mutate: func(c *Connection) { c.URL = "" }, wantErr: "url: required, the address of the Jira Cloud site, such as https://acme.atlassian.net"},
		{name: "http on another host", mutate: func(c *Connection) { c.URL = "http://acme.atlassian.net" }, wantErr: "url: must use https; plain http is only allowed for localhost"},
		{name: "no email", mutate: func(c *Connection) { c.Email = "" }, wantErr: "email: required, the email address of the account the API token belongs to"},
		{name: "not an email", mutate: func(c *Connection) { c.Email = "sidekick" }, wantErr: "email: expected an email address"},
		{name: "no token", mutate: func(c *Connection) { c.APITokenSecret = registry.SecretRef{} }, wantErr: "apiTokenSecret: required"},
		{
			name:    "a bearer token for Data Center",
			mutate:  func(c *Connection) { c.BearerTokenSecret = registry.SecretRef{Name: "jira", Key: "pat"} },
			wantErr: "bearerTokenSecret: Jira Data Center is not supported by this action yet; use url, email and apiTokenSecret with a Jira Cloud site",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := goodConnection()
			tt.mutate(&c)

			cfg := CommentConfig{Connection: c, Limits: CommentLimits{Projects: []string{"OPS"}}}

			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestCommentConfigValidateProjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		projects []string
		wantErr  string
	}{
		{name: "one project", projects: []string{"OPS"}},
		{name: "none", wantErr: "limits.projects: list at least one project key, such as OPS"},
		{name: "not a key", projects: []string{"ops project"}, wantErr: `limits.projects[0]: "ops project" is not a project key, such as OPS`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := CommentConfig{Connection: goodConnection(), Limits: CommentLimits{Projects: tt.projects}}

			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestIssueConfigValidate(t *testing.T) {
	t.Parallel()

	good := IssueConfig{Connection: goodConnection(), Limits: IssueLimits{Projects: []string{"OPS"}, IssueTypes: []string{"Task"}, Labels: []string{"incident"}, MaxPerDay: 5}}

	tests := []struct {
		name    string
		mutate  func(c *IssueConfig)
		wantErr string
	}{
		{name: "valid", mutate: func(*IssueConfig) {}},
		{name: "no labels", mutate: func(c *IssueConfig) { c.Limits.Labels = nil }},
		{name: "no issue types", mutate: func(c *IssueConfig) { c.Limits.IssueTypes = nil }, wantErr: "limits.issueTypes: list at least one issue type, such as Task"},
		{name: "an empty issue type", mutate: func(c *IssueConfig) { c.Limits.IssueTypes = []string{" "} }, wantErr: "limits.issueTypes[0]: must not be empty or longer than 60 characters"},
		{name: "no maxPerDay", mutate: func(c *IssueConfig) { c.Limits.MaxPerDay = 0 }, wantErr: "limits.maxPerDay: required, at least 1"},
		{name: "a label with a space", mutate: func(c *IssueConfig) { c.Limits.Labels = []string{"in cident"} }, wantErr: `limits.labels[0]: "in cident" is not a label: it must have no spaces and at most 100 characters`},
		{name: "no projects", mutate: func(c *IssueConfig) { c.Limits.Projects = nil }, wantErr: "limits.projects: list at least one project key, such as OPS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := good
			tt.mutate(&c)

			err := c.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestNewNeedsSecrets(t *testing.T) {
	t.Parallel()

	_, err := NewComment(t.Context(), "c", CommentConfig{Connection: goodConnection()}, registry.Deps{})
	require.ErrorIs(t, err, errNoSecrets)

	_, err = NewIssue(t.Context(), "i", IssueConfig{Connection: goodConnection()}, registry.Deps{})
	require.ErrorIs(t, err, errNoSecrets)
}

func TestKeyHash(t *testing.T) {
	t.Parallel()

	a := keyHash("sidekick/one")
	require.Len(t, a, keyHexChars)
	require.Equal(t, a, keyHash("sidekick/one"))
	require.NotEqual(t, a, keyHash("sidekick/two"))
}

func TestDocument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		link   string
		marker string
		want   string
	}{
		{name: "one line", body: "Hello", want: "Hello|" + footerText},
		{name: "paragraphs and breaks", body: "One\ntwo\n\nThree", want: "One|two|Three|" + footerText},
		{name: "a link and a marker", body: "Hello", link: "https://sidekick.example.com/investigations/1", marker: "sidekick:abc", want: "Hello|" + footerText + "| |Investigation|sidekick:abc"},
		{name: "control characters", body: "a\x00b\r\nc", want: "ab|c|" + footerText},
		{name: "markup stays text", body: "[~admin] {code}x{code} *b* [a](http://evil)", want: "[~admin] {code}x{code} *b* [a](http://evil)|" + footerText},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			doc := document(tt.body, footerText, tt.link, tt.marker)
			require.Equal(t, "doc", doc.Type)
			require.Equal(t, adfVersion, doc.Version)

			raw, err := json.Marshal(doc)
			require.NoError(t, err)
			require.Equal(t, tt.want, bodyText(t, raw))
		})
	}
}

func TestOneLine(t *testing.T) {
	t.Parallel()

	require.Equal(t, "a b c", oneLine("  a \n b\t\tc \r\n"))
	require.Empty(t, oneLine(" \n "))
}
