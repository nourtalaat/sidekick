package githubgit

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// MaxFileBytes is the largest file an action reads from a repository.
const MaxFileBytes = 256 << 10

var (
	// ErrBadBlob is returned for a blob that is not the SHA of a blob.
	ErrBadBlob = errors.New("is not a blob SHA")
	// ErrTooBig is returned for a file larger than MaxFileBytes.
	ErrTooBig = fmt.Errorf("is larger than %d bytes", MaxFileBytes)
	// ErrNotText is returned for a file that is not UTF-8 text without NUL
	// characters.
	ErrNotText = errors.New("is not a UTF-8 text file")
)

// blobPattern is the shape of the SHA of a blob.
var blobPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ReadBlob returns the text of the blob with the SHA in the repository, read
// with the client's own token. The blob must be at most MaxFileBytes of UTF-8
// text without NUL characters.
func ReadBlob(ctx context.Context, client *gh.Client, owner, repo, blob string) (string, error) {
	if !blobPattern.MatchString(blob) {
		return "", fmt.Errorf("%q %w", blob, ErrBadBlob)
	}

	data, _, err := client.Git.GetBlobRaw(ctx, owner, repo, blob)
	if err != nil {
		return "", Classify("read blob "+blob, err)
	}

	if len(data) > MaxFileBytes {
		return "", fmt.Errorf("blob %s %w", blob, ErrTooBig)
	}

	text := string(data)
	if !gitchange.ValidContent(text) {
		return "", fmt.Errorf("blob %s %w", blob, ErrNotText)
	}

	return text, nil
}
