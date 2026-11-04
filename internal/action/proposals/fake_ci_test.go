package proposals

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// ciNote is a note action whose result is a pull request, like a Git
// action's. pr-note is the same without the CI reader, like the actions that
// open monitor pull requests.
type ciNote struct {
	*fakeNote
}

// ciNoteReader is a ciNote that also reads CI.
type ciNoteReader struct {
	ciNote
}

var (
	_ action.Action      = ciNote{}
	_ gitchange.CIReader = ciNoteReader{}
)

func newCINote(name string, reads bool) action.Action {
	n := ciNote{fakeNote: newFakeNote(name)}
	if reads {
		return ciNoteReader{ciNote: n}
	}

	return n
}

func (n ciNote) Execute(ctx context.Context, inv action.Invocation) (action.Result, error) {
	res, err := n.fakeNote.Execute(ctx, inv)
	if err != nil {
		return res, fmt.Errorf("run the note: %w", err)
	}

	res.PullRequest = &action.PullRequestRef{Number: 12, HeadSHA: "4f2c1a9"}

	return res, nil
}

func (ciNoteReader) ReadCI(context.Context, json.RawMessage, action.PullRequestRef) (gitchange.CI, error) {
	return gitchange.CI{State: gitchange.CIPending}, nil
}

func (ciNoteReader) CIReportTimeout() time.Duration { return time.Hour }

func (ciNoteReader) CIHost() string { return "github" }
