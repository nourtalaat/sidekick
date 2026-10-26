package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxPages is the most pages one list call reads, so a call that filters
// what it reads cannot page through a whole repository's history.
const maxPages = 10

// Limits on the text a result keeps, in bytes.
const (
	// maxListMessageLen bounds a commit message in a list.
	maxListMessageLen = 1 << 10
	// maxMessageLen bounds the message of one commit.
	maxMessageLen = 16 << 10
	// maxTitleLen bounds a title or name from GitHub, such as a pull
	// request title.
	maxTitleLen = 512
)

// Texts of results that found nothing.
const (
	notFoundText = "Not found on GitHub: the repository, ref or path does not exist, or the GitHub App cannot read it."
	emptyText    = "The repository is empty."
)

// fetchPage reads one page of a list.
type fetchPage[T any] func(ctx context.Context, opts gh.ListOptions) ([]T, *gh.Response, error)

// filter says whether to keep an item of a list, and whether the list has
// no more items worth reading after it, for lists sorted newest first.
type filter[T any] func(item T) (keep, stop bool)

// collect reads a list page by page, keeping the items keep allows, until
// it has limit items, keep says stop, the pages end, or it has read
// maxPages pages. A nil keep keeps every item. more is true when items may
// be left unread.
func collect[T any](ctx context.Context, perPage, limit int, fetch fetchPage[T], keep filter[T]) (items []T, more bool, err error) {
	opts := gh.ListOptions{PerPage: perPage}

	for range maxPages {
		page, resp, err := fetch(ctx, opts)
		if err != nil {
			return nil, false, fmt.Errorf("read page %d: %w", max(opts.Page, 1), err)
		}

		for _, item := range page {
			ok, stop := true, false
			if keep != nil {
				ok, stop = keep(item)
			}

			if stop {
				return items, false, nil
			}

			if !ok {
				continue
			}

			if len(items) == limit {
				return items, true, nil
			}

			items = append(items, item)
		}

		if resp.NextPage == 0 {
			return items, false, nil
		}

		if len(items) == limit {
			return items, true, nil
		}

		opts.Page = resp.NextPage
	}

	return items, true, nil
}

// structured returns a result that holds v as JSON, with the evidence that
// has a link. When empty says the call found nothing, it returns a result
// with only NoData set instead.
func structured(v any, evidence []source.Evidence, truncated, empty bool) (source.Result, error) {
	if empty {
		return source.Result{NoData: true}, nil
	}

	data, err := json.Marshal(v)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	evidence = slices.DeleteFunc(evidence, func(e source.Evidence) bool { return e.URL == "" })
	if len(evidence) == 0 {
		evidence = nil
	}

	return source.Result{Structured: data, Evidence: evidence, Truncated: truncated}, nil
}

// failure turns the error of a call into what Call returns. A 404 and a
// 409 (an empty repository) are results with NoData set. Other refusals of
// the call itself, such as a 422 for a ref GitHub cannot resolve, are tool
// errors with GitHub's message. A 401 that remains after a new token, rate
// limits and server errors are failures of the source.
func failure(tool string, err error) (source.Result, error) {
	if resp, ok := errors.AsType[*gh.ErrorResponse](err); ok && resp.Response != nil {
		switch code := resp.Response.StatusCode; {
		case code == http.StatusNotFound:
			return source.Result{NoData: true, Text: notFoundText}, nil
		case code == http.StatusConflict:
			return source.Result{NoData: true, Text: emptyText}, nil
		case code >= http.StatusBadRequest && code < http.StatusInternalServerError && code != http.StatusUnauthorized:
			message := source.CutText(resp.Message, maxTitleLen)

			return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: fmt.Sprintf("GitHub answered %d: %s", code, message)})
		}
	}

	return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
}

// commitEvidence returns the evidence of one commit.
func commitEvidence(r repo, sha, url string, at time.Time) source.Evidence {
	return source.Evidence{Kind: source.EvidenceCommit, Title: r.String() + "@" + shortSHA(sha), URL: url, At: at}
}

// firstLines cuts a commit message to n bytes.
func firstLines(message string, n int) string {
	return source.CutText(strings.TrimSpace(message), n)
}

// shortSHA returns the first seven characters of a SHA, as GitHub shows it.
func shortSHA(sha string) string {
	const shortLen = 7

	if len(sha) <= shortLen {
		return sha
	}

	return sha[:shortLen]
}

// timeOf returns the time of a GitHub timestamp, in UTC, or the zero time.
func timeOf(ts *gh.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}

	return ts.UTC()
}
