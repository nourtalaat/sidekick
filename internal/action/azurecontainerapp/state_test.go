package azurecontainerapp

import (
	"encoding/json"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v5"
	"github.com/stretchr/testify/require"
)

// entry makes a traffic entry. An empty name with latest false is an entry
// with no revision.
func entry(name string, weight int32, latest bool, label string) *armappcontainers.TrafficWeight {
	e := &armappcontainers.TrafficWeight{Weight: new(weight)}

	if name != "" {
		e.RevisionName = new(name)
	}

	if latest {
		e.LatestRevision = new(true)
	}

	if label != "" {
		e.Label = new(label)
	}

	return e
}

// listJSON writes a traffic list as the PATCH sends it.
func listJSON(t *testing.T, list []*armappcontainers.TrafficWeight) string {
	t.Helper()

	raw, err := json.Marshal(list)
	require.NoError(t, err)

	return string(raw)
}

func TestShift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		list []*armappcontainers.TrafficWeight
		dest destination
		want string
	}{
		{
			name: "the running entry is dropped and the destination added",
			list: []*armappcontainers.TrafficWeight{entry("a", 100, false, "")},
			dest: destination{revision: "b"},
			want: `[{"revisionName":"b","weight":100}]`,
		},
		{
			name: "a latest entry is dropped and the destination is named",
			list: []*armappcontainers.TrafficWeight{entry("", 100, true, "")},
			dest: destination{revision: "b"},
			want: `[{"revisionName":"b","weight":100}]`,
		},
		{
			name: "a labelled running entry stays with weight 0",
			list: []*armappcontainers.TrafficWeight{entry("a", 100, false, "prod")},
			dest: destination{revision: "b"},
			want: `[{"label":"prod","revisionName":"a","weight":0},{"revisionName":"b","weight":100}]`,
		},
		{
			name: "an entry of the destination is reused, with its label",
			list: []*armappcontainers.TrafficWeight{entry("a", 100, false, ""), entry("b", 0, false, "staging")},
			dest: destination{revision: "b"},
			want: `[{"label":"staging","revisionName":"b","weight":100}]`,
		},
		{
			name: "the destination is the latest revision",
			list: []*armappcontainers.TrafficWeight{entry("b", 100, false, "")},
			dest: destination{latest: true},
			want: `[{"latestRevision":true,"weight":100}]`,
		},
		{
			name: "a latest entry of weight 0 is reused",
			list: []*armappcontainers.TrafficWeight{entry("b", 100, false, ""), entry("", 0, true, "next")},
			dest: destination{latest: true},
			want: `[{"label":"next","latestRevision":true,"weight":100}]`,
		},
		{
			name: "a split is gathered on the destination",
			list: []*armappcontainers.TrafficWeight{entry("a", 60, false, ""), entry("b", 40, false, ""), entry("c", 0, false, "old")},
			dest: destination{revision: "c"},
			want: `[{"label":"old","revisionName":"c","weight":100}]`,
		},
		{
			name: "an entry of weight 0 without a label stays",
			list: []*armappcontainers.TrafficWeight{entry("a", 100, false, ""), entry("z", 0, false, "")},
			dest: destination{revision: "b"},
			want: `[{"revisionName":"z","weight":0},{"revisionName":"b","weight":100}]`,
		},
		{
			name: "a second entry of the destination is not given the weight again",
			list: []*armappcontainers.TrafficWeight{entry("a", 100, false, ""), entry("b", 0, false, "x"), entry("b", 0, false, "y")},
			dest: destination{revision: "b"},
			want: `[{"label":"x","revisionName":"b","weight":100},{"label":"y","revisionName":"b","weight":0}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			before := listJSON(t, tt.list)

			require.JSONEq(t, tt.want, listJSON(t, shift(tt.list, tt.dest)))
			require.Equal(t, before, listJSON(t, tt.list), "the list that was read is not changed")
		})
	}
}

func TestVersion(t *testing.T) {
	t.Parallel()

	base := func() live {
		return live{app: armappcontainers.ContainerApp{Properties: &armappcontainers.ContainerAppProperties{
			LatestRevisionName:      new("a2"),
			LatestReadyRevisionName: new("a2"),
			Configuration: &armappcontainers.Configuration{
				ActiveRevisionsMode: new(armappcontainers.ActiveRevisionsModeMultiple),
				Ingress:             &armappcontainers.Ingress{Traffic: []*armappcontainers.TrafficWeight{entry("a1", 100, false, "")}},
			},
		}}}
	}

	pinned, latest := &running{name: "a1"}, &running{name: "a1", latest: true}

	v := base().version(pinned)
	require.Regexp(t, `^pinned [0-9a-f]{32}$`, v)
	require.Equal(t, v, base().version(pinned), "the same app has the same version")
	require.Regexp(t, `^latest [0-9a-f]{32}$`, base().version(latest))
	require.Regexp(t, `^unknown [0-9a-f]{32}$`, base().version(nil))

	changes := map[string]func(l *live){
		"a weight":   func(l *live) { l.app.Properties.Configuration.Ingress.Traffic[0].Weight = new(int32(90)) },
		"a revision": func(l *live) { l.app.Properties.Configuration.Ingress.Traffic[0].RevisionName = new("a2") },
		"a label":    func(l *live) { l.app.Properties.Configuration.Ingress.Traffic[0].Label = new("x") },
		"an added entry": func(l *live) {
			l.app.Properties.Configuration.Ingress.Traffic = append(l.app.Properties.Configuration.Ingress.Traffic, entry("a2", 0, false, ""))
		},
		"the latest":       func(l *live) { l.app.Properties.LatestRevisionName = new("a3") },
		"the latest ready": func(l *live) { l.app.Properties.LatestReadyRevisionName = new("a1") },
		"the revisions mode": func(l *live) {
			l.app.Properties.Configuration.ActiveRevisionsMode = new(armappcontainers.ActiveRevisionsModeSingle)
		},
	}

	for name, change := range changes {
		l := base()
		change(&l)

		require.NotEqual(t, v, l.version(pinned), "%s changes the version", name)
	}
}

func TestModeOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version string
		want    string
		ok      bool
	}{
		{version: "latest abc", want: "latest", ok: true},
		{version: "pinned abc", want: "pinned", ok: true},
		{version: "unknown abc", want: "unknown"},
		{version: "abc"},
		{version: ""},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			t.Parallel()

			mode, ok := modeOf(tt.version)
			require.Equal(t, tt.want, mode)
			require.Equal(t, tt.ok, ok)
		})
	}
}
