package gitlabapi

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// linkPattern finds one link of a Link header, with its relation.
var linkPattern = regexp.MustCompile(`<([^>]*)>[^,<]*?rel="?([a-z]+)"?`)

// ListPage reads one page of a list: page number page, perPage items. It
// returns the items and the number of the next page, or 0 when this was the
// last.
func ListPage[T any](ctx context.Context, c *Client, path string, query url.Values, page, perPage int) ([]T, int, error) {
	q := maps.Clone(query)
	if q == nil {
		q = url.Values{}
	}

	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(perPage))

	var items []T

	header, err := c.Get(ctx, path, q, &items)
	if err != nil {
		return nil, 0, fmt.Errorf("get %s: %w", path, err)
	}

	return items, nextPage(header), nil
}

// nextPage returns the number of the next page a list answer points to, or
// 0. It reads the x-next-page header and, when that is empty, the page of
// the next link of the Link header. It never uses the host of a link.
func nextPage(h http.Header) int {
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("X-Next-Page"))); err == nil && n > 0 {
		return n
	}

	for _, link := range h.Values("Link") {
		for _, m := range linkPattern.FindAllStringSubmatch(link, -1) {
			if m[2] != "next" {
				continue
			}

			u, err := url.Parse(m[1])
			if err != nil {
				return 0
			}

			if n, err := strconv.Atoi(u.Query().Get("page")); err == nil && n > 0 {
				return n
			}
		}
	}

	return 0
}
