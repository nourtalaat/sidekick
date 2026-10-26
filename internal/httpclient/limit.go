package httpclient

import "io"

// LimitBody returns body with a size limit: once more than limit bytes
// have been read, every read fails with tooLarge. A plugin wraps the
// response bodies of a service with it, so a huge answer cannot make
// Sidekick read without end. Reading exactly limit bytes still ends with
// the body's own io.EOF. Closing the result closes body.
func LimitBody(body io.ReadCloser, limit int64, tooLarge error) io.ReadCloser {
	return &limitedBody{ReadCloser: body, left: limit, tooLarge: tooLarge}
}

// limitedBody fails a read once more than its limit has been read.
type limitedBody struct {
	io.ReadCloser

	left     int64
	tooLarge error
}

// Read implements io.Reader. It reads at most one byte past the limit, to
// tell a body of exactly limit bytes from a longer one.
func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, b.tooLarge
	}

	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}

	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)

	if b.left < 0 {
		return n, b.tooLarge
	}

	return n, err //nolint:wrapcheck // io.Reader callers compare err with io.EOF, so it must stay unwrapped.
}
