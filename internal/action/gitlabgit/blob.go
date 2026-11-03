package gitlabgit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/nourtalaat/sidekick/internal/gitlabapi"
)

// blobPattern is the shape of the ID of a blob.
var blobPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ErrBadBlob is returned for a blob that is not the ID of a blob.
var ErrBadBlob = errors.New("is not a blob ID")

// ReadBlob returns the text of the blob with the ID in the project, read with
// the client's own token. The blob must be at most MaxFileBytes of UTF-8 text
// without NUL characters.
func ReadBlob(ctx context.Context, client *gitlabapi.Client, project, blob string) (string, error) {
	if !blobPattern.MatchString(blob) {
		return "", fmt.Errorf("%q %w", blob, ErrBadBlob)
	}

	text, err := ReadBlobText(ctx, client, project, blob, "blob "+blob)
	if err != nil {
		return "", fmt.Errorf("read the blob: %w", err)
	}

	return text, nil
}

// ReadBlobText reads a blob by its ID and returns its text. A blob that is
// too large or is not UTF-8 text is an error. name says what the blob is, for
// the errors.
func ReadBlobText(ctx context.Context, client *gitlabapi.Client, project, blob, name string) (string, error) {
	var b apiBlob

	api := gitlabapi.ProjectPath(project, "repository", "blobs", url.PathEscape(blob))

	if _, err := client.GetLimited(ctx, api, nil, &b, maxBodyBytes); err != nil {
		if errors.Is(err, gitlabapi.ErrResponseTooLarge) {
			return "", fmt.Errorf("%s %w", name, ErrTooBig)
		}

		return "", Classify("read "+name, err)
	}

	return decodeText(name, b.Encoding, b.Content, b.Size)
}
