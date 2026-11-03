package gitchange

import (
	"slices"
	"strings"
)

// The kinds of a line of a line diff.
const (
	LineContext = "context"
	LineAdded   = "added"
	LineRemoved = "removed"
)

// The ways a change can touch the newline that ends a file, for
// Diff.FinalNewline.
const (
	NewlineAdded   = "added"
	NewlineRemoved = "removed"
)

// Line is one line of a line diff.
type Line struct {
	// Kind is LineContext, LineAdded or LineRemoved.
	Kind string
	// Old and New are the 1-based numbers of the line in the old and the new
	// text. A removed line has no New number, an added line no Old number;
	// the missing number is 0.
	Old, New int
	// Text is the line without its line break. A carriage return before the
	// line break is part of the text.
	Text string
}

// Diff is the difference between two texts, line by line.
type Diff struct {
	// Lines holds every line of both texts, in order.
	Lines []Line
	// FinalNewline says that the new text has a newline at its end where the
	// old text had none (NewlineAdded), or the other way round
	// (NewlineRemoved). It is empty when both are alike or one text is empty.
	FinalNewline string
}

// LineDiff returns the lines that turn old into updated, from the shortest
// edit script (Myers, 1986), so the lines it adds and removes are the counts
// LineCounts gives. Lines are split at "\n" only. Within a run of changes the
// removed lines come before the added ones. Two texts that differ only in the
// final newline give the last line twice, removed and then added. It reports
// false when more than limit lines would be added and removed together.
func LineDiff(old, updated string, limit int) (Diff, bool) {
	from, to := splitLines(old), splitLines(updated)
	diff := Diff{FinalNewline: finalNewline(old, updated)}

	if old != updated && slices.Equal(from, to) {
		last := len(from)

		diff.Lines = contextLines(from[:last-1], 0, 0)
		diff.Lines = append(diff.Lines,
			Line{Kind: LineRemoved, Old: last, Text: from[last-1]},
			Line{Kind: LineAdded, New: last, Text: to[last-1]})

		return diff, true
	}

	prefix := commonPrefix(from, to)
	suffix := commonSuffix(from[prefix:], to[prefix:])
	a, b := from[prefix:len(from)-suffix], to[prefix:len(to)-suffix]

	steps, ok := editScript(a, b, limit)
	if !ok {
		return Diff{}, false
	}

	diff.Lines = contextLines(from[:prefix], 0, 0)
	diff.Lines = append(diff.Lines, changedLines(steps, a, b, prefix)...)
	diff.Lines = append(diff.Lines, contextLines(from[len(from)-suffix:], len(from)-suffix, len(to)-suffix)...)

	return diff, true
}

// Hunks returns the lines that changed with n lines of context on each side,
// in order. Two changes whose context meets share it. The numbers of the
// lines tell where lines were left out.
func (d Diff) Hunks(n int) []Line {
	keep := make([]bool, len(d.Lines))

	for i, l := range d.Lines {
		if l.Kind == LineContext {
			continue
		}

		for j := max(0, i-n); j <= min(len(d.Lines)-1, i+n); j++ {
			keep[j] = true
		}
	}

	var out []Line

	for i, l := range d.Lines {
		if keep[i] {
			out = append(out, l)
		}
	}

	return out
}

// step is one move of an edit script.
type step int

const (
	stepEqual step = iota
	stepDelete
	stepInsert
)

// editScript returns the moves that turn a into b, or false when it takes
// more than limit deletions and insertions. It keeps the furthest-reaching
// points of every round, so memory grows with the square of the distance,
// which the callers bound by limit.
func editScript(a, b []string, limit int) ([]step, bool) {
	n, m := len(a), len(b)

	switch {
	case n == 0 && m == 0:
		return nil, true
	case n+m > limit && (n == 0 || m == 0):
		return nil, false
	case m == 0:
		return slices.Repeat([]step{stepDelete}, n), true
	case n == 0:
		return slices.Repeat([]step{stepInsert}, m), true
	}

	maxD := min(n+m, limit)
	offset := maxD + 1
	furthest := make([]int, 2*maxD+3)
	trace := make([][]int, 0, maxD+1)

	for d := 0; d <= maxD; d++ {
		trace = append(trace, slices.Clone(furthest[offset-d-1:offset+d+2]))

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
				return walkBack(trace, n, m, d), true
			}
		}
	}

	return nil, false
}

// walkBack turns the points of every round into the moves of the script, from
// the end of both texts back to the start.
func walkBack(trace [][]int, n, m, distance int) []step {
	var reversed []step

	x, y := n, m

	for d := distance; d > 0; d-- {
		k := x - y
		at := func(k int) int { return trace[d][k+d+1] }

		prevK := k - 1
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		}

		prevX := at(prevK)
		prevY := prevX - prevK

		for x > prevX && y > prevY {
			reversed = append(reversed, stepEqual)
			x, y = x-1, y-1
		}

		if x == prevX {
			reversed = append(reversed, stepInsert)
		} else {
			reversed = append(reversed, stepDelete)
		}

		x, y = prevX, prevY
	}

	for ; x > 0 && y > 0; x, y = x-1, y-1 {
		reversed = append(reversed, stepEqual)
	}

	slices.Reverse(reversed)

	return reversed
}

// changedLines writes the lines of a script over a and b, whose first lines
// are line prefix+1 of both texts. Inside a run of changes the removed lines
// come first.
func changedLines(steps []step, a, b []string, prefix int) []Line {
	var (
		lines    []Line
		ai, bi   int
		deletes  int
		inserts  int
		oldFirst = prefix + 1
	)

	flush := func() {
		for range deletes {
			lines = append(lines, Line{Kind: LineRemoved, Old: oldFirst + ai, Text: a[ai]})
			ai++
		}

		for range inserts {
			lines = append(lines, Line{Kind: LineAdded, New: prefix + 1 + bi, Text: b[bi]})
			bi++
		}

		deletes, inserts = 0, 0
	}

	for _, s := range steps {
		switch s {
		case stepDelete:
			deletes++
		case stepInsert:
			inserts++
		case stepEqual:
			flush()

			lines = append(lines, Line{Kind: LineContext, Old: oldFirst + ai, New: prefix + 1 + bi, Text: a[ai]})
			ai++
			bi++
		}
	}

	flush()

	return lines
}

// contextLines returns lines as context lines. The first of them is line
// oldAt+1 of the old text and line newAt+1 of the new text.
func contextLines(lines []string, oldAt, newAt int) []Line {
	out := make([]Line, 0, len(lines))

	for i, text := range lines {
		out = append(out, Line{Kind: LineContext, Old: oldAt + i + 1, New: newAt + i + 1, Text: text})
	}

	return out
}

// commonPrefix returns how many lines a and b start with in common.
func commonPrefix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}

	return n
}

// commonSuffix returns how many lines a and b end with in common.
func commonSuffix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}

	return n
}

// finalNewline says how the newline at the end of the text differs between
// the two texts. Nothing differs when either is empty.
func finalNewline(old, updated string) string {
	if old == "" || updated == "" {
		return ""
	}

	oldEnds, newEnds := strings.HasSuffix(old, "\n"), strings.HasSuffix(updated, "\n")

	switch {
	case !oldEnds && newEnds:
		return NewlineAdded
	case oldEnds && !newEnds:
		return NewlineRemoved
	default:
		return ""
	}
}
