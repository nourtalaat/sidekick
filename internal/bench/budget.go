package bench

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/nourtalaat/sidekick/internal/report"
)

// ErrOverBudget is returned when the next run or grading would take a
// benchmark past its Budget.
var ErrOverBudget = errors.New("the benchmark would cost more than its budget")

// Budget caps what a benchmark may cost. Runs are added to it as they end,
// and graded root causes as they are graded. Before a run starts, it
// stops the benchmark when the money spent plus what a run has cost on
// average so far would pass the cap. A nil *Budget has no cap and counts
// nothing. It is safe for concurrent use.
type Budget struct {
	mu sync.Mutex
	// max is the cap in micro-dollars; zero or less means none.
	max int64
	// spent is everything counted, and runCost and runs what the runs alone
	// cost, for the average.
	spent, runCost int64
	runs           int
}

// NewBudget returns a Budget that allows maxMicros micro-dollars. Zero or
// less allows any amount.
func NewBudget(maxMicros int64) *Budget {
	return &Budget{max: maxMicros}
}

// AddRun counts the cost of a run that ended or was kept from before.
func (b *Budget) AddRun(micros int64) {
	if b == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.spent += micros
	b.runCost += micros
	b.runs++
}

// AddOther counts a cost that is not a run, such as grading a root cause.
func (b *Budget) AddOther(micros int64) {
	if b == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.spent += micros
}

// Spent returns everything counted so far, in micro-dollars.
func (b *Budget) Spent() int64 {
	if b == nil {
		return 0
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.spent
}

// CheckNext returns an error that wraps ErrOverBudget when one more run, at
// the mean cost of those counted so far, would pass the cap.
func (b *Budget) CheckNext() error {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.max <= 0 {
		return nil
	}

	var mean int64
	if b.runs > 0 {
		mean = b.runCost / int64(b.runs)
	}

	if b.spent+mean > b.max {
		return fmt.Errorf("%w: %s spent of %s, and a run costs about %s", ErrOverBudget, usd(b.spent), usd(b.max), usd(mean))
	}

	return nil
}

// CheckSpent returns an error that wraps ErrOverBudget when the cap is
// already reached.
func (b *Budget) CheckSpent() error {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.max > 0 && b.spent >= b.max {
		return fmt.Errorf("%w: %s spent of %s", ErrOverBudget, usd(b.spent), usd(b.max))
	}

	return nil
}

// NewBudgetGrader returns a Grader that grades with inner and counts the
// cost of each grading in budget. It refuses to grade, with an error that
// wraps ErrOverBudget, once the budget is spent, and Score then stops. A nil
// budget returns inner.
func NewBudgetGrader(inner Grader, budget *Budget) Grader {
	if budget == nil {
		return inner
	}

	return &budgetGrader{inner: inner, budget: budget}
}

// budgetGrader is a Grader that counts what it spends.
type budgetGrader struct {
	inner  Grader
	budget *Budget
}

// Grade grades with the inner grader unless the budget is spent.
func (g *budgetGrader) Grade(ctx context.Context, expected RootCause, rep report.Report) (Grade, error) {
	if err := g.budget.CheckSpent(); err != nil {
		return Grade{}, err
	}

	grade, err := g.inner.Grade(ctx, expected, rep)
	g.budget.AddOther(grade.CostMicros)

	if err != nil {
		return grade, fmt.Errorf("grade within the budget: %w", err)
	}

	return grade, nil
}

// usd formats micro-dollars as US dollars.
func usd(micros int64) string {
	return fmt.Sprintf("$%.4f", float64(micros)/microsPerUSD)
}
