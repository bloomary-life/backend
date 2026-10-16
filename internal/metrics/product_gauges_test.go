package metrics

import (
	"context"
	"database/sql"
	"testing"
	"time"

	metricsRepo "bloomary-api/internal/gates/repositories/metrics"
	"bloomary-api/tests"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	_ "modernc.org/sqlite"
)

func TestProductGaugeCacheRefreshQueriesSQLite(t *testing.T) {
	db := newTestDB(t)
	insertAuth(t, db, "signup-yesterday", "2026-09-10T08:00:00Z")
	insertAuth(t, db, "signup-before", "2026-09-09T08:00:00Z")
	insertPost(t, db, "activated", "2026-09-10T08:00:00Z")
	insertPost(t, db, "active-returning", "2026-09-09T08:00:00Z")
	insertPost(t, db, "active-returning", "2026-09-10T08:00:00Z")
	insertPosts(t, db, "four-posts-old", "2026-09-05T08:00:00Z", 4)
	insertPosts(t, db, "four-posts-active", "2026-09-08T08:00:00Z", 2)
	insertPosts(t, db, "four-posts-active", "2026-09-10T08:00:00Z", 2)
	gauges := newTestProductGauges()
	repo, err := metricsRepo.NewMetricsRepository(db)
	if err != nil {
		t.Fatalf("new metrics repository: %v", err)
	}

	err = NewProductGaugeCache().Refresh(tests.TestContext(), repo, gauges.productGauges(), time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC))

	if err != nil {
		t.Fatalf("refresh product gauges: %v", err)
	}
	assertGauge(t, gauges.signups, 1)
	assertGauge(t, gauges.connected, 2)
	assertGauge(t, gauges.activated, 1)
	assertGauge(t, gauges.active, 3)
	assertGauge(t, gauges.users4Posts, 2)
}

func TestProductGaugeCacheUsesCachedValuesForYesterday(t *testing.T) {
	db := newTestDB(t)
	insertAuth(t, db, "signup-1", "2026-09-10T08:00:00Z")
	gauges := newTestProductGauges()
	cache := NewProductGaugeCache()
	repo, err := metricsRepo.NewMetricsRepository(db)
	if err != nil {
		t.Fatalf("new metrics repository: %v", err)
	}
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)

	if err := cache.Refresh(tests.TestContext(), repo, gauges.productGauges(), now); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	insertAuth(t, db, "signup-2", "2026-09-10T09:00:00Z")
	if err := cache.Refresh(tests.TestContext(), repo, gauges.productGauges(), now.Add(time.Hour)); err != nil {
		t.Fatalf("cached refresh: %v", err)
	}

	assertGauge(t, gauges.signups, 1)
	assertGauge(t, gauges.connected, 2)
	if _, err := db.Exec(`DELETE FROM linkedin_auth WHERE member_id = 'signup-1'`); err != nil {
		t.Fatalf("remove connection: %v", err)
	}
	if err := cache.Refresh(tests.TestContext(), repo, gauges.productGauges(), now.Add(2*time.Hour)); err != nil {
		t.Fatalf("refresh after disconnect: %v", err)
	}
	assertGauge(t, gauges.connected, 1)
	assertGauge(t, gauges.signups, 1)
}

func TestProductGaugeCacheRefreshesStaleCache(t *testing.T) {
	db := newTestDB(t)
	insertAuth(t, db, "signup-1", "2026-09-10T08:00:00Z")
	gauges := newTestProductGauges()
	cache := NewProductGaugeCache()
	repo, err := metricsRepo.NewMetricsRepository(db)
	if err != nil {
		t.Fatalf("new metrics repository: %v", err)
	}
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)

	if err := cache.Refresh(tests.TestContext(), repo, gauges.productGauges(), now); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	insertAuth(t, db, "signup-2", "2026-09-10T09:00:00Z")
	cache.mu.Lock()
	cache.expiresAt = now.Add(-time.Second)
	cache.mu.Unlock()
	if err := cache.Refresh(tests.TestContext(), repo, gauges.productGauges(), now); err != nil {
		t.Fatalf("stale refresh: %v", err)
	}

	assertGauge(t, gauges.signups, 2)
}

func TestProductGaugeCacheKeepsPreviousValuesOnSQLiteError(t *testing.T) {
	db := newTestDB(t)
	insertAuth(t, db, "signup-1", "2026-09-10T08:00:00Z")
	gauges := newTestProductGauges()
	cache := NewProductGaugeCache()
	repo, err := metricsRepo.NewMetricsRepository(db)
	if err != nil {
		t.Fatalf("new metrics repository: %v", err)
	}
	now := time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)

	if err := cache.Refresh(tests.TestContext(), repo, gauges.productGauges(), now); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	cache.mu.Lock()
	cache.expiresAt = now.Add(-time.Second)
	cache.mu.Unlock()

	if err := cache.Refresh(tests.TestContext(), repo, gauges.productGauges(), now); err == nil {
		t.Fatalf("refresh after close should fail")
	}
	assertGauge(t, gauges.signups, 1)
}

func TestPostPublishRecordFeedsGauges(t *testing.T) {
	db := newTestDB(t)
	gauges := newTestProductGauges()
	postRepo, err := metricsRepo.NewPostPublishRepository(db)
	if err != nil {
		t.Fatalf("new post publish repository: %v", err)
	}
	metricsRepo, err := metricsRepo.NewMetricsRepository(db)
	if err != nil {
		t.Fatalf("new metrics repository: %v", err)
	}

	err = postRepo.RecordPostPublished(
		tests.TestContext(),
		"member-1",
		time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("record post published: %v", err)
	}
	err = NewProductGaugeCache().Refresh(
		tests.TestContext(),
		metricsRepo,
		gauges.productGauges(),
		time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("refresh product gauges: %v", err)
	}

	assertGauge(t, gauges.activated, 1)
	assertGauge(t, gauges.active, 1)
}

type testProductGauges struct {
	connected   prometheus.Gauge
	signups     prometheus.Gauge
	activated   prometheus.Gauge
	active      prometheus.Gauge
	users4Posts prometheus.Gauge
}

func newTestProductGauges() testProductGauges {
	return testProductGauges{
		connected:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_connected_users"}),
		signups:     prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_signups_yesterday"}),
		activated:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_activated_yesterday"}),
		active:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_active_yesterday"}),
		users4Posts: prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_users_4_posts_last_7d"}),
	}
}

func (g testProductGauges) productGauges() ProductGauges {
	return ProductGauges{
		ConnectedUsers:     g.connected,
		SignupsYesterday:   g.signups,
		ActivatedYesterday: g.activated,
		ActiveYesterday:    g.active,
		Users4PostsLast7d:  g.users4Posts,
	}
}

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE linkedin_auth (
  member_id TEXT PRIMARY KEY,
  connected_at TEXT NOT NULL
);
`); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	postRepo, err := metricsRepo.NewPostPublishRepository(db)
	if err != nil {
		t.Fatalf("new post publish repository: %v", err)
	}
	if err := postRepo.InitTable(context.Background()); err != nil {
		t.Fatalf("init product tables: %v", err)
	}
	return db
}

func insertAuth(t *testing.T, db *sql.DB, memberID, connectedAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO linkedin_auth (member_id, connected_at) VALUES (?, ?)`, memberID, connectedAt); err != nil {
		t.Fatalf("insert auth: %v", err)
	}
}

func insertPosts(t *testing.T, db *sql.DB, memberID, publishedAt string, count int) {
	t.Helper()
	for range count {
		insertPost(t, db, memberID, publishedAt)
	}
}

func insertPost(t *testing.T, db *sql.DB, memberID, publishedAt string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO metric_post_publishes (member_id, published_at) VALUES (?, ?)`,
		memberID,
		publishedAt,
	); err != nil {
		t.Fatalf("insert post: %v", err)
	}
}

func assertGauge(t *testing.T, gauge prometheus.Gauge, want float64) {
	t.Helper()
	var metric dto.Metric
	if err := gauge.Write(&metric); err != nil {
		t.Fatalf("write gauge: %v", err)
	}
	if got := metric.GetGauge().GetValue(); got != want {
		t.Fatalf("gauge = %v, want %v", got, want)
	}
}
