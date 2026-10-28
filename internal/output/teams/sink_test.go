package teams

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The identities of the sink tests.
const (
	testAppID    = "00000000-1111-2222-3333-444444444444"
	testTenantID = "55555555-6666-7777-8888-999999999999"
	testChannel  = "19:alerts@thread.tacv2"
	testThread   = "1761642000001"
	mainActivity = "1761642000100"
)

// The paths of the fake Microsoft server.
const (
	tokenPath     = "/login/" + testTenantID + "/oauth2/v2.0/token"
	pathNewThread = "/teams/v3/conversations"
)

// testSecretRef names the client secret in the sink tests.
var testSecretRef = registry.SecretRef{Name: "sidekick-teams", Key: "clientSecret"}

// testStart is the time of the fake clock.
var testStart = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

// fakeClock is the sink's clock: it records every wait and moves on by it.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

// Now returns the clock's time.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// Wait records d and moves the clock on by it.
func (c *fakeClock) Wait(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)

	return nil
}

// Waits returns the recorded waits.
func (c *fakeClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]time.Duration(nil), c.waits...)
}

func TestPublishFirstReply(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// teams is the event's Teams message, nil for a webhook event.
		teams      func(serverURL string) *event.TeamsRef
		wantPath   string
		answer     string
		wantThread string
	}{
		{
			name:       "a webhook event starts a thread in the output's channel",
			wantPath:   pathNewThread,
			answer:     `{"id": "` + testChannel + `;messageid=` + mainActivity + `", "activityId": "` + mainActivity + `"}`,
			wantThread: mainActivity,
		},
		{
			name: "a Teams message gets a reply in its thread, at its own service URL",
			teams: func(serverURL string) *event.TeamsRef {
				return &event.TeamsRef{ChannelID: "19:ops@thread.tacv2", MessageID: "1761642000005", ThreadID: "1761642000002", ServiceURL: serverURL + "/emea/"}
			},
			wantPath:   "/emea/v3/conversations/19:ops@thread.tacv2;messageid=1761642000002/activities",
			answer:     `{"id": "` + mainActivity + `"}`,
			wantThread: "1761642000002",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, tokenPath, tokenAnswer("bf-token"))
			api.Handle(http.MethodPost, tt.wantPath, fakehttp.Response{Status: http.StatusCreated, Body: []byte(tt.answer)})
			api.ExpectAllCalled()

			s, _ := testSink(t, api)

			u := testUpdate(output.PhaseQueued, nil)
			if tt.teams != nil {
				u.Event.Teams = tt.teams(api.URL())
			}

			got, err := s.Publish(t.Context(), u)
			require.NoError(t, err)

			r, ok := refOf(got)
			require.True(t, ok)
			require.Equal(t, tt.wantThread, r.thread)
			require.Equal(t, mainActivity, r.activity)
			require.Equal(t, "teams-thread", got.Sink)

			req := requestTo(t, api, tt.wantPath)
			require.Equal(t, "Bearer bf-token", req.Header.Get("Authorization"))

			a := decodeActivity(t, req.Body, tt.teams == nil)
			require.Equal(t, "Looking into it: Checkout p99 above 2s", a.Summary)
			require.Equal(t, styleWarning, a.Attachments[0].Content.Body[0].Style)

			form, err := url.ParseQuery(string(requestTo(t, api, tokenPath).Body))
			require.NoError(t, err)
			require.Equal(t, "https://api.botframework.com/.default", form.Get("scope"))
			require.Equal(t, "test-client-secret", form.Get("client_secret"))
		})
	}
}

func TestPublishUpdateInPlace(t *testing.T) {
	t.Parallel()

	updatePath := "/teams/v3/conversations/" + testChannel + ";messageid=" + testThread + "/activities/" + mainActivity
	replyPath := "/teams/v3/conversations/" + testChannel + ";messageid=" + testThread + "/activities"

	tests := []struct {
		name string
		// prevSeq and prevEditedAt are what the main message shows.
		prevSeq      int64
		prevEditedAt int64
		seq          int64
		answers      map[string][]fakehttp.Response
		wantWaits    []time.Duration
		wantPaths    []string
		wantSeq      int64
	}{
		{
			name:      "an update edits the message in place",
			prevSeq:   1,
			seq:       2,
			answers:   map[string][]fakehttp.Response{updatePath: {{Body: []byte(`{"id": "` + mainActivity + `"}`)}}},
			wantPaths: []string{updatePath},
			wantSeq:   2,
		},
		{
			name:         "an edit waits for the edit interval",
			prevSeq:      1,
			prevEditedAt: testStart.Add(-time.Second).UnixMilli(),
			seq:          2,
			answers:      map[string][]fakehttp.Response{updatePath: {{Body: []byte(`{}`)}}},
			wantWaits:    []time.Duration{2 * time.Second},
			wantPaths:    []string{updatePath},
			wantSeq:      2,
		},
		{
			name:    "an update older than the one shown is dropped",
			prevSeq: 5,
			seq:     4,
			wantSeq: 5,
		},
		{
			name:    "a deleted message is posted again",
			prevSeq: 1,
			seq:     2,
			answers: map[string][]fakehttp.Response{
				updatePath: {{Status: http.StatusNotFound}},
				replyPath:  {{Status: http.StatusCreated, Body: []byte(`{"id": "1761642000200"}`)}},
			},
			wantPaths: []string{updatePath, replyPath},
			wantSeq:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			if len(tt.answers) > 0 {
				api.Handle(http.MethodPost, tokenPath, tokenAnswer("bf-token"))
			}

			for path, answers := range tt.answers {
				method := http.MethodPut
				if path == replyPath {
					method = http.MethodPost
				}

				api.Handle(method, path, answers...)
			}

			api.ExpectAllCalled()

			s, clock := testSink(t, api)

			u := testUpdate(output.PhaseRunning, nil)
			u.Event.Teams = &event.TeamsRef{ChannelID: testChannel, MessageID: testThread, ThreadID: testThread, ServiceURL: api.URL() + "/teams/"}
			previous := shownRef(api.URL()+"/teams/", tt.prevSeq, "", tt.prevEditedAt)
			u.Previous, u.Seq = &previous, tt.seq
			u.Progress = []output.Progress{{At: testStart, Text: "Checked recent deploys"}}

			got, err := s.Publish(t.Context(), u)
			require.NoError(t, err)

			r, ok := refOf(got)
			require.True(t, ok)
			require.Equal(t, tt.wantSeq, r.seq)
			require.Equal(t, tt.wantWaits, clock.Waits())
			require.Equal(t, tt.wantPaths, connectorPaths(api))

			if len(tt.wantPaths) > 0 {
				a := decodeActivity(t, requestTo(t, api, tt.wantPaths[0]).Body, false)
				require.Equal(t, "Investigating: Checkout p99 above 2s", a.Summary)
				require.Contains(t, runsText(a), iconDone+" Checked recent deploys")
			}
		})
	}
}

func TestPublishFinal(t *testing.T) {
	t.Parallel()

	updatePath := "/teams/v3/conversations/" + testChannel + ";messageid=" + testThread + "/activities/" + mainActivity
	replyPath := "/teams/v3/conversations/" + testChannel + ";messageid=" + testThread + "/activities"

	tests := []struct {
		name        string
		report      *report.Report
		announced   string
		wantReplies int
		wantStyle   string
		wantSummary string
		wantLabel   string
	}{
		{
			name:        "the final result edits the message and posts the verdict in the thread",
			report:      testReport(2),
			wantReplies: 1,
			wantStyle:   styleAttention,
			wantSummary: "Real issue (high confidence): Checkout p99 above 2s",
			wantLabel:   "Real issue",
		},
		{
			name:        "a long report continues in a thread reply",
			report:      testReport(maxFindings + 2),
			wantReplies: 2,
			wantStyle:   styleAttention,
			wantSummary: "Real issue (high confidence): Checkout p99 above 2s",
			wantLabel:   "Real issue",
		},
		{
			name:        "a verdict already announced is not announced again",
			report:      testReport(2),
			announced:   string(report.TruePositive),
			wantStyle:   styleAttention,
			wantSummary: "Real issue (high confidence): Checkout p99 above 2s",
			wantLabel:   "Real issue",
		},
		{
			name:        "a run that could not finish says so",
			report:      &report.Report{StopReason: report.StopError, StopDetail: "the model did not answer"},
			wantReplies: 1,
			wantStyle:   styleWarning,
			wantSummary: "Could not finish: Checkout p99 above 2s",
			wantLabel:   "Could not finish",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodPost, tokenPath, tokenAnswer("bf-token"))
			api.Handle(http.MethodPut, updatePath, fakehttp.Response{Body: []byte(`{"id": "` + mainActivity + `"}`)})

			replies := make([]fakehttp.Response, tt.wantReplies)
			for i := range replies {
				replies[i] = fakehttp.Response{Status: http.StatusCreated, Body: []byte(`{"id": "17616420003` + strconv.Itoa(i) + `"}`)}
			}

			if len(replies) > 0 {
				api.Handle(http.MethodPost, replyPath, replies...)
			}

			api.ExpectAllCalled()

			s, _ := testSink(t, api)

			previous := shownRef(api.URL()+"/teams/", 3, tt.announced, 0)
			u := testUpdate(output.PhaseFinal, tt.report)
			u.Previous, u.Seq = &previous, 4

			got, err := s.Publish(t.Context(), u)
			require.NoError(t, err)

			r, ok := refOf(got)
			require.True(t, ok)
			require.Equal(t, int64(4), r.seq)

			wantAnnounced := string(report.TruePositive)
			if tt.report.StopReason == report.StopError {
				wantAnnounced = announcedFailed
			}

			require.Equal(t, wantAnnounced, r.announced)

			main := decodeActivity(t, requestTo(t, api, updatePath).Body, false)
			require.Equal(t, tt.wantSummary, main.Summary)
			require.Equal(t, tt.wantStyle, main.Attachments[0].Content.Body[0].Style)
			require.Contains(t, runsText(main), tt.wantLabel)
			require.Equal(t, []cardLink{{Type: typeOpenURL, Title: "Open in Sidekick", URL: "https://sidekick.example.com/investigations/inv-1"}}, main.Attachments[0].Content.Actions)

			sent := repliesTo(t, api, replyPath)
			require.Len(t, sent, tt.wantReplies)

			if tt.wantReplies > 0 {
				verdict := sent[len(sent)-1]
				require.Equal(t, tt.wantSummary, verdict.Summary)
				require.Contains(t, runsText(verdict), tt.wantLabel)
			}

			if tt.wantReplies == 2 {
				require.Contains(t, runsText(sent[0]), "More findings")
				require.Contains(t, runsText(sent[0]), "• finding 7")
			}
		})
	}
}

func TestClientRetries(t *testing.T) {
	t.Parallel()

	replyPath := "/teams/v3/conversations/" + testChannel + ";messageid=" + testThread + "/activities"
	created := fakehttp.Response{Status: http.StatusCreated, Body: []byte(`{"id": "` + mainActivity + `"}`)}

	tests := []struct {
		name       string
		tokens     int
		answers    []fakehttp.Response
		wantWaits  []time.Duration
		wantErr    string
		wantErrors float64
	}{
		{
			name:      "a 429 waits for its Retry-After",
			tokens:    1,
			answers:   []fakehttp.Response{{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "2"}}, created},
			wantWaits: []time.Duration{2 * time.Second},
		},
		{
			name:      "a 502 without Retry-After backs off",
			tokens:    1,
			answers:   []fakehttp.Response{{Status: http.StatusBadGateway}, {Status: http.StatusPreconditionFailed}, created},
			wantWaits: []time.Duration{firstBackoff, 2 * firstBackoff},
		},
		{
			name:    "a 401 gets a new token once",
			tokens:  2,
			answers: []fakehttp.Response{{Status: http.StatusUnauthorized}, created},
		},
		{
			name:    "a Retry-After longer than a minute fails the call",
			tokens:  1,
			answers: []fakehttp.Response{{Status: http.StatusTooManyRequests, Headers: map[string]string{"Retry-After": "120"}}},
			wantErr: "retry later",
		},
		{
			name:    "a 403 fails the call",
			tokens:  1,
			answers: []fakehttp.Response{{Status: http.StatusForbidden}},
			wantErr: "teams answered HTTP 403",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)

			tokens := make([]fakehttp.Response, tt.tokens)
			for i := range tokens {
				tokens[i] = tokenAnswer(fmt.Sprintf("bf-token-%d", i+1))
			}

			api.Handle(http.MethodPost, tokenPath, tokens...)
			api.Handle(http.MethodPost, replyPath, tt.answers...)
			api.ExpectAllCalled()

			s, clock := testSink(t, api)

			_, err := s.client.reply(t.Context(), api.URL()+"/teams/", conversationOf(testChannel, testThread), activity{Type: "message"})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantWaits, clock.Waits())

			reqs := repliesRequests(api, replyPath)
			require.Equal(t, fmt.Sprintf("Bearer bf-token-%d", tt.tokens), reqs[len(reqs)-1].Header.Get("Authorization"))
		})
	}
}

func TestClientServiceURLs(t *testing.T) {
	t.Parallel()

	c := &client{allowedHost: "smba.example.com"}

	tests := []struct {
		raw     string
		wantErr bool
	}{
		{raw: "https://smba.trafficmanager.net/emea/"},
		{raw: "https://SMBA.TrafficManager.net/amer/"},
		{raw: "https://smba.infra.gov.teams.microsoft.us/teams/"},
		{raw: "https://smba.example.com/teams/"},
		{raw: "http://127.0.0.1:8080/teams/"},
		{raw: "https://attacker.example.com/teams/", wantErr: true},
		{raw: "https://trafficmanager.net.attacker.example.com/", wantErr: true},
		{raw: "http://smba.trafficmanager.net/emea/", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()

			err := c.checkServiceURL(tt.raw)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestServiceURLRefusedBeforeSending(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	s, _ := testSink(t, api)

	u := testUpdate(output.PhaseQueued, nil)
	u.Event.Teams = &event.TeamsRef{ChannelID: testChannel, ThreadID: testThread, ServiceURL: "https://attacker.example.com/"}

	_, err := s.Publish(t.Context(), u)
	require.ErrorContains(t, err, "not a Bot Framework host")
	require.Empty(t, api.Requests(), "no token is asked for or sent")
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		channel string
		wantErr bool
	}{
		{channel: testChannel},
		{channel: "19:abc_DEF-1@thread.skype"},
		{channel: "", wantErr: true},
		{channel: "#alerts", wantErr: true},
		{channel: "19:abc@thread.v2", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.channel, func(t *testing.T) {
			t.Parallel()

			err := (&Config{Channel: tt.channel}).Validate()
			if tt.wantErr {
				require.ErrorIs(t, err, errBadChannel)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		deps    registry.Deps
		wantErr string
	}{
		{name: "without the teams section", deps: registry.Deps{}, wantErr: "the teams section is required"},
		{
			name:    "a client secret without a resolver",
			deps:    registry.Deps{TeamsBot: registry.TeamsBot{AppID: testAppID, TenantID: testTenantID, ClientSecret: testSecretRef}},
			wantErr: "no secrets resolver",
		},
		{
			name: "a service URL over plain http",
			deps: registry.Deps{
				Secrets:  secrets.NewDir(t.TempDir()),
				TeamsBot: registry.TeamsBot{AppID: testAppID, TenantID: testTenantID, ClientSecret: testSecretRef, ServiceURL: "http://smba.example.com/teams/"},
			},
			wantErr: "must use https",
		},
		{
			name: "a client secret",
			deps: registry.Deps{Secrets: secrets.NewDir(t.TempDir()), TeamsBot: registry.TeamsBot{AppID: testAppID, TenantID: testTenantID, ClientSecret: testSecretRef}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(t.Context(), "teams-thread", Config{Channel: testChannel}, tt.deps)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

// testSink returns a sink that posts to api as the test bot, with a fake
// clock and no conversation limit.
func testSink(t *testing.T, api *fakehttp.Server) (*Sink, *fakeClock) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, testSecretRef.Name), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, testSecretRef.Name, testSecretRef.Key), []byte("test-client-secret\n"), 0o600))

	s, err := newSink("teams-thread", Config{Channel: testChannel}, registry.Deps{
		Secrets:  secrets.NewDir(dir),
		HTTP:     api.Client(),
		TeamsBot: registry.TeamsBot{AppID: testAppID, TenantID: testTenantID, ClientSecret: testSecretRef, ServiceURL: api.URL() + "/teams/"},
	}, api.URL()+"/login/")
	require.NoError(t, err)

	clock := &fakeClock{now: testStart}
	s.now, s.wait = clock.Now, clock.Wait
	s.client.wait = clock.Wait
	s.client.limit = rate.Inf

	return s, clock
}

// testUpdate is an update of the investigation inv-1 in phase.
func testUpdate(phase output.Phase, r *report.Report) output.Update {
	return output.Update{
		InvestigationID: "inv-1",
		URL:             "https://sidekick.example.com/investigations/inv-1",
		Event:           event.Event{Title: "Checkout p99 above 2s", ReceivedAt: testStart.Add(-time.Minute)},
		Events:          1,
		Phase:           phase,
		Report:          r,
		Seq:             1,
	}
}

// testReport is a real issue with findings findings, each with one
// evidence link.
func testReport(findings int) *report.Report {
	r := &report.Report{
		Verdict:    report.TruePositive,
		Confidence: report.High,
		Reason:     "Errors rose after the 09:00 deploy.",
		Cause:      "A bad config in release 42.",
		Impact:     "2% of checkouts fail.",
		Fixes:      []report.Fix{{Title: "Roll back release 42"}},
		Checked:    []report.Check{{Source: "signoz", Status: report.CheckOK}},
	}

	for i := range findings {
		r.Findings = append(r.Findings, report.Finding{
			Claim:    fmt.Sprintf("finding %d", i+1),
			Evidence: []source.Evidence{{Kind: source.EvidenceKind("logs"), Title: "logs", URL: "https://signoz.example.com/logs/" + strconv.Itoa(i)}},
		})
	}

	return r
}

// shownRef is the ref of the main message at serviceURL showing seq,
// edited at editedAt (in Unix milliseconds), after announcing announced.
func shownRef(serviceURL string, seq int64, announced string, editedAt int64) output.Ref {
	data := map[string]string{
		dataServiceURL: serviceURL,
		dataChannel:    testChannel,
		dataThread:     testThread,
		dataActivity:   mainActivity,
		dataSeq:        strconv.FormatInt(seq, 10),
		dataEditedAt:   strconv.FormatInt(editedAt, 10),
	}

	if announced != "" {
		data[dataAnnounced] = announced
	}

	return output.Ref{Sink: "teams-thread", External: testChannel + "/" + mainActivity, Data: data}
}

// tokenAnswer is a token endpoint answer with token.
func tokenAnswer(token string) fakehttp.Response {
	return fakehttp.Response{Body: []byte(`{"token_type":"Bearer","expires_in":3599,"access_token":"` + token + `"}`)}
}

// requestTo returns the first request to path.
func requestTo(t *testing.T, api *fakehttp.Server, path string) fakehttp.Request {
	t.Helper()

	for _, req := range api.Requests() {
		if req.Path == path {
			return req
		}
	}

	t.Fatalf("no request to %s", path)

	return fakehttp.Request{}
}

// repliesRequests returns every request to path.
func repliesRequests(api *fakehttp.Server, path string) []fakehttp.Request {
	var out []fakehttp.Request

	for _, req := range api.Requests() {
		if req.Path == path {
			out = append(out, req)
		}
	}

	return out
}

// repliesTo returns the activities posted to path, in order.
func repliesTo(t *testing.T, api *fakehttp.Server, path string) []activity {
	t.Helper()

	var out []activity

	for _, req := range repliesRequests(api, path) {
		if req.Method == http.MethodPost {
			out = append(out, decodeActivity(t, req.Body, false))
		}
	}

	return out
}

// connectorPaths returns the paths of the requests that were not for
// tokens, in order.
func connectorPaths(api *fakehttp.Server) []string {
	var out []string

	for _, req := range api.Requests() {
		if req.Path != tokenPath {
			out = append(out, req.Path)
		}
	}

	return out
}

// decodeActivity decodes the activity in a request body, or in the body
// that starts a thread when newThread is true, checking its channel.
func decodeActivity(t *testing.T, body []byte, isNewThread bool) activity {
	t.Helper()

	if !isNewThread {
		var a activity
		require.NoError(t, json.Unmarshal(body, &a))

		return a
	}

	var nt newThread
	require.NoError(t, json.Unmarshal(body, &nt))
	require.True(t, nt.IsGroup)
	require.Equal(t, testChannel, nt.ChannelData.Channel.ID)
	require.Equal(t, testTenantID, nt.ChannelData.Tenant.ID)
	require.Equal(t, testTenantID, nt.TenantID)

	return nt.Activity
}

// runsText returns the text of every run in a's card, one run per line.
func runsText(a activity) string {
	var out string

	var walk func(elements []element)

	walk = func(elements []element) {
		for _, e := range elements {
			for _, r := range e.Inlines {
				out += r.Text
			}

			out += "\n"

			walk(e.Items)
		}
	}

	for _, att := range a.Attachments {
		walk(att.Content.Body)
	}

	return out
}
