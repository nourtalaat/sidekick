package model

import (
	"net/http"
	"strconv"
	"time"
)

// Response headers that say how long to wait before retrying.
const (
	RetryAfterHeader   = "retry-after"
	RetryAfterMSHeader = "retry-after-ms"
)

// RetryAfter reads how long a provider asked to wait before the next call:
// retry-after-ms in milliseconds, else retry-after in seconds or as an
// HTTP date. It returns zero when resp is nil or asked for nothing.
// Connectors put the result in Error.RetryAfter.
func RetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}

	if ms, err := strconv.ParseFloat(resp.Header.Get(RetryAfterMSHeader), 64); err == nil && ms > 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}

	v := resp.Header.Get(RetryAfterHeader)
	if v == "" {
		return 0
	}

	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs <= 0 {
			return 0
		}

		return time.Duration(secs * float64(time.Second))
	}

	if at, err := http.ParseTime(v); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}

	return 0
}
