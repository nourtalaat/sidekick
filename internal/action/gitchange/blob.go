package gitchange

import (
	"crypto/sha1" //nolint:gosec // Git names a blob by the SHA-1 of its content, which is what the snapshot compares.
	"encoding/hex"
	"fmt"
	"io"
)

// BlobSHA returns the SHA-1 that Git gives a blob with this content: the
// SHA-1 of "blob <length>", a NUL byte and the content.
func BlobSHA(content string) string {
	h := sha1.New() //nolint:gosec // see the import.

	_, _ = fmt.Fprintf(h, "blob %d\x00", len(content))
	_, _ = io.WriteString(h, content)

	return hex.EncodeToString(h.Sum(nil))
}
