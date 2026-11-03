package gitlabmr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// ReadBlob implements action.FileReader. It reads the blob from a project
// that limits.projects allows, with the action's own token, and never
// writes.
func (m *MR) ReadBlob(ctx context.Context, raw json.RawMessage, blob string) (string, error) {
	params, err := decodeParams(raw)
	if err != nil {
		return "", fmt.Errorf("read the parameters: %w", err)
	}

	if !m.limits.allowsProject(params.Project) {
		return "", fmt.Errorf("project %q is not one of limits.projects (%s): %w", params.Project, strings.Join(m.limits.Projects, ", "), action.ErrLimit)
	}

	text, err := gitlabgit.ReadBlob(ctx, m.client, params.Project, blob)
	if err != nil {
		return "", fmt.Errorf("read the blob: %w", err)
	}

	return text, nil
}
