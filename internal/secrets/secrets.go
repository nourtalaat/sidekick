// Package secrets resolves secret references to the files that hold them.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// DefaultDir is where the Helm chart mounts secrets. The secrets.dir setting
// overrides it, for example with /run/secrets for Docker and Podman.
const DefaultDir = "/var/run/secrets/sidekick"

// MaxSize is the largest secret file Dir reads. It matches the Kubernetes
// limit on the size of a Secret.
const MaxSize = 1 << 20

// ErrBadRef is returned for a reference whose name or key is not a single
// plain path element, so it cannot reach files outside its own secret.
var ErrBadRef = errors.New("invalid secret reference")

// ErrTooLarge is returned when a secret file is larger than MaxSize.
var ErrTooLarge = errors.New("secret file too large")

// Dir resolves a reference by reading the file <dir>/<name>/<key>. It reads
// the file on every call, so a rotated secret is used without a restart.
type Dir struct {
	dir string
}

var _ registry.Secrets = Dir{}

// Resolve returns the content of the file that holds ref, unchanged. Use it
// for binary keys such as HMAC secrets; use ResolveText for tokens, URLs and
// passwords. Symbolic links are followed only while they stay inside the
// secrets directory, which is how Kubernetes lays out mounted Secrets.
func (d Dir) Resolve(ctx context.Context, ref registry.SecretRef) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("resolve secret %s/%s: %w", ref.Name, ref.Key, err)
	}

	if err := CheckRef(ref); err != nil {
		return nil, fmt.Errorf("secret %q key %q: %w: %w", ref.Name, ref.Key, ErrBadRef, err)
	}

	value, err := d.read(path.Join(ref.Name, ref.Key))
	if err != nil {
		return nil, fmt.Errorf("read secret %s/%s: %w", ref.Name, ref.Key, err)
	}

	return value, nil
}

func (d Dir) read(name string) (value []byte, err error) {
	root, err := os.OpenRoot(d.dir)
	if err != nil {
		return nil, fmt.Errorf("open secrets dir: %w", err)
	}
	defer closeInto(&err, root, "close secrets dir")

	f, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer closeInto(&err, f, "close file")

	value, err = io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	if len(value) > MaxSize {
		return nil, fmt.Errorf("more than %d bytes: %w", MaxSize, ErrTooLarge)
	}

	return value, nil
}

// NewDir returns a Dir that reads secrets below dir, or below DefaultDir when
// dir is empty.
func NewDir(dir string) Dir {
	if dir == "" {
		dir = DefaultDir
	}

	return Dir{dir: dir}
}

// CheckRef reports whether ref can name a secret file: its name and key must
// each be one plain path element. It joins one error per bad field, each
// starting with the field's key, so config validation can point at it.
// Resolve wraps these errors with ErrBadRef.
func CheckRef(ref registry.SecretRef) error {
	var errs []error

	for _, f := range []struct{ key, value string }{{"name", ref.Name}, {"key", ref.Key}} {
		switch {
		case f.value == "":
			errs = append(errs, fmt.Errorf("%s: required", f.key))
		case !plainElement(f.value):
			errs = append(errs, fmt.Errorf(`%s: %q must not be "." or ".." or contain "/" or "\"`, f.key, f.value))
		}
	}

	return errors.Join(errs...)
}

// ResolveText resolves ref through s and removes trailing "\r" and "\n"
// characters, which a Secret made from a file often ends with. Use it for
// tokens, URLs and passwords, where a trailing newline would break a header
// or a connection string. Newlines inside the value are kept. Use
// Resolve directly for binary keys, whose bytes must stay exact.
func ResolveText(ctx context.Context, s registry.Secrets, ref registry.SecretRef) (string, error) {
	value, err := s.Resolve(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve text secret: %w", err)
	}

	return strings.TrimRight(string(value), "\r\n"), nil
}

// closeInto closes c and adds a failure to *err.
func closeInto(err *error, c io.Closer, what string) {
	if cerr := c.Close(); cerr != nil {
		*err = errors.Join(*err, fmt.Errorf("%s: %w", what, cerr))
	}
}

// plainElement reports whether s names one entry in a directory: not empty,
// not "." or "..", and with no path separator.
func plainElement(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}
