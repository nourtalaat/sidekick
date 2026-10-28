package retention

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// labelTable is the label of the table rows were deleted from.
const labelTable = "table"

// Metrics are the retention job's Prometheus metrics. Pass them to New in
// Options.Metrics. A nil *Metrics records nothing.
type Metrics struct {
	deletedRows *prometheus.CounterVec
}

// NewMetrics creates the retention metrics and registers them with reg:
// sidekick_retention_deleted_rows_total, a counter by table of the rows
// the retention job deleted on this replica.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		deletedRows: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sidekick_retention_deleted_rows_total",
			Help: "Rows the retention job deleted, by table.",
		}, []string{labelTable}),
	}

	if err := reg.Register(m.deletedRows); err != nil {
		return nil, fmt.Errorf("register retention metrics: %w", err)
	}

	return m, nil
}

// deleted counts n rows deleted from table.
func (m *Metrics) deleted(table string, n int64) {
	if m == nil || n == 0 {
		return
	}

	m.deletedRows.WithLabelValues(table).Add(float64(n))
}
