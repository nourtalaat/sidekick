package awsecs

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// activeBelow42 are the ACTIVE revisions of checkout below 42 in the fake.
var activeBelow42 = []int{41, 40, 38, 37, 36}

// repeat returns n copies of a history entry.
func repeat(entry string, n int) []string {
	return slices.Repeat([]string{entry}, n)
}

// newReader returns a reader of the fake service, for the service running
// checkout:42.
func newReader(t *testing.T, r *Rollback, back int) *reader {
	t.Helper()

	_, pos, err := r.parse(json.RawMessage(paramsBack1))
	require.NoError(t, err)

	client, err := r.client(t.Context(), pos)
	require.NoError(t, err)

	return &reader{
		client: client, pos: pos, family: "checkout", running: 42, back: back,
		active:          revisionSet(activeBelow42),
		taskDefinitions: map[string]string{},
	}
}

func TestReaderRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		history []string
		fake    func(*fakeECS)
		back    int
		// want is the revisions found, newest first.
		want         []int
		wantInactive []int
		wantRead     int
		wantNone     bool
		wantCut      bool
	}{
		{name: "the newest ACTIVE revision", history: []string{"42", "41", "40"}, back: 1, want: []int{41}, wantRead: 3},
		{name: "two ACTIVE revisions", history: []string{"42", "41", "40", "38"}, back: 2, want: []int{41, 40}, wantRead: 4},
		{name: "revisions that were never deployed are not found", history: []string{"42", "40", "37"}, back: 2, want: []int{40, 37}, wantRead: 3},
		{name: "an INACTIVE revision is listed apart", history: []string{"42", "39", "41"}, back: 1, want: []int{41}, wantInactive: []int{39}, wantRead: 3},
		{name: "a redeploy counts once", history: []string{"42", "41", "41", "40"}, back: 2, want: []int{41, 40}, wantRead: 4},
		{name: "the running revision and newer ones are skipped", history: []string{"42", "43", "42", "41"}, back: 1, want: []int{41}, wantRead: 4},
		{name: "the history ends before enough are found", history: []string{"42", "40"}, back: 2, want: []int{40}, wantRead: 2},
		{name: "deployments that did not succeed are not read", history: []string{"42", "41/STOPPED", "40/ROLLBACK_SUCCESSFUL"}, back: 1, wantRead: 1},
		{name: "no successful deployment, but others", history: []string{"42/STOPPED", "41/STOPPED"}, back: 1},
		{name: "no deployment at all", fake: func(f *fakeECS) { f.deployments = nil }, back: 1, wantNone: true},
		{
			name: "another family is ignored", back: 1, want: []int{41}, wantRead: 8,
			fake: func(f *fakeECS) {
				f.deployments = append([]fakeDeployment{{family: "checkout-worker", revision: 6, status: "SUCCESSFUL"}}, f.deployments...)
			},
		},
		{name: "a service revision ECS cannot describe is skipped", history: []string{"42", "41", "40"}, back: 1, want: []int{40}, wantRead: 3, fake: func(f *fakeECS) { f.deployments[1].unreadable = true }},
		{name: "the search stops at its bound", history: repeat("42", 400), back: 1, wantRead: 300, wantCut: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, nil)
			if tt.history != nil {
				f.deploy(t, tt.history...)
			}

			if tt.fake != nil {
				tt.fake(f)
			}

			h, err := newReader(t, r, tt.back).read(t.Context())
			require.NoError(t, err)

			var found []int
			for _, d := range h.found {
				found = append(found, d.revision)
			}

			require.Equal(t, tt.want, found)
			require.Equal(t, tt.wantInactive, h.inactive)
			require.Equal(t, tt.wantRead, h.read)
			require.Equal(t, tt.wantNone, h.none)
			require.Equal(t, tt.wantCut, h.cut)
		})
	}
}

func TestReaderReadsTheDeployment(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)

	h, err := newReader(t, r, 1).read(t.Context())
	require.NoError(t, err)
	require.Len(t, h.found, 1)

	finished := time.Date(2026, 5, 4, 9, 40, 0, 0, time.UTC)
	require.Equal(t, 41, h.found[0].revision)
	require.Equal(t, "sd-1", h.found[0].id)
	require.Equal(t, &finished, h.found[0].at)
	require.Equal(t, 1, f.count("ListServiceDeployments"))
	require.Equal(t, 1, f.count("DescribeServiceRevisions"))
}

func TestReaderPagesAndBatches(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)
	f.deploy(t, slices.Concat([]string{"42"}, repeat("42", 150), []string{"41"})...)

	h, err := newReader(t, r, 1).read(t.Context())
	require.NoError(t, err)
	require.Len(t, h.found, 1)
	require.Equal(t, 41, h.found[0].revision)

	require.Len(t, f.deploymentLists, 2)
	require.JSONEq(t, `{"cluster":"shop","service":"checkout","status":["SUCCESSFUL"],"maxResults":100}`, string(f.deploymentLists[0]))
	require.JSONEq(t, `{"cluster":"shop","service":"checkout","status":["SUCCESSFUL"],"maxResults":100,"nextToken":"100"}`, string(f.deploymentLists[1]))
	require.Equal(t, []int{20, 20, 20, 20, 20, 20, 20, 12}, f.revisionDescribes, "the service revisions are described 20 at a time, each once")
}

func TestReaderReturnsWhatECSRefuses(t *testing.T) {
	t.Parallel()

	for _, op := range []string{"ListServiceDeployments", "DescribeServiceRevisions"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()

			r, f := newTestAction(t, nil)
			f.failWith(op, 400, "AccessDeniedException")

			_, err := newReader(t, r, 1).read(t.Context())
			require.ErrorContains(t, err, "AccessDeniedException")
		})
	}
}
