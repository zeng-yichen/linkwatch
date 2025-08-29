package models

import "time"

// Target represents a URL to be monitored.
// It contains both the original URL and its canonical form.
type Target struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	CanonicalURL string    `json:"-"` // Internal field, not exposed in API responses
	Host         string    `json:"-"` // Internal field for the checker's per-host limiter
	CreatedAt    time.Time `json:"created_at"`
}

// CheckResult stores the outcome of a single HTTP check for a Target.
type CheckResult struct {
	ID         string     `json:"id"`
	TargetID   string     `json:"-"` // Not exposed in the results list API
	CheckedAt  time.Time  `json:"checked_at"`
	StatusCode *int       `json:"status_code"` // Pointer to allow for null on network errors
	LatencyMS  int64      `json:"latency_ms"`
	Error      *string    `json:"error"`      // Pointer to allow for null on success
}
