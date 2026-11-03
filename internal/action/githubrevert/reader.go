package githubrevert

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

// ReadBlob implements action.FileReader. It reads the blob from a
// repository that limits.repos allows, with the action's own installation
// token.
func (r *Revert) ReadBlob(ctx context.Context, raw json.RawMessage, blob string) (string, error) {
	params, err := decodeParams(raw)
	if err != nil {
		return "", fmt.Errorf("read the parameters: %w", err)
	}

	if !githubgit.AllowsRepo(r.limits.Repos, params.Repo) {
		return "", fmt.Errorf("repo %q is not one of limits.repos (%s): %w", params.Repo, strings.Join(r.limits.Repos, ", "), action.ErrLimit)
	}

	owner, name, _ := strings.Cut(params.Repo, "/")

	text, err := githubgit.ReadBlob(ctx, r.client, owner, name, blob)
	if err != nil {
		return "", fmt.Errorf("read the blob: %w", err)
	}

	return text, nil
}
