package linear

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestCommentConfigValidate(t *testing.T) {
	t.Parallel()

	good := CommentConfig{Connection: Connection{APIKeySecret: keyRef}, Limits: CommentLimits{Teams: []string{"OPS"}}}

	tests := []struct {
		name    string
		mutate  func(c *CommentConfig)
		wantErr string
	}{
		{name: "valid", mutate: func(*CommentConfig) {}},
		{name: "a team ID", mutate: func(c *CommentConfig) { c.Limits.Teams = []string{opsTeamID} }},
		{name: "a custom URL on https", mutate: func(c *CommentConfig) { c.APIURL = "https://linear.example.com/graphql" }},
		{name: "http on localhost", mutate: func(c *CommentConfig) { c.APIURL = "http://127.0.0.1:9000/graphql" }},
		{name: "no key", mutate: func(c *CommentConfig) { c.APIKeySecret = registry.SecretRef{} }, wantErr: "apiKeySecret: required"},
		{name: "no teams", mutate: func(c *CommentConfig) { c.Limits.Teams = nil }, wantErr: "limits.teams: list at least one team key or ID, such as OPS"},
		{name: "not a team", mutate: func(c *CommentConfig) { c.Limits.Teams = []string{"ops team"} }, wantErr: `limits.teams[0]: "ops team" is not a team key, such as OPS, or a team ID`},
		{name: "http on another host", mutate: func(c *CommentConfig) { c.APIURL = "http://linear.example.com" }, wantErr: "apiURL: must use https; plain http is only allowed for localhost"},
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

	_, err := NewComment(t.Context(), "c", CommentConfig{}, registry.Deps{})
	require.ErrorIs(t, err, errNoSecrets)
}

func TestAuthorization(t *testing.T) {
	t.Parallel()

	require.Equal(t, "lin_api_abc", authorization("lin_api_abc"))
	require.Equal(t, "Bearer oauth-token", authorization("oauth-token"))
}

func TestDeriveUUID(t *testing.T) {
	t.Parallel()

	a := deriveUUID("sidekick/one")
	require.Equal(t, a, deriveUUID("sidekick/one"))
	require.NotEqual(t, a, deriveUUID("sidekick/two"))
	require.True(t, isUUID(a))
	require.Equal(t, byte('4'), a[14])
}

func TestEscape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text", in: "Checkout is slow", want: "Checkout is slow"},
		{name: "a link", in: "[click](http://evil.example.com)", want: `\[click\]\(http\://evil.example.com\)`},
		{name: "a mention", in: "@alice please look", want: `\@alice please look`},
		{name: "emphasis and code", in: "*bold* _it_ `code`", want: "\\*bold\\* \\_it\\_ \\`code\\`"},
		{name: "a heading and a list", in: "# Title\n- item\n1. first", want: "\\# Title\n\\- item\n1\\. first"},
		{name: "html", in: "<img src=x onerror=1>", want: `\<img src\=x onerror\=1\>`},
		{name: "a bare address", in: "see https://evil.example.com", want: `see https\://evil.example.com`},
		{name: "carriage returns", in: "a\r\nb", want: "a\nb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, escape(tt.in))
		})
	}
}

func TestWithFooter(t *testing.T) {
	t.Parallel()

	got := withFooter("Hello *there*", testPublicURL, testInvestigationID)
	require.Equal(t, "Hello \\*there\\*\n\n---\nPosted by Sidekick after an approver approved it. [Investigation]("+
		testPublicURL+"/investigations/"+testInvestigationID+")", got)

	require.Equal(t, "Hello\n\n---\nPosted by Sidekick after an approver approved it.", withFooter("Hello", "", testInvestigationID))
}
