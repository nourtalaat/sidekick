package queue

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// The label names of the queue metrics.
const (
	labelKind   = "kind"
	labelStatus = "status"
)

// waitBuckets are the bucket bounds of sidekick_queue_wait_seconds, from a
// job claimed at once to one that waited ten minutes.
var waitBuckets = []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600}

// Metrics are the queue's Prometheus metrics. Pass them to a Runner in
// Options.Metrics. A nil *Metrics records nothing.
type Metrics struct {
	jobs *prometheus.GaugeVec
	wait *prometheus.HistogramVec
	dead *prometheus.CounterVec

	// mu guards reported, the kind and status pairs that the last refresh
	// of sidekick_queue_jobs set.
	mu       sync.Mutex
	reported map[[2]string]bool
}

// NewMetrics creates the queue metrics and registers them with reg:
//
//   - sidekick_queue_jobs, a gauge of pending, running and dead jobs by kind
//     and status, read from the database by every replica, so combine
//     replicas with max, not sum. Done jobs are left out: their number only
//     grows until retention deletes them.
//   - sidekick_queue_wait_seconds, a histogram by kind of how long jobs
//     waited from ready to claimed.
//   - sidekick_jobs_dead_total, a counter by kind of jobs that became dead
//     on this replica.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		jobs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "sidekick_queue_jobs",
			Help: "Jobs in the queue by kind and status (pending, running or dead).",
		}, []string{labelKind, labelStatus}),
		wait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "sidekick_queue_wait_seconds",
			Help:    "Time from a job being ready to being claimed.",
			Buckets: waitBuckets,
		}, []string{labelKind}),
		dead: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sidekick_jobs_dead_total",
			Help: "Jobs that became dead after their last attempt or because their handler gave up.",
		}, []string{labelKind}),
		reported: map[[2]string]bool{},
	}

	for _, collector := range []prometheus.Collector{m.jobs, m.wait, m.dead} {
		if err := reg.Register(collector); err != nil {
			return nil, fmt.Errorf("register queue metrics: %w", err)
		}
	}

	return m, nil
}

// observeWait records how long a job just claimed with lease waited. The
// claim time comes from the database's clock, as locked_until less the
// lease, so clock differences between replicas do not skew it.
func (m *Metrics) observeWait(job db.Job, lease time.Duration) {
	if m == nil || job.LockedUntil == nil {
		return
	}

	claimedAt := job.LockedUntil.Add(-lease)
	m.wait.WithLabelValues(job.Kind).Observe(max(claimedAt.Sub(job.RunAt).Seconds(), 0))
}

// jobDied counts a job of kind that became dead.
func (m *Metrics) jobDied(kind string) {
	if m == nil {
		return
	}

	m.dead.WithLabelValues(kind).Inc()
}

// setCounts sets sidekick_queue_jobs from counts, and sets to zero every
// kind and status that an earlier refresh reported and counts leaves out.
func (m *Metrics) setCounts(counts []db.CountJobsRow) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current := make(map[[2]string]bool, len(counts))

	for _, row := range counts {
		key := [2]string{row.Kind, row.Status}
		current[key] = true
		m.jobs.WithLabelValues(row.Kind, row.Status).Set(float64(row.Jobs))
	}

	for key := range m.reported {
		if !current[key] {
			m.jobs.WithLabelValues(key[0], key[1]).Set(0)
			current[key] = true
		}
	}

	m.reported = current
}
