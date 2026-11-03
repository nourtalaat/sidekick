package gitlabmr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
)

// blobPattern is the shape of the ID of a blob.
var blobPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var errBadBlob = errors.New("is not a blob ID")

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

	if !blobPattern.MatchString(blob) {
		return "", fmt.Errorf("%q %w", blob, errBadBlob)
	}

	text, err := readBlobText(ctx, m.client, params.Project, blob, "blob "+blob)
	if err != nil {
		return "", fmt.Errorf("read the blob: %w", err)
	}

	return text, nil
}
