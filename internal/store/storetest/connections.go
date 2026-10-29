//go:build integration

package storetest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnvConnsLog names the environment variable with the path of a file where
// each test binary appends one line with the most connections it saw on the
// test servers. When it is empty, nothing is measured. CI sets it to find
// which packages hold the most connections.
const EnvConnsLog = "SIDEKICK_CONNS_LOG"

const (
	connsSampleEvery  = 100 * time.Millisecond
	connsLogMode      = 0o600
	connsCountClients = `SELECT count(*), count(*) FILTER (WHERE datname = ANY($1))
		FROM pg_stat_activity WHERE backend_type = 'client backend'`
)

var (
	ownDatabasesMu sync.Mutex
	ownDatabases   []string
)

// peak is the most connections a sampler saw on one server: all clients of
// the server, and those on databases that this test binary created.
type peak struct {
	total, own int
}

// trackDatabase remembers a database that this test binary created, so the
// sampler can tell its connections from those of other test binaries.
func trackDatabase(name string) {
	ownDatabasesMu.Lock()
	defer ownDatabasesMu.Unlock()

	ownDatabases = append(ownDatabases, name)
}

func ownDatabaseNames() []string {
	ownDatabasesMu.Lock()
	defer ownDatabasesMu.Unlock()

	return append([]string{}, ownDatabases...)
}

// startConnsSampler samples the connections of the test servers every
// 100 ms until the returned function is called, which then appends the
// peaks to the file named by EnvConnsLog.
func startConnsSampler() func() {
	path := os.Getenv(EnvConnsLog)
	if path == "" {
		return func() {}
	}

	ctx, cancel := context.WithCancel(context.Background())
	plain, vector := &peak{}, &peak{}

	var wg sync.WaitGroup

	for _, s := range []struct {
		url    string
		result *peak
	}{{os.Getenv(EnvDatabaseURL), plain}, {os.Getenv(EnvVectorDatabaseURL), vector}} {
		if s.url == "" {
			continue
		}

		wg.Add(1)

		go func() {
			defer wg.Done()

			sampleConns(ctx, s.url, s.result)
		}()
	}

	return func() {
		cancel()
		wg.Wait()

		pkg, err := packageDir()
		if err != nil {
			pkg = err.Error()
		}

		line := fmt.Sprintf("%4d %4d %4d %4d %s\n", plain.own, plain.total, vector.own, vector.total, pkg)

		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, connsLogMode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "storetest: %v\n", err)

			return
		}
		defer f.Close()

		_, _ = f.WriteString(line)
	}
}

func sampleConns(ctx context.Context, url string, result *peak) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return
	}
	defer conn.Close(context.WithoutCancel(ctx))

	ticker := time.NewTicker(connsSampleEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		var total, own int
		if err := conn.QueryRow(ctx, connsCountClients, ownDatabaseNames()).Scan(&total, &own); err != nil {
			continue
		}

		result.total = max(result.total, total)
		result.own = max(result.own, own)
	}
}
