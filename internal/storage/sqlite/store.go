package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"linkwatch/internal/models"
	"linkwatch/internal/storage"
)

// SQLiteStore implements the storage.Storer interface for SQLite.
type SQLiteStore struct {
	db *sql.DB
}

// New creates a new SQLiteStore and establishes a connection to the database file.
// It also runs migrations to ensure the schema is up to date.
func New(ctx context.Context, dataSourceName string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?_foreign_keys=on&_journal_mode=WAL", dataSourceName))
	if err != nil {
		return nil, fmt.Errorf("unable to open sqlite database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("unable to ping database: %w", err)
	}
	store := &SQLiteStore{db: db}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}
	return store, nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// migrate ensures the database schema is created.
func (s *SQLiteStore) migrate(ctx context.Context) error {
	schema := `
CREATE TABLE IF NOT EXISTS targets (
	id            TEXT PRIMARY KEY,
	url           TEXT NOT NULL,
	canonical_url TEXT NOT NULL UNIQUE,
	host          TEXT NOT NULL,
	created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_targets_created_at_id ON targets (created_at, id);
CREATE INDEX IF NOT EXISTS idx_targets_host ON targets (host);

CREATE TABLE IF NOT EXISTS check_results (
	id           TEXT PRIMARY KEY,
	target_id    TEXT NOT NULL,
	checked_at   TEXT NOT NULL,
	status_code  INTEGER,
	latency_ms   INTEGER NOT NULL,
	error        TEXT,
	FOREIGN KEY(target_id) REFERENCES targets(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_check_results_target_id_checked_at ON check_results (target_id, checked_at DESC);

CREATE TABLE IF NOT EXISTS idempotency_keys (
	key          TEXT PRIMARY KEY,
	target_id    TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	FOREIGN KEY(target_id) REFERENCES targets(id)
);
`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

func randomID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return prefix + time.Now().UTC().Format("20060102150405")
	}
	return prefix + hex.EncodeToString(b)
}

// CreateTarget saves a new target, handling idempotency.
func (s *SQLiteStore) CreateTarget(ctx context.Context, target *models.Target, idempotencyKey *string) (*models.Target, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("could not begin transaction: %w", err)
	}
	defer tx.Rollback()

	if idempotencyKey != nil {
		var existingTargetID string
		query := `SELECT target_id FROM idempotency_keys WHERE key = ?`
		err := tx.QueryRowContext(ctx, query, *idempotencyKey).Scan(&existingTargetID)
		if err == nil {
			return s.getTargetByIDTx(ctx, tx, existingTargetID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("failed to check idempotency key: %w", err)
		}
	}

	// Insert target if not exists by canonical URL
	query := `
INSERT INTO targets (id, url, canonical_url, host, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(canonical_url) DO NOTHING`
	res, err := tx.ExecContext(ctx, query, target.ID, target.URL, target.CanonicalURL, target.Host, target.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("failed to insert target: %w", err)
	}
	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		var existingTarget models.Target
		findQuery := `SELECT id, url, canonical_url, host, created_at FROM targets WHERE canonical_url = ?`
		var createdAtStr string
		if err := tx.QueryRowContext(ctx, findQuery, target.CanonicalURL).Scan(&existingTarget.ID, &existingTarget.URL, &existingTarget.CanonicalURL, &existingTarget.Host, &createdAtStr); err != nil {
			return nil, fmt.Errorf("failed to retrieve existing target: %w", err)
		}
		existingTarget.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		return &existingTarget, storage.ErrDuplicateKey
	}

	if idempotencyKey != nil {
		insertKeyQuery := `INSERT INTO idempotency_keys (key, target_id, created_at) VALUES (?, ?, ?)`
		if _, err := tx.ExecContext(ctx, insertKeyQuery, *idempotencyKey, target.ID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return nil, fmt.Errorf("failed to record idempotency key: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}
	return target, nil
}

// getTargetByIDTx retrieves a target within a transaction.
func (s *SQLiteStore) getTargetByIDTx(ctx context.Context, tx *sql.Tx, id string) (*models.Target, error) {
	query := `SELECT id, url, canonical_url, host, created_at FROM targets WHERE id = ?`
	var t models.Target
	var createdAtStr string
	err := tx.QueryRowContext(ctx, query, id).Scan(&t.ID, &t.URL, &t.CanonicalURL, &t.Host, &createdAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get target by id: %w", err)
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
	return &t, nil
}

// GetTargetByID retrieves a single target by its unique ID.
func (s *SQLiteStore) GetTargetByID(ctx context.Context, id string) (*models.Target, error) {
	query := `SELECT id, url, canonical_url, host, created_at FROM targets WHERE id = ?`
	var t models.Target
	var createdAtStr string
	err := s.db.QueryRowContext(ctx, query, id).Scan(&t.ID, &t.URL, &t.CanonicalURL, &t.Host, &createdAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get target by id: %w", err)
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
	return &t, nil
}

// ListTargets retrieves a paginated list of targets.
func (s *SQLiteStore) ListTargets(ctx context.Context, params storage.ListTargetsParams) ([]models.Target, error) {
	var args []interface{}
	qb := strings.Builder{}
	qb.WriteString("SELECT id, url, canonical_url, host, created_at FROM targets WHERE 1=1")
	if params.Host != "" {
		args = append(args, params.Host)
		qb.WriteString(" AND host = ?")
	}
	if !params.AfterTime.IsZero() && params.AfterID != "" {
		args = append(args, params.AfterTime.Format(time.RFC3339Nano), params.AfterID)
		qb.WriteString(" AND (created_at, id) > (?, ?)")
	}
	qb.WriteString(" ORDER BY created_at, id LIMIT ?")
	args = append(args, params.Limit)

	rows, err := s.db.QueryContext(ctx, qb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list targets: %w", err)
	}
	defer rows.Close()
	var targets []models.Target
	for rows.Next() {
		var t models.Target
		var createdAtStr string
		if err := rows.Scan(&t.ID, &t.URL, &t.CanonicalURL, &t.Host, &createdAtStr); err != nil {
			return nil, fmt.Errorf("failed to scan target row: %w", err)
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// GetAllTargets retrieves all targets from the database.
func (s *SQLiteStore) GetAllTargets(ctx context.Context) ([]models.Target, error) {
	query := `SELECT id, url, canonical_url, host, created_at FROM targets ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query all targets: %w", err)
	}
	defer rows.Close()
	var targets []models.Target
	for rows.Next() {
		var t models.Target
		var createdAtStr string
		if err := rows.Scan(&t.ID, &t.URL, &t.CanonicalURL, &t.Host, &createdAtStr); err != nil {
			return nil, fmt.Errorf("failed to scan target row: %w", err)
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAtStr)
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// CreateCheckResult saves a new check result to the database.
func (s *SQLiteStore) CreateCheckResult(ctx context.Context, result *models.CheckResult) error {
	if result.ID == "" {
		result.ID = randomID("cr_")
	}
	query := `INSERT INTO check_results (id, target_id, checked_at, status_code, latency_ms, error) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, query, result.ID, result.TargetID, result.CheckedAt.Format(time.RFC3339Nano), result.StatusCode, result.LatencyMS, result.Error)
	if err != nil {
		return fmt.Errorf("failed to create check result: %w", err)
	}
	return nil
}

// ListCheckResultsByTargetID retrieves recent check results for a target.
func (s *SQLiteStore) ListCheckResultsByTargetID(ctx context.Context, params storage.ListCheckResultsParams) ([]models.CheckResult, error) {
	args := []interface{}{params.TargetID}
	qb := strings.Builder{}
	qb.WriteString("SELECT id, target_id, checked_at, status_code, latency_ms, error FROM check_results WHERE target_id = ?")
	if params.Since != nil {
		args = append(args, params.Since.Format(time.RFC3339Nano))
		qb.WriteString(" AND checked_at > ?")
	}
	qb.WriteString(" ORDER BY checked_at DESC LIMIT ?")
	args = append(args, params.Limit)
	rows, err := s.db.QueryContext(ctx, qb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list check results: %w", err)
	}
	defer rows.Close()
	var results []models.CheckResult
	for rows.Next() {
		var r models.CheckResult
		var checkedAtStr string
		if err := rows.Scan(&r.ID, &r.TargetID, &checkedAtStr, &r.StatusCode, &r.LatencyMS, &r.Error); err != nil {
			return nil, fmt.Errorf("failed to scan check result row: %w", err)
		}
		r.CheckedAt, _ = time.Parse(time.RFC3339Nano, checkedAtStr)
		results = append(results, r)
	}
	return results, rows.Err()
}
