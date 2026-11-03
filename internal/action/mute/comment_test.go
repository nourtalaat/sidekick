package mute_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

func TestCleanComment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "plain text", raw: "Planned database migration", want: "Planned database migration"},
		{name: "whitespace and line breaks become one space", raw: "  planned\n\nwork\t\tby  ops \r\n", want: "planned work by ops"},
		{name: "the longest", raw: strings.Repeat("é", mute.MaxCommentRunes), want: strings.Repeat("é", mute.MaxCommentRunes)},
		{name: "one too long", raw: strings.Repeat("é", mute.MaxCommentRunes+1), wantErr: true},
		{name: "empty", raw: "", wantErr: true},
		{name: "only whitespace", raw: " \n\t ", wantErr: true},
		{name: "a control character", raw: "bell\x07", wantErr: true},
		{name: "an escape", raw: "a\x1b[31mred", wantErr: true},
		{name: "a right-to-left override", raw: "abc\u202edef", wantErr: true},
		{name: "a zero width space", raw: "ab\u200bcd", wantErr: true},
		{name: "invalid UTF-8", raw: "ab\xffcd", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := mute.CleanComment(tt.raw)

			if tt.wantErr {
				require.ErrorIs(t, err, action.ErrLimit)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMarker(t *testing.T) {
	t.Parallel()

	const id = "0194f2c8-7d5e-7a41-9a3b-1c2d3e4f5a6b"

	other := "0194f2c8-7d5e-7a41-9a3b-ffffffffffff"
	comment := mute.WithMarker("Planned work", id)

	require.Equal(t, "Planned work\n[sidekick proposal "+id+"]", comment)
	require.True(t, mute.HasMarker(comment, id))
	require.False(t, mute.HasMarker(comment, other))

	t.Run("another proposal's marker in the text does not count", func(t *testing.T) {
		t.Parallel()

		cleaned, err := mute.CleanComment("[sidekick proposal " + other + "]")
		require.NoError(t, err)
		require.False(t, mute.HasMarker(mute.WithMarker(cleaned, id), other))
		require.False(t, mute.HasMarker(cleaned, other), "without the line break the model cannot write it")
	})

	t.Run("the marker must end the comment", func(t *testing.T) {
		t.Parallel()

		require.False(t, mute.HasMarker(comment+" edited", id))
		require.False(t, mute.HasMarker("Planned work", id))
	})
}
