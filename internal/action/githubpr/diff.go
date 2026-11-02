package githubpr

import "strings"

// changedLines returns how many lines differ between old and updated: lines
// removed plus lines added, which is the length of the shortest edit script
// (Myers, 1986). It stops looking once the count passes limit and then
// reports false, so a rewrite of a large file costs no more than the limit
// allows. Two texts that differ only in the final newline count as one line.
func changedLines(old, updated string, limit int) (int, bool) {
	if old == updated {
		return 0, true
	}

	n, within := editDistance(splitLines(old), splitLines(updated), limit)
	if within && n == 0 {
		n = 1
	}

	return n, within
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
