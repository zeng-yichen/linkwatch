package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"linkwatch/internal/models"
	"linkwatch/internal/storage"
)

// PostgresStore implements the storage.Storer interface for PostgreSQL.
type PostgresStore struct {
	db *pgxpool.Pool
}

// New creates a new PostgresStore and establishes a connection to the database.
// It also runs migrations to ensure the schema is up to date.
func New(ctx context.Context, connString string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("unable to ping database: %w", err)
	}

	store := &PostgresStore{db: pool}
	if err := store.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return store, nil
}

// Close closes the database connection pool.
func (s *PostgresStore) Close() {
	s.db.Close()
}

// migrate ensures the database schema is created.
func (s *PostgresStore) migrate(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS targets (
		id            TEXT PRIMARY KEY,
		url           TEXT NOT NULL,
		canonical_url TEXT NOT NULL UNIQUE,
		host          TEXT NOT NULL,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	CREATE INDEX IF NOT EXISTS idx_targets_created_at_id ON targets (created_at, id);
	CREATE INDEX IF NOT EXISTS idx_targets_host ON targets (host);

	CREATE TABLE IF NOT EXISTS check_results (
		id           TEXT PRIMARY KEY,
		target_id    TEXT NOT NULL REFERENCES targets(id) ON DELETE CASCADE,
		checked_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		status_code  INTEGER,
		latency_ms   INTEGER NOT NULL,
		error        TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_check_results_target_id_checked_at ON check_results (target_id, checked_at DESC);

	CREATE TABLE IF NOT EXISTS idempotency_keys (
		key          TEXT PRIMARY KEY,
		target_id    TEXT NOT NULL REFERENCES targets(id),
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	`
	_, err := s.db.Exec(ctx, schema)
	return err
}

// CreateTarget implements the Storer interface.
func (s *PostgresStore) CreateTarget(ctx context.Context, target *models.Target, idempotencyKey *string) (*models.Target, error) {
	// TODO: Implement full transaction with idempotency key handling
	// For now, just insert the target
	query := `INSERT INTO targets (id, url, canonical_url, host, created_at) VALUES ($1, $2, $3, $4, $5)`
	_, err := s.db.Exec(ctx, query, target.ID, target.URL, target.CanonicalURL, target.Host, target.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create target: %w", err)
	}
	return target, nil
}

// GetTargetByID implements the Storer interface.
func (s *PostgresStore) GetTargetByID(ctx context.Context, id string) (*models.Target, error) {
	query := `SELECT id, url, canonical_url, host, created_at FROM targets WHERE id = $1`
	var t models.Target
	err := s.db.QueryRow(ctx, query, id).Scan(&t.ID, &t.URL, &t.CanonicalURL, &t.Host, &t.CreatedAt)
	if err != nil {
		return nil, storage.ErrNotFound
	}
	return &t, nil
}

// ListTargets implements the Storer interface.
func (s *PostgresStore) ListTargets(ctx context.Context, params storage.ListTargetsParams) ([]models.Target, error) {
	// TODO: Implement pagination and filtering
	query := `SELECT id, url, canonical_url, host, created_at FROM targets ORDER BY created_at, id LIMIT $1`
	rows, err := s.db.Query(ctx, query, params.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list targets: %w", err)
	}
	defer rows.Close()

	var targets []models.Target
	for rows.Next() {
		var t models.Target
		if err := rows.Scan(&t.ID, &t.URL, &t.CanonicalURL, &t.Host, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan target: %w", err)
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// GetAllTargets implements the Storer interface.
func (s *PostgresStore) GetAllTargets(ctx context.Context) ([]models.Target, error) {
	query := `SELECT id, url, canonical_url, host, created_at FROM targets ORDER BY created_at, id`
	rows, err := s.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query all targets: %w", err)
	}
	defer rows.Close()

	var targets []models.Target
	for rows.Next() {
		var t models.Target
		if err := rows.Scan(&t.ID, &t.URL, &t.CanonicalURL, &t.Host, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan target row: %w", err)
		}
		targets = append(targets, t)
	}

	return targets, rows.Err()
}

// CreateCheckResult implements the Storer interface.
func (s *PostgresStore) CreateCheckResult(ctx context.Context, result *models.CheckResult) error {
	query := `INSERT INTO check_results (id, target_id, checked_at, status_code, latency_ms, error) VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := s.db.Exec(ctx, query, result.ID, result.TargetID, result.CheckedAt, result.StatusCode, result.LatencyMS, result.Error)
	if err != nil {
		return fmt.Errorf("failed to create check result: %w", err)
	}
	return nil
}

// ListCheckResultsByTargetID implements the Storer interface.
func (s *PostgresStore) ListCheckResultsByTargetID(ctx context.Context, params storage.ListCheckResultsParams) ([]models.CheckResult, error) {
	query := `SELECT id, target_id, checked_at, status_code, latency_ms, error FROM check_results WHERE target_id = $1 ORDER BY checked_at DESC LIMIT $2`
	rows, err := s.db.Query(ctx, query, params.TargetID, params.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list check results: %w", err)
	}
	defer rows.Close()

	var results []models.CheckResult
	for rows.Next() {
		var r models.CheckResult
		if err := rows.Scan(&r.ID, &r.TargetID, &r.CheckedAt, &r.StatusCode, &r.LatencyMS, &r.Error); err != nil {
			return nil, fmt.Errorf("failed to scan check result: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
