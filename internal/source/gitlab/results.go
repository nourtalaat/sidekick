package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/source"
)

// maxPages is the most pages one list call reads, so a call that filters
// what it reads cannot page through a whole project's history.
const maxPages = 10

// Limits on the text a result keeps, in bytes.
const (
	// maxListMessageLen bounds a commit message in a list.
	maxListMessageLen = 1 << 10
	// maxTitleLen bounds a title or name from GitLab, such as a merge
	// request title, and an error message.
	maxTitleLen = 512
)

// notFoundText is the text of a result for something GitLab does not have.
const notFoundText = "Not found on GitLab: the project, ref or path does not exist, or the token cannot read it."

// fetchPage reads page number page of a list. It returns the items and the
// number of the next page, or 0 when there is none.
type fetchPage[T any] func(ctx context.Context, page int) (items []T, next int, err error)

// filter says whether to keep an item of a list, and whether the list has
// no more items worth reading after it, for lists sorted newest first.
type filter[T any] func(item T) (keep, stop bool)

// collect reads a list page by page, keeping the items keep allows, until
// it has limit items, keep says stop, the pages end, or it has read
// maxPages pages. A nil keep keeps every item. more is true when items may
// be left unread.
func collect[T any](ctx context.Context, limit int, fetch fetchPage[T], keep filter[T]) (items []T, more bool, err error) {
	page := 1

	for range maxPages {
		batch, next, err := fetch(ctx, page)
		if err != nil {
			return nil, false, fmt.Errorf("read page %d: %w", page, err)
		}

		for _, item := range batch {
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

		if next == 0 {
			return items, false, nil
		}

		if len(items) == limit {
			return items, true, nil
		}

		page = next
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

// failure turns the error of a call into what Call returns. A 404 is a
// result with NoData set. Other refusals of the call itself, such as a 400
// for a ref GitLab cannot resolve or a 403 for something the token's role
// may not read, are tool errors with GitLab's message. A refused token (401),
// rate limits, server errors and redirects the client does not follow are
// failures of the source.
func failure(tool string, err error) (source.Result, error) {
	if se, ok := errors.AsType[*gitlabapi.StatusError](err); ok {
		switch code := se.Code; {
		case code == http.StatusNotFound:
			return source.Result{NoData: true, Text: notFoundText}, nil
		case code >= http.StatusBadRequest && code < http.StatusInternalServerError &&
			code != http.StatusUnauthorized && code != http.StatusTooManyRequests:
			return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: se.Error()})
		}
	}

	return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
}

// commitEvidence returns the evidence of one commit.
func commitEvidence(project, sha, link string, at time.Time) source.Evidence {
	return source.Evidence{Kind: source.EvidenceCommit, Title: project + "@" + shortSHA(sha), URL: safeURL(link), At: at}
}

// safeURL returns link when it is an absolute http or https URL, and the
// empty string otherwise. Links come from GitLab, and only web pages are
// worth showing to a person.
func safeURL(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}

	return link
}

// firstLines cuts a commit message to n bytes.
func firstLines(message string, n int) string {
	return source.CutText(strings.TrimSpace(message), n)
}

// shortSHA returns the first eight characters of a SHA, as GitLab shows it.
func shortSHA(sha string) string {
	const shortLen = 8

	if len(sha) <= shortLen {
		return sha
	}

	return sha[:shortLen]
}

// utc returns t in UTC, or the zero time.
func utc(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}

	return t.UTC()
}
