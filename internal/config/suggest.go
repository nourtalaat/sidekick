package config

import (
	"fmt"
	"slices"
)

// maxSuggestionDistance is the largest number of single-character edits
// between a misspelled name and a name we suggest instead.
const maxSuggestionDistance = 2

// didYouMean returns ` (did you mean "x"?)` for the candidate closest to
// name, or "" when no candidate is close enough. Ties go to the candidate
// that sorts first, so the suggestion does not depend on map order.
func didYouMean(name string, candidates []string) string {
	best, bestDistance := "", maxSuggestionDistance+1

	for _, c := range sortedCopy(candidates) {
		if c == name {
			continue
		}

		d := editDistance(name, c)
		if d < bestDistance && d < len([]rune(name)) {
			best, bestDistance = c, d
		}
	}

	if best == "" {
		return ""
	}

	return fmt.Sprintf(" (did you mean %q?)", best)
}

// editDistance returns the Levenshtein distance between a and b: the number
// of single-character insertions, deletions and substitutions that turn a
// into b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)

	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}

	cur := make([]int, len(rb)+1)
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}

			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}

		prev, cur = cur, prev
	}

	return prev[len(rb)]
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)

	return out
}
