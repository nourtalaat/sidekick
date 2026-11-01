package api

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// timeCursor is where a page of a list ordered by a time and an ID starts:
// after the row with that time and ID.
type timeCursor struct {
	at time.Time
	id uuid.UUID
}

// String encodes c for the nextCursor of a page.
func (c timeCursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.at.UnixMicro(), 10) + "." + c.id.String()))
}

// parseTimeCursor decodes what timeCursor.String encoded.
func parseTimeCursor(s string) (timeCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return timeCursor{}, errBadCursor
	}

	parts := strings.SplitN(string(raw), ".", cursorParts)
	if len(parts) != cursorParts {
		return timeCursor{}, errBadCursor
	}

	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return timeCursor{}, errBadCursor
	}

	id, err := uuid.Parse(parts[1])
	if err != nil {
		return timeCursor{}, errBadCursor
	}

	return timeCursor{at: time.UnixMicro(micros).UTC(), id: id}, nil
}
