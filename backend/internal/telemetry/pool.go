package telemetry

import (
	"context"
	"database/sql"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Pool metrics are summed per pool label ("metadata", "target"): every target
// registration has its own *sql.DB, but a per-database label is not allowed.
var pools = struct {
	sync.Mutex
	once    sync.Once
	dbs     map[*sql.DB]string
	retired map[string]sql.DBStats // waits of closed pools, so the counters stay monotonic
}{dbs: map[*sql.DB]string{}, retired: map[string]sql.DBStats{}}

// RegisterPool adds db's connection stats to the db_pool_* metrics under
// pool; call unregister before the pool is closed.
func RegisterPool(pool string, db *sql.DB) (unregister func()) {
	pools.once.Do(registerPoolCallback)
	pools.Lock()
	pools.dbs[db] = pool
	pools.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			stats := db.Stats()
			pools.Lock()
			defer pools.Unlock()
			delete(pools.dbs, db)
			r := pools.retired[pool]
			r.WaitCount += stats.WaitCount
			r.WaitDuration += stats.WaitDuration
			pools.retired[pool] = r
		})
	}
}

func registerPoolCallback() {
	m := meter()
	conns, _ := m.Int64ObservableGauge("db_pool_connections", metric.WithDescription("Open connections by state."))
	maxOpen, _ := m.Int64ObservableGauge("db_pool_max_open_connections", metric.WithDescription("Configured maximum open connections."))
	waits, _ := m.Int64ObservableCounter("db_pool_wait_count", metric.WithDescription("Connections waited for."))
	waited, _ := m.Float64ObservableCounter("db_pool_wait_duration", metric.WithUnit("s"), metric.WithDescription("Time spent waiting for a connection."))
	_, _ = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		sums := map[string]sql.DBStats{}
		pools.Lock()
		for pool, r := range pools.retired {
			sums[pool] = r
		}
		for db, pool := range pools.dbs {
			s, cur := sums[pool], db.Stats()
			s.InUse += cur.InUse
			s.Idle += cur.Idle
			s.MaxOpenConnections += cur.MaxOpenConnections
			s.WaitCount += cur.WaitCount
			s.WaitDuration += cur.WaitDuration
			sums[pool] = s
		}
		pools.Unlock()
		for pool, s := range sums {
			p := attribute.String("pool", pool)
			o.ObserveInt64(conns, int64(s.InUse), metric.WithAttributes(p, attribute.String("state", "in_use")))
			o.ObserveInt64(conns, int64(s.Idle), metric.WithAttributes(p, attribute.String("state", "idle")))
			o.ObserveInt64(maxOpen, int64(s.MaxOpenConnections), metric.WithAttributes(p))
			o.ObserveInt64(waits, s.WaitCount, metric.WithAttributes(p))
			o.ObserveFloat64(waited, s.WaitDuration.Seconds(), metric.WithAttributes(p))
		}
		return nil
	}, conns, maxOpen, waits, waited)
}
