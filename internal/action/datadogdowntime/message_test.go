package datadogdowntime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

func TestCleanMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		want     string
		wantText string
	}{
		{name: "a plain reason", raw: "Planned database migration", want: "Planned database migration"},
		{name: "white space becomes one space", raw: "Planned\n\tmigration  now", want: "Planned migration now"},
		{name: "the longest message", raw: strings.Repeat("a", mute.MaxCommentRunes), want: strings.Repeat("a", mute.MaxCommentRunes)},
		{name: "a message too long", raw: strings.Repeat("a", mute.MaxCommentRunes+1), wantText: "the most is 500"},
		{name: "an empty message", raw: "  ", wantText: "the comment is empty"},
		{name: "a control character", raw: "a\x1bb", wantText: "control or format character"},
		{name: "an @ handle", raw: "Planned work, tell @pagerduty-oncall", wantText: "holds an @"},
		{name: "an email handle", raw: "Ask ana@example.com", wantText: "holds an @"},
		{name: "the text of a marker", raw: "Planned work [sidekick proposal 1234]", wantText: markerText},
		{name: "the text of a marker in capitals", raw: "Planned work [SIDEKICK PROPOSAL 1234]", wantText: markerText},
		{name: "the text of a marker with odd spacing", raw: "Planned work [sidekick proposal 1234]", wantText: markerText},
		{name: "the start of a marker", raw: "[sidekick proposal", wantText: markerText},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := cleanMessage(tt.raw)

			if tt.wantText == "" {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)

				return
			}

			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.wantText)
		})
	}
}

func TestHasMarker(t *testing.T) {
	t.Parallel()

	marker := mute.Marker(proposalID)

	tests := []struct {
		name    string
		message string
		id      string
		want    bool
	}{
		{name: "as Execute writes it", message: "Planned work\n" + marker, id: proposalID, want: true},
		{name: "the line break came back as a space", message: "Planned work " + marker, id: proposalID, want: true},
		{name: "the line break came back as a tab", message: "Planned work\t" + marker, id: proposalID, want: true},
		{name: "a line break and a carriage return", message: "Planned work\r\n" + marker, id: proposalID, want: true},
		{name: "white space came back after the marker", message: "Planned work\n" + marker + " \n", id: proposalID, want: true},
		{name: "the marker is the whole message", message: marker, id: proposalID, want: true},
		{name: "the marker of another proposal", message: "Planned work\n" + marker, id: otherProposalID},
		{name: "no white space before the marker", message: "Planned work" + marker, id: proposalID},
		{name: "text after the marker", message: "Planned work\n" + marker + " and more", id: proposalID},
		{name: "the marker in the middle", message: "Planned " + marker + " work", id: proposalID},
		{name: "no marker", message: "Planned work", id: proposalID},
		{name: "an empty message", message: "", id: proposalID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, hasMarker(tt.message, tt.id))
		})
	}
}
