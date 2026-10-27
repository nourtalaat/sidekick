package linear

import (
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/output/sinkhttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	valid := func() Config {
		return Config{APIKeySecret: apiKeyRef, Workspace: "acme", Teams: []string{"OPS", "pay_2"}}
	}

	tests := []struct {
		name     string
		edit     func(c *Config)
		wantErrs []string
	}{
		{name: "valid", edit: func(*Config) {}},
		{name: "with labels and an API URL", edit: func(c *Config) { c.Labels, c.APIURL = []string{"ticket"}, "https://linear.example.com/graphql" }},
		{
			name:     "empty",
			edit:     func(c *Config) { *c = Config{} },
			wantErrs: []string{"apiKeySecret: required", "workspace: required", "teams: list at least one team key"},
		},
		{name: "a workspace that is a URL", edit: func(c *Config) { c.Workspace = "https://linear.app/acme" }, wantErrs: []string{"workspace: expected the URL key"}},
		{name: "a team that is not a key", edit: func(c *Config) { c.Teams = []string{"OPS", "Ops Team"} }, wantErrs: []string{`teams[1]: "Ops Team" is not a team key`}},
		{name: "an empty label", edit: func(c *Config) { c.Labels = []string{" "} }, wantErrs: []string{"labels[0]: must not be empty"}},
		{name: "an http API URL", edit: func(c *Config) { c.APIURL = "http://linear.example.com/graphql" }, wantErrs: []string{"apiURL: must use https"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := valid()
			tt.edit(&c)

			err := c.Validate()
			if len(tt.wantErrs) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.wantErrs {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

func TestNewWithoutSecrets(t *testing.T) {
	t.Parallel()

	_, err := New(t.Context(), testOutput, Config{}, registry.Deps{})
	require.ErrorIs(t, err, errNoSecrets)
}

func TestPublish(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		edit       func(u *output.Update)
		responses  []fakehttp.Response
		wantRef    output.Ref
		wantErr    bool
		wantBodyMD string
	}{
		{
			name:       "comments on the issue in the label",
			responses:  []fakehttp.Response{{BodyFile: "fixture/issue.json"}, {BodyFile: "fixture/comment_created.json"}},
			wantRef:    postedRef("5"),
			wantBodyMD: "fixture/golden/true-positive.md",
		},
		{
			name: "comments on the issue an alert links to",
			edit: func(u *output.Update) {
				u.Event.Labels = nil
				u.Event.Links = []event.Link{{Title: "Ticket", URL: "https://linear.app/acme/issue/OPS-123/checkout-is-slow"}}
			},
			responses: []fakehttp.Response{{BodyFile: "fixture/issue.json"}, {BodyFile: "fixture/comment_created.json"}},
			wantRef:   postedRef("5"),
		},
		{
			name:    "no related issue posts nothing",
			edit:    func(u *output.Update) { u.Event.Labels = nil },
			wantRef: output.Ref{Sink: testOutput, Data: map[string]string{"seq": "5"}},
		},
		{
			name:      "an issue that moved to another team gets no comment",
			responses: []fakehttp.Response{{BodyFile: "fixture/issue_moved.json"}},
			wantRef:   output.Ref{Sink: testOutput, Data: map[string]string{"seq": "5"}},
		},
		{
			name: "a newer final edits the comment",
			edit: func(u *output.Update) {
				prev := postedRef("4")
				u.Previous = &prev
			},
			responses: []fakehttp.Response{{BodyFile: "fixture/comment_updated.json"}},
			wantRef:   postedRef("5"),
		},
		{
			name:      "a server error fails the publish, so the job retries it",
			responses: []fakehttp.Response{{BodyFile: "fixture/issue.json"}, {Status: http.StatusServiceUnavailable}},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if len(tt.responses) > 0 {
				api.Handle(http.MethodPost, pathGraphQL, tt.responses...)
			}

			api.ExpectAllCalled()

			u := finalUpdate()
			if tt.edit != nil {
				tt.edit(&u)
			}

			got, err := testSink(t, api).Publish(t.Context(), u)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantRef, got)

			if tt.wantBodyMD != "" {
				want, err := os.ReadFile(tt.wantBodyMD)
				require.NoError(t, err)

				input, ok := requests(t, api)[1].Variables["input"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, string(want), input["body"])
			}
		})
	}
}

func TestPublishRetriesAfterAServerError(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodPost, pathGraphQL,
		fakehttp.Response{BodyFile: "fixture/issue.json"},
		fakehttp.Response{Status: http.StatusBadGateway},
		fakehttp.Response{BodyFile: "fixture/issue.json"},
		fakehttp.Response{BodyFile: "fixture/comment_created.json"},
	)
	api.ExpectAllCalled()

	sink := testSink(t, api)

	_, err := sink.Publish(t.Context(), finalUpdate())
	statusErr, ok := errors.AsType[*sinkhttp.StatusError](err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, http.StatusBadGateway, statusErr.Status)

	got, err := sink.Publish(t.Context(), finalUpdate())
	require.NoError(t, err)
	require.Equal(t, postedRef("5"), got)

	reqs := requests(t, api)
	require.Equal(t, reqs[1].Variables["input"], reqs[3].Variables["input"], "the retry sends the same comment ID, so it cannot post twice")
}

func TestPublishWithoutTheAPIKey(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)

	sec := registry.NewMockSecrets(gomock.NewController(t))
	sec.EXPECT().Resolve(gomock.Any(), apiKeyRef).Return(nil, os.ErrNotExist)

	sink, err := New(t.Context(), testOutput, testConfig(api), registry.Deps{Secrets: sec, HTTP: api.Client()})
	require.NoError(t, err)

	_, err = sink.Publish(t.Context(), finalUpdate())
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Empty(t, api.Requests())
}

// postedRef is the ref of the comment in fixture/comment_created.json on
// OPS-123, showing seq.
func postedRef(seq string) output.Ref {
	return output.Ref{
		Sink:     testOutput,
		External: "OPS-123:" + testCommentID,
		Data:     map[string]string{"issue_id": testIssueID, "issue": "OPS-123", "comment": testCommentID, "seq": seq},
	}
}
