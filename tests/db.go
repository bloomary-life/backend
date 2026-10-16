package tests

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

const (
	DefaultTestDBPath = "./test_bloomary.db"
)

func SQLiteConnect(ctx context.Context, t *testing.T, dbPath string) *sql.DB {
	if dbPath == "" {
		dbPath = DefaultTestDBPath
	}

	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		//nolint:gosec // Test infrastructure - paths come from env vars or hardcoded values, not user input
		err := os.MkdirAll(dir, 0755)
		require.NoError(t, err, "error creating db directory")
	}

	// Remove existing test DB to start fresh
	//nolint:gosec // Test infrastructure - paths come from env vars or hardcoded values, not user input
	if _, err := os.Stat(dbPath); err == nil {
		//nolint:gosec // Test infrastructure - paths come from env vars or hardcoded values, not user input
		err = os.Remove(dbPath)
		require.NoError(t, err, "error removing existing test db")
	}

	conn, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err, "error opening sqlite database")

	// Enable WAL mode for better concurrent read performance
	_, err = conn.ExecContext(ctx, "PRAGMA journal_mode=WAL")
	require.NoError(t, err, "error setting WAL mode")

	// Enable foreign keys
	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	require.NoError(t, err, "error enabling foreign keys")

	return conn
}

func SQLiteConnectDefault(ctx context.Context, t *testing.T) *sql.DB {
	dbPath := os.Getenv("SQLITE_TEST_DB_PATH")
	if dbPath == "" {
		dbPath = DefaultTestDBPath
	}
	return SQLiteConnect(ctx, t, dbPath)
}

func CleanupSQLite(t *testing.T, db *sql.DB, dbPath string) {
	err := db.Close()
	require.NoError(t, err, "error closing database")

	if dbPath == "" {
		dbPath = DefaultTestDBPath
	}

	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		t.Logf("warning: could not remove test db %s: %v", dbPath, err)
	}

	// Also remove WAL and SHM files if they exist
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")
}

func FormatTestDBPath(suffix string) string {
	return fmt.Sprintf("./test_bloomary_%s.db", suffix)
}
