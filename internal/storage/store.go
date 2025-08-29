package storage

import (
	"context"
	"errors"
	"time"

	"linkwatch/internal/models"
)

var (
	ErrDuplicateKey = errors.New("duplicate")
	ErrNotFound     = errors.New("not found")
)

type ListTargetsParams struct {
	Host      string
	AfterTime time.Time
	AfterID   string
	Limit     int
}

type ListCheckResultsParams struct {
	TargetID string
	Since    *time.Time
	Limit    int
}

type Storer interface {
	CreateTarget(ctx context.Context, target *models.Target, idempotencyKey *string) (*models.Target, error)
	GetTargetByID(ctx context.Context, id string) (*models.Target, error)
	ListTargets(ctx context.Context, params ListTargetsParams) ([]models.Target, error)
	GetAllTargets(ctx context.Context) ([]models.Target, error)

	CreateCheckResult(ctx context.Context, result *models.CheckResult) error
	ListCheckResultsByTargetID(ctx context.Context, params ListCheckResultsParams) ([]models.CheckResult, error)
}
