package ticket

import (
	"errors"
	"maps"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
)

// The tickets of the tests.
const (
	testSink        = "ticket-comment"
	testInvestigate = "0199c9d2-7f00-7000-8000-000000000001"
	testLinkPrefix  = "https://tickets.example.com/browse/"
)

// errTracker is a failure of the fake ticket system.
var errTracker = errors.New("HTTP status 503")

var (
	ops123 = Issue{ID: "id-ops-123", Key: "OPS-123", Scope: "OPS"}
	ops7   = Issue{ID: "id-ops-7", Key: "OPS-7", Scope: "OPS"}
	pay9   = Issue{ID: "id-pay-9", Key: "PAY-9", Scope: "PAY"}
)

func TestAccepts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase output.Phase
		want  bool
	}{
		{phase: output.PhaseQueued},
		{phase: output.PhaseRunning},
		{phase: output.PhaseFinal, want: true},
		{phase: output.PhaseFollowUp},
		{phase: output.PhaseWatcher},
	}

	for _, tt := range tests {
		t.Run(string(tt.phase), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, NewSink(Options{}).Accepts(tt.phase))
		})
	}
}

func TestFindTheRelatedTicket(t *testing.T) {
	t.Parallel()

	created := func(status action.ProposalStatus, url string) action.Proposal {
		return action.Proposal{Status: status, Result: &action.Result{Links: []action.Link{{Title: "Ticket", URL: url}}}}
	}

	tests := []struct {
		name       string
		labels     map[string]string
		links      []event.Link
		proposals  []action.Proposal
		setupMocks func(tr *MockTracker)
		want       Issue
		wantFound  bool
		wantErr    error
	}{
		{
			name:   "a key in a label",
			labels: map[string]string{"ticket": "ops-123"},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-123").Return(ops123, nil)
			},
			want:      ops123,
			wantFound: true,
		},
		{
			name:   "a link in a label",
			labels: map[string]string{"ticket": " " + testLinkPrefix + "OPS-123 "},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-123").Return(ops123, nil)
			},
			want:      ops123,
			wantFound: true,
		},
		{
			name:  "a ticket link in the alert",
			links: []event.Link{{Title: "Runbook", URL: "https://runbooks.example.com/checkout"}, {Title: "Ticket", URL: testLinkPrefix + "OPS-7"}},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-7").Return(ops7, nil)
			},
			want:      ops7,
			wantFound: true,
		},
		{
			name:      "a ticket Sidekick created",
			proposals: []action.Proposal{created(action.StatusFailed, testLinkPrefix+"OPS-1"), created(action.StatusSucceeded, testLinkPrefix+"OPS-7")},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-7").Return(ops7, nil)
			},
			want:      ops7,
			wantFound: true,
		},
		{
			name:      "a label comes before a link and a created ticket",
			labels:    map[string]string{"ticket": "OPS-123"},
			links:     []event.Link{{URL: testLinkPrefix + "OPS-7"}},
			proposals: []action.Proposal{created(action.StatusSucceeded, testLinkPrefix+"OPS-8")},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-123").Return(ops123, nil)
			},
			want:      ops123,
			wantFound: true,
		},
		{
			name:      "none found",
			labels:    map[string]string{"service": "checkout-api", "ticket": "not a ticket"},
			links:     []event.Link{{URL: "https://grafana.example.com/d/abc"}},
			proposals: []action.Proposal{{Status: action.StatusSucceeded}},
		},
		{
			name:   "a ticket outside the scopes is not looked up",
			labels: map[string]string{"ticket": "SEC-1"},
			links:  []event.Link{{URL: testLinkPrefix + "SEC-2"}},
		},
		{
			name:   "a ticket that moved out of the scopes is skipped",
			labels: map[string]string{"ticket": "OPS-123"},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-123").Return(Issue{ID: "id-ops-123", Key: "SEC-4", Scope: "SEC"}, nil)
			},
		},
		{
			name:   "a missing ticket gives way to the next one",
			labels: map[string]string{"ticket": "OPS-404"},
			links:  []event.Link{{URL: testLinkPrefix + "PAY-9"}},
			setupMocks: func(tr *MockTracker) {
				gomock.InOrder(
					tr.EXPECT().Issue(gomock.Any(), "OPS-404").Return(Issue{}, ErrNotFound),
					tr.EXPECT().Issue(gomock.Any(), "PAY-9").Return(pay9, nil),
				)
			},
			want:      pay9,
			wantFound: true,
		},
		{
			name: "at most three lookups",
			links: []event.Link{
				{URL: testLinkPrefix + "OPS-1"}, {URL: testLinkPrefix + "OPS-2"}, {URL: testLinkPrefix + "OPS-3"}, {URL: testLinkPrefix + "OPS-4"},
			},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), gomock.Any()).Return(Issue{}, ErrNotFound).Times(maxLookups)
			},
		},
		{
			name:   "the same ticket is looked up once",
			labels: map[string]string{"ticket": "OPS-404", "linear": "OPS-404"},
			links:  []event.Link{{URL: testLinkPrefix + "OPS-404"}},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-404").Return(Issue{}, ErrNotFound)
			},
		},
		{
			name:   "a failed lookup",
			labels: map[string]string{"ticket": "OPS-123"},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-123").Return(Issue{}, errTracker)
			},
			wantErr: errTracker,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := fakeTracker(t)
			if tt.setupMocks != nil {
				tt.setupMocks(tr)
			}

			s := NewSink(Options{Name: testSink, Tracker: tr, Labels: []string{"ticket", "linear"}, Scopes: []string{"ops", "PAY"}})

			u := finalUpdate()
			u.Event.Labels, u.Event.Links, u.Proposals = tt.labels, tt.links, tt.proposals

			got, found, err := s.find(t.Context(), u)
			require.ErrorIs(t, err, tt.wantErr)
			require.Equal(t, tt.wantFound, found)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestPublish(t *testing.T) {
	t.Parallel()

	posted := &output.Ref{Sink: testSink, External: "OPS-123:c1", Data: map[string]string{
		dataIssueID: "id-ops-123", dataIssueKey: "OPS-123", dataComment: "c1", dataSeq: "4",
	}}

	tests := []struct {
		name       string
		previous   *output.Ref
		seq        int64
		labels     map[string]string
		setupMocks func(tr *MockTracker)
		want       output.Ref
		wantErr    error
	}{
		{
			name:   "the first final comments on the related ticket",
			seq:    4,
			labels: map[string]string{"ticket": "OPS-123"},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-123").Return(ops123, nil)
				tr.EXPECT().CreateComment(gomock.Any(), ops123, commentWithKey(0)).Return("c1", nil)
			},
			want: *posted,
		},
		{
			name: "no related ticket posts nothing",
			seq:  4,
			want: output.Ref{Sink: testSink, Data: map[string]string{dataSeq: "4"}},
		},
		{
			name:     "a ref without a comment looks for the ticket again",
			seq:      6,
			previous: &output.Ref{Sink: testSink, Data: map[string]string{dataSeq: "4"}},
			labels:   map[string]string{"ticket": "OPS-123"},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-123").Return(ops123, nil)
				tr.EXPECT().CreateComment(gomock.Any(), ops123, commentWithKey(0)).Return("c1", nil)
			},
			want: refWith(*posted, map[string]string{dataSeq: "6"}),
		},
		{
			name:     "a newer final edits the comment",
			seq:      6,
			previous: posted,
			labels:   map[string]string{"ticket": "OPS-999"},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().UpdateComment(gomock.Any(), Issue{ID: "id-ops-123", Key: "OPS-123"}, "c1", commentWithKey(0)).Return(nil)
			},
			want: refWith(*posted, map[string]string{dataSeq: "6"}),
		},
		{
			name:     "an older update is dropped",
			seq:      4,
			previous: posted,
			want:     *posted,
		},
		{
			name:     "a deleted comment is posted again with a new idempotency key",
			seq:      6,
			previous: posted,
			setupMocks: func(tr *MockTracker) {
				issue := Issue{ID: "id-ops-123", Key: "OPS-123"}
				tr.EXPECT().UpdateComment(gomock.Any(), issue, "c1", commentWithKey(0)).Return(ErrNotFound)
				tr.EXPECT().CreateComment(gomock.Any(), issue, commentWithKey(1)).Return("c2", nil)
			},
			want: output.Ref{Sink: testSink, External: "OPS-123:c2", Data: map[string]string{
				dataIssueID: "id-ops-123", dataIssueKey: "OPS-123", dataComment: "c2", dataSeq: "6", dataGeneration: "1",
			}},
		},
		{
			name:   "a failed comment",
			seq:    4,
			labels: map[string]string{"ticket": "OPS-123"},
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().Issue(gomock.Any(), "OPS-123").Return(ops123, nil)
				tr.EXPECT().CreateComment(gomock.Any(), ops123, gomock.Any()).Return("", errTracker)
			},
			wantErr: errTracker,
		},
		{
			name:     "a failed edit",
			seq:      6,
			previous: posted,
			setupMocks: func(tr *MockTracker) {
				tr.EXPECT().UpdateComment(gomock.Any(), gomock.Any(), "c1", gomock.Any()).Return(errTracker)
			},
			wantErr: errTracker,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := fakeTracker(t)
			if tt.setupMocks != nil {
				tt.setupMocks(tr)
			}

			s := NewSink(Options{Name: testSink, Tracker: tr, Labels: []string{"ticket"}, Scopes: []string{"OPS"}})

			u := finalUpdate()
			u.Seq, u.Previous, u.Event.Labels = tt.seq, tt.previous, tt.labels

			got, err := s.Publish(t.Context(), u)
			require.ErrorIs(t, err, tt.wantErr)

			if tt.wantErr == nil {
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value  string
		want   string
		wantOK bool
	}{
		{value: "OPS-123", want: "OPS-123", wantOK: true},
		{value: "ops-1", want: "OPS-1", wantOK: true},
		{value: "MY_PROJ2-42", want: "MY_PROJ2-42", wantOK: true},
		{value: "OPS-0"},
		{value: "OPS-"},
		{value: "1OPS-3"},
		{value: "OPS 3"},
		{value: "OPS-12 and more"},
		{value: "https://tickets.example.com/browse/OPS-1"},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()

			got, ok := Key(tt.value)
			require.Equal(t, tt.wantOK, ok)

			if tt.wantOK {
				require.Equal(t, tt.want, got)
			}
		})
	}
}

// fakeTracker returns a mock tracker whose links are testLinkPrefix and a
// ticket key.
func fakeTracker(t *testing.T) *MockTracker {
	t.Helper()

	tr := NewMockTracker(gomock.NewController(t))
	tr.EXPECT().KeyFromURL(gomock.Any()).DoAndReturn(func(rawURL string) (string, bool) {
		rest, ok := strings.CutPrefix(rawURL, testLinkPrefix)
		if !ok {
			return "", false
		}

		return Key(rest)
	}).AnyTimes()

	return tr
}

// commentWithKey matches a comment with the idempotency key of comment
// number generation of the test investigation.
func commentWithKey(generation int) gomock.Matcher {
	want := "sidekick:" + testSink + ":" + testInvestigate + ":" + strconv.Itoa(generation)

	return gomock.Cond(func(c Comment) bool {
		return c.IdempotencyKey == want && len(c.Doc.Blocks) > 0
	})
}

// refWith returns r with the data in changes set.
func refWith(r output.Ref, changes map[string]string) output.Ref {
	r.Data = maps.Clone(r.Data)
	maps.Copy(r.Data, changes)

	return r
}

// finalUpdate is a final update with a short report.
func finalUpdate() output.Update {
	return output.Update{
		InvestigationID: testInvestigate,
		Phase:           output.PhaseFinal,
		Event:           event.Event{Title: "checkout-api p99 latency above 2s"},
		Report: &report.Report{
			Verdict:    report.TruePositive,
			Confidence: report.High,
			Reason:     "A deploy at 10:02 doubled the database calls.",
			StopReason: report.StopSubmitted,
		},
	}
}
