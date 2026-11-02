package githubpr

import "strings"

// changedLines returns how many lines differ between old and updated: lines
// removed plus lines added. It stops looking once the count passes limit
// and then reports false, so a rewrite of a large file costs no more than
// the limit allows.
func changedLines(old, updated string, limit int) (int, bool) {
	added, removed, within := lineCounts(old, updated, limit)
	if !within {
		return limit + 1, false
	}

	return added + removed, true
}

// lineCounts returns how many lines updated adds to old and how many it
// removes, from the length of the shortest edit script (Myers, 1986). It
// stops looking once their sum passes limit and then reports false. Two
// texts that differ only in the final newline count as one line removed and
// one added.
func lineCounts(old, updated string, limit int) (added, removed int, within bool) {
	if old == updated {
		return 0, 0, true
	}

	from, to := splitLines(old), splitLines(updated)

	d, within := editDistance(from, to, limit)
	if !within {
		return 0, 0, false
	}

	if d == 0 {
		return 1, 1, true
	}

	common := (len(from) + len(to) - d) / 2

	return len(to) - common, len(from) - common, true
}

// splitLines splits text into its lines. A final newline ends the last line
// and does not start another.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}

	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// editDistance returns the number of lines to delete from a and insert to
// get b. It reports false when the distance is more than limit.
func editDistance(a, b []string, limit int) (int, bool) {
	for len(a) > 0 && len(b) > 0 && a[0] == b[0] {
		a, b = a[1:], b[1:]
	}

	for len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] {
		a, b = a[:len(a)-1], b[:len(b)-1]
	}

	n, m := len(a), len(b)

	if n == 0 || m == 0 {
		return n + m, n+m <= limit
	}

	maxD := min(n+m, limit)
	offset := maxD + 1
	furthest := make([]int, 2*maxD+3)

	for d := 0; d <= maxD; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && furthest[offset+k-1] < furthest[offset+k+1]) {
				x = furthest[offset+k+1]
			} else {
				x = furthest[offset+k-1] + 1
			}

			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}

			furthest[offset+k] = x

			if x >= n && y >= m {
				return d, true
			}
		}
	}

	return limit + 1, false
}
