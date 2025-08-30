package storage

import (
	"context"
	"errors"
	"time"

	"linkwatch/internal/models"
)

var (
	// ErrDuplicateKey is returned when attempting to create a duplicate resource
	ErrDuplicateKey = errors.New("duplicate")
	// ErrNotFound is returned when a requested resource is not found
	ErrNotFound = errors.New("not found")
)

// ListTargetsParams contains parameters for listing targets with filtering and pagination
type ListTargetsParams struct {
	Host      string
	AfterTime time.Time
	AfterID   string
	Limit     int
}

// ListCheckResultsParams contains parameters for listing check results with filtering and pagination
type ListCheckResultsParams struct {
	TargetID string
	Since    *time.Time
	Limit    int
}

// Storer defines the interface for storage operations on targets and check results
type Storer interface {
	CreateTarget(ctx context.Context, target *models.Target, idempotencyKey *string) (*models.Target, error)
	GetTargetByID(ctx context.Context, id string) (*models.Target, error)
	ListTargets(ctx context.Context, params ListTargetsParams) ([]models.Target, error)
	GetAllTargets(ctx context.Context) ([]models.Target, error)

	CreateCheckResult(ctx context.Context, result *models.CheckResult) error
	ListCheckResultsByTargetID(ctx context.Context, params ListCheckResultsParams) ([]models.CheckResult, error)
}
