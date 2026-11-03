package githubpr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// blobPattern is the shape of the SHA of a blob.
var blobPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var errBadBlob = errors.New("is not a blob SHA")

// ReadBlob implements action.FileReader. It reads the blob from a repository
// that limits.repos allows, with the action's own installation token.
func (p *PR) ReadBlob(ctx context.Context, raw json.RawMessage, blob string) (string, error) {
	params, err := decodeParams(raw)
	if err != nil {
		return "", fmt.Errorf("read the parameters: %w", err)
	}

	if !p.limits.allowsRepo(params.Repo) {
		return "", fmt.Errorf("repo %q is not one of limits.repos (%s): %w", params.Repo, strings.Join(p.limits.Repos, ", "), action.ErrLimit)
	}

	if !blobPattern.MatchString(blob) {
		return "", fmt.Errorf("%q %w", blob, errBadBlob)
	}

	owner, name, _ := strings.Cut(params.Repo, "/")

	data, _, err := p.client.Git.GetBlobRaw(ctx, owner, name, blob)
	if err != nil {
		return "", classify("read blob "+blob, err)
	}

	if len(data) > maxBaseFileBytes {
		return "", fmt.Errorf("blob %s %w", blob, errTooBig)
	}

	text := string(data)
	if !gitchange.ValidContent(text) {
		return "", fmt.Errorf("blob %s %w", blob, errNotText)
	}

	return text, nil
}
