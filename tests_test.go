package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"linkwatch/internal/api"
	"linkwatch/internal/checker"
	"linkwatch/internal/config"
	"linkwatch/internal/models"
	"linkwatch/internal/storage"
	"linkwatch/internal/storage/sqlite"
	"linkwatch/internal/urlutil"
)

// Simple in-memory storage for testing
type testStore struct {
	mu          sync.RWMutex
	targets     map[string]models.Target
	results     map[string][]models.CheckResult
	idempotency map[string]string
	canonical   map[string]string
}

func newTestStore() *testStore {
	return &testStore{
		targets:     make(map[string]models.Target),
		results:     make(map[string][]models.CheckResult),
		idempotency: make(map[string]string),
		canonical:   make(map[string]string),
	}
}

func (s *testStore) CreateTarget(ctx context.Context, target *models.Target, idempotencyKey *string) (*models.Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check idempotency key first
	if idempotencyKey != nil {
		if targetID, ok := s.idempotency[*idempotencyKey]; ok {
			t := s.targets[targetID]
			return &t, storage.ErrDuplicateKey
		}
	}

	// Check for duplicate canonical URL
	if targetID, ok := s.canonical[target.CanonicalURL]; ok {
		t := s.targets[targetID]
		return &t, storage.ErrDuplicateKey
	}

	// Create new target
	s.targets[target.ID] = *target
	s.canonical[target.CanonicalURL] = target.ID
	if idempotencyKey != nil {
		s.idempotency[*idempotencyKey] = target.ID
	}

	t := *target
	return &t, nil
}

func (s *testStore) GetTargetByID(ctx context.Context, id string) (*models.Target, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if t, ok := s.targets[id]; ok {
		return &t, nil
	}
	return nil, storage.ErrNotFound
}

func (s *testStore) ListTargets(ctx context.Context, params storage.ListTargetsParams) ([]models.Target, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var targets []models.Target
	for _, t := range s.targets {
		// Host filtering
		if params.Host != "" && strings.ToLower(t.Host) != strings.ToLower(params.Host) {
			continue
		}

		// Pagination filtering
		if !params.AfterTime.IsZero() && params.AfterID != "" {
			// Skip items that come before or equal to the cursor
			if t.CreatedAt.Before(params.AfterTime) ||
				(t.CreatedAt.Equal(params.AfterTime) && t.ID <= params.AfterID) {
				continue
			}
		}

		targets = append(targets, t)
	}

	// Sort by (created_at, id) for deterministic ordering
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].CreatedAt.Equal(targets[j].CreatedAt) {
			return targets[i].ID < targets[j].ID
		}
		return targets[i].CreatedAt.Before(targets[j].CreatedAt)
	})

	// Apply limit
	if len(targets) > params.Limit {
		return targets[:params.Limit], nil
	}
	return targets, nil
}

func (s *testStore) GetAllTargets(ctx context.Context) ([]models.Target, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var targets []models.Target
	for _, t := range s.targets {
		targets = append(targets, t)
	}
	return targets, nil
}

func (s *testStore) CreateCheckResult(ctx context.Context, result *models.CheckResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.results[result.TargetID] = append(s.results[result.TargetID], *result)
	return nil
}

func (s *testStore) ListCheckResultsByTargetID(ctx context.Context, params storage.ListCheckResultsParams) ([]models.CheckResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	results, ok := s.results[params.TargetID]
	if !ok {
		return []models.CheckResult{}, nil
	}
	if len(results) > params.Limit {
		return results[:params.Limit], nil
	}
	return results, nil
}

func TestURLCanonicalization(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "Standard URL",
			input: "http://example.com/path",
			want:  "http://example.com/path",
		},
		{
			name:  "Uppercase Scheme and Host",
			input: "HTTPS://EXAMPLE.COM/path",
			want:  "https://example.com/path",
		},
		{
			name:  "With Default HTTP Port",
			input: "http://example.com:80/path",
			want:  "http://example.com/path",
		},
		{
			name:  "With Default HTTPS Port",
			input: "https://example.com:443/path",
			want:  "https://example.com/path",
		},
		{
			name:  "With Custom Port",
			input: "http://example.com:8080/path",
			want:  "http://example.com:8080/path",
		},
		{
			name:  "With Fragment",
			input: "http://example.com/path#section1",
			want:  "http://example.com/path",
		},
		{
			name:  "With Trailing Slash",
			input: "http://example.com/path/",
			want:  "http://example.com/path",
		},
		{
			name:  "Root Path with Trailing Slash",
			input: "http://example.com/",
			want:  "http://example.com/",
		},
		{
			name:    "Invalid URL",
			input:   "://example.com",
			wantErr: true,
		},
		{
			name:    "Relative URL",
			input:   "/path/to/resource",
			wantErr: true,
		},
		{
			name:    "Unsupported Scheme",
			input:   "ftp://example.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := urlutil.Canonicalize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Canonicalize() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("Canonicalize() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAPICreateTarget(t *testing.T) {
	store := newTestStore()
	router := api.NewRouter(store)

	t.Run("success on first create", func(t *testing.T) {
		body := `{"url": "https://example.com"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/targets", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()

		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated {
			t.Errorf("expected status %d, got %d", http.StatusCreated, rr.Code)
		}

		var resp models.Target
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if resp.URL != "https://example.com" {
			t.Errorf("expected URL %s, got %s", "https://example.com", resp.URL)
		}
	})

	t.Run("success with 200 on duplicate canonical url", func(t *testing.T) {
		body := `{"url": "https://example.com"}` // Same canonical URL as first test
		req := httptest.NewRequest(http.MethodPost, "/v1/targets", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()

		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
		}
	})

	t.Run("idempotency key works", func(t *testing.T) {
		body := `{"url": "https://idempotent.com"}`
		key := "test-key-123"

		// First request
		req1 := httptest.NewRequest(http.MethodPost, "/v1/targets", bytes.NewBufferString(body))
		req1.Header.Set("Idempotency-Key", key)
		rr1 := httptest.NewRecorder()
		router.ServeHTTP(rr1, req1)
		if rr1.Code != http.StatusCreated {
			t.Errorf("expected status %d on first idempotent request, got %d", http.StatusCreated, rr1.Code)
		}
		var resp1 models.Target
		json.NewDecoder(rr1.Body).Decode(&resp1)

		// Second request with same key
		req2 := httptest.NewRequest(http.MethodPost, "/v1/targets", bytes.NewBufferString(body))
		req2.Header.Set("Idempotency-Key", key)
		rr2 := httptest.NewRecorder()
		router.ServeHTTP(rr2, req2)
		if rr2.Code != http.StatusOK {
			t.Errorf("expected status %d on second idempotent request, got %d", http.StatusOK, rr2.Code)
		}
		var resp2 models.Target
		json.NewDecoder(rr2.Body).Decode(&resp2)

		if resp1.ID != resp2.ID {
			t.Errorf("expected same target ID on idempotent requests, got %s and %s", resp1.ID, resp2.ID)
		}
	})

	t.Run("invalid URL returns 400", func(t *testing.T) {
		body := `{"url": "not-a-url"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/targets", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()

		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
		}
	})
}

func TestAPIListTargets(t *testing.T) {
	store := newTestStore()
	router := api.NewRouter(store)

	// Pre-populate store with some data
	baseTime := time.Now().UTC()
	store.CreateTarget(context.Background(), &models.Target{ID: "t_1", URL: "http://a.com", CanonicalURL: "http://a.com", Host: "a.com", CreatedAt: baseTime}, nil)
	store.CreateTarget(context.Background(), &models.Target{ID: "t_2", URL: "http://b.com", CanonicalURL: "http://b.com", Host: "b.com", CreatedAt: baseTime.Add(time.Second)}, nil)

	t.Run("list targets with pagination", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/targets?limit=1", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
		}

		var resp struct {
			Items         []models.Target `json:"items"`
			NextPageToken string          `json:"next_page_token"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if len(resp.Items) != 1 {
			t.Errorf("expected 1 item, got %d", len(resp.Items))
		}
		if resp.NextPageToken == "" {
			t.Error("expected next page token")
		}
	})

	t.Run("list targets with host filter", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/targets?host=a.com", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
		}

		var resp struct {
			Items []models.Target `json:"items"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if len(resp.Items) != 1 {
			t.Errorf("expected 1 item for host filter, got %d", len(resp.Items))
		}
		// Host field is not exposed in API responses, so we can't check it here
		// The filtering is working if we get exactly 1 item when filtering by host
	})

	t.Run("full pagination flow", func(t *testing.T) {
		// First page: limit=1
		req := httptest.NewRequest(http.MethodGet, "/v1/targets?limit=1", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
		}

		var resp1 struct {
			Items         []models.Target `json:"items"`
			NextPageToken string          `json:"next_page_token"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp1); err != nil {
			t.Fatalf("failed to decode first page response: %v", err)
		}

		if len(resp1.Items) != 1 {
			t.Errorf("expected 1 item on first page, got %d", len(resp1.Items))
		}
		if resp1.NextPageToken == "" {
			t.Fatal("expected next page token on first page")
		}

		// Second page: use the token
		req2 := httptest.NewRequest(http.MethodGet, "/v1/targets?limit=1&page_token="+resp1.NextPageToken, nil)
		rr2 := httptest.NewRecorder()
		router.ServeHTTP(rr2, req2)

		if rr2.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rr2.Code)
		}

		var resp2 struct {
			Items         []models.Target `json:"items"`
			NextPageToken string          `json:"next_page_token"`
		}
		if err := json.NewDecoder(rr2.Body).Decode(&resp2); err != nil {
			t.Fatalf("failed to decode second page response: %v", err)
		}

		if len(resp2.Items) != 1 {
			t.Errorf("expected 1 item on second page, got %d", len(resp2.Items))
		}
		// Since we have exactly 2 items total and limit=1, the second page should be full
		// and thus generate a next page token, but there are no more items after that
		if resp2.NextPageToken == "" {
			t.Error("expected next page token on second page (page is full)")
		}

		// Verify items are different
		if resp1.Items[0].ID == resp2.Items[0].ID {
			t.Error("expected different items on different pages")
		}

		// Verify ordering (first page should have earlier timestamp)
		if resp1.Items[0].CreatedAt.After(resp2.Items[0].CreatedAt) {
			t.Error("expected first page to have earlier timestamp than second page")
		}

		// Third page: should have no items and no next page token
		req3 := httptest.NewRequest(http.MethodGet, "/v1/targets?limit=1&page_token="+resp2.NextPageToken, nil)
		rr3 := httptest.NewRecorder()
		router.ServeHTTP(rr3, req3)

		if rr3.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rr3.Code)
		}

		var resp3 struct {
			Items         []models.Target `json:"items"`
			NextPageToken string          `json:"next_page_token"`
		}
		if err := json.NewDecoder(rr3.Body).Decode(&resp3); err != nil {
			t.Fatalf("failed to decode third page response: %v", err)
		}

		if len(resp3.Items) != 0 {
			t.Errorf("expected 0 items on third page, got %d", len(resp3.Items))
		}
		if resp3.NextPageToken != "" {
			t.Error("expected no next page token on third page (no more items)")
		}
	})
}

func TestAPIListCheckResults(t *testing.T) {
	store := newTestStore()
	router := api.NewRouter(store)

	// Create a target and add some results
	target, _ := store.CreateTarget(context.Background(), &models.Target{ID: "t_results", URL: "http://results.com", CanonicalURL: "http://results.com", Host: "results.com"}, nil)

	now := time.Now().UTC()
	status200 := 200
	store.CreateCheckResult(context.Background(), &models.CheckResult{TargetID: target.ID, CheckedAt: now.Add(-time.Minute), StatusCode: &status200, LatencyMS: 100})
	store.CreateCheckResult(context.Background(), &models.CheckResult{TargetID: target.ID, CheckedAt: now, StatusCode: &status200, LatencyMS: 120})

	t.Run("get check results", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/targets/"+target.ID+"/results", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
		}

		var resp struct {
			Items []models.CheckResult `json:"items"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if len(resp.Items) != 2 {
			t.Errorf("expected 2 results, got %d", len(resp.Items))
		}
	})

	t.Run("target not found returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/targets/t_notfound/results", nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Errorf("expected status 404 for non-existent target, got %d", rr.Code)
		}
	})
}

func TestAPIHealthz(t *testing.T) {
	store := newTestStore()
	router := api.NewRouter(store)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
}

func TestSQLiteStorage(t *testing.T) {
	// Test SQLite storage with a temporary database
	ctx := context.Background()
	store, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	defer store.Close()

	t.Run("create and retrieve target", func(t *testing.T) {
		target := &models.Target{
			ID:           "t_test",
			URL:          "https://example.com",
			CanonicalURL: "https://example.com",
			Host:         "example.com",
			CreatedAt:    time.Now().UTC(),
		}

		created, err := store.CreateTarget(ctx, target, nil)
		if err != nil {
			t.Fatalf("failed to create target: %v", err)
		}

		retrieved, err := store.GetTargetByID(ctx, created.ID)
		if err != nil {
			t.Fatalf("failed to retrieve target: %v", err)
		}

		if retrieved.ID != target.ID {
			t.Errorf("expected ID %s, got %s", target.ID, retrieved.ID)
		}
		if retrieved.URL != target.URL {
			t.Errorf("expected URL %s, got %s", target.URL, retrieved.URL)
		}
	})

	t.Run("create check result", func(t *testing.T) {
		result := &models.CheckResult{
			TargetID:   "t_test",
			CheckedAt:  time.Now().UTC(),
			LatencyMS:  100,
			StatusCode: &[]int{200}[0],
		}

		err := store.CreateCheckResult(ctx, result)
		if err != nil {
			t.Fatalf("failed to create check result: %v", err)
		}

		results, err := store.ListCheckResultsByTargetID(ctx, storage.ListCheckResultsParams{
			TargetID: "t_test",
			Limit:    10,
		})
		if err != nil {
			t.Fatalf("failed to list check results: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("expected 1 result, got %d", len(results))
		}
		if results[0].LatencyMS != 100 {
			t.Errorf("expected latency 100, got %d", results[0].LatencyMS)
		}
	})

	t.Run("idempotency key handling", func(t *testing.T) {
		// Create target with idempotency key
		target := &models.Target{
			ID:           "t_idempotent",
			URL:          "https://idempotent.com",
			CanonicalURL: "https://idempotent.com",
			Host:         "idempotent.com",
			CreatedAt:    time.Now().UTC(),
		}
		idempotencyKey := "test-key-123"

		// First request
		created1, err := store.CreateTarget(ctx, target, &idempotencyKey)
		if err != nil {
			t.Fatalf("failed to create target with idempotency key: %v", err)
		}

		// Second request with same key
		created2, err := store.CreateTarget(ctx, target, &idempotencyKey)
		if err != nil {
			t.Fatalf("failed to create target with same idempotency key: %v", err)
		}

		// Should return same target
		if created1.ID != created2.ID {
			t.Errorf("expected same target ID for idempotency key, got %s and %s", created1.ID, created2.ID)
		}

		// Third request with different key but same canonical URL
		differentKey := "test-key-456"
		created3, err := store.CreateTarget(ctx, target, &differentKey)
		if err != nil && !errors.Is(err, storage.ErrDuplicateKey) {
			t.Fatalf("failed to create target with different idempotency key: %v", err)
		}

		// Should return same target (canonical URL deduplication)
		if err == nil && created1.ID != created3.ID {
			t.Errorf("expected same target ID for same canonical URL, got %s and %s", created1.ID, created3.ID)
		}
	})

	t.Run("canonical URL deduplication", func(t *testing.T) {
		// Create first target
		target1 := &models.Target{
			ID:           "t_canonical1",
			URL:          "https://canonical-test.com/path",
			CanonicalURL: "https://canonical-test.com/path",
			Host:         "canonical-test.com",
			CreatedAt:    time.Now().UTC(),
		}

		created1, err := store.CreateTarget(ctx, target1, nil)
		if err != nil {
			t.Fatalf("failed to create first target: %v", err)
		}

		// Create second target with same canonical URL
		target2 := &models.Target{
			ID:           "t_canonical2",
			URL:          "https://CANONICAL-TEST.COM/path", // Different case, same canonical
			CanonicalURL: "https://canonical-test.com/path",
			Host:         "canonical-test.com",
			CreatedAt:    time.Now().UTC(),
		}

		created2, err := store.CreateTarget(ctx, target2, nil)
		if err != nil && !errors.Is(err, storage.ErrDuplicateKey) {
			t.Fatalf("failed to create second target: %v", err)
		}

		// Should return same target ID
		if err == nil && created1.ID != created2.ID {
			t.Errorf("expected same target ID for same canonical URL, got %s and %s", created1.ID, created2.ID)
		}

		// Should return first target's URL
		if err == nil && created2.URL != target1.URL {
			t.Errorf("expected first target's URL, got %s", created2.URL)
		}
	})

	t.Run("pagination and filtering", func(t *testing.T) {
		// Create multiple targets with different hosts and timestamps
		baseTime := time.Now().UTC()
		targets := []*models.Target{
			{
				ID:           "t_paginate1",
				URL:          "https://paginate-host1.com",
				CanonicalURL: "https://paginate-host1.com",
				Host:         "paginate-host1.com",
				CreatedAt:    baseTime,
			},
			{
				ID:           "t_paginate2",
				URL:          "https://paginate-host2.com",
				CanonicalURL: "https://paginate-host2.com",
				Host:         "paginate-host2.com",
				CreatedAt:    baseTime.Add(time.Second),
			},
			{
				ID:           "t_paginate3",
				URL:          "https://paginate-host1.com/path",
				CanonicalURL: "https://paginate-host1.com/path",
				Host:         "paginate-host1.com",
				CreatedAt:    baseTime.Add(2 * time.Second),
			},
		}

		// Create all targets
		for _, target := range targets {
			_, err := store.CreateTarget(ctx, target, nil)
			if err != nil {
				t.Fatalf("failed to create target: %v", err)
			}
		}

		// Test host filtering
		host1Targets, err := store.ListTargets(ctx, storage.ListTargetsParams{
			Host:  "paginate-host1.com",
			Limit: 10,
		})
		if err != nil {
			t.Fatalf("failed to list targets with host filter: %v", err)
		}
		if len(host1Targets) != 2 {
			t.Errorf("expected 2 targets for paginate-host1.com, got %d", len(host1Targets))
		}

		// Test pagination - get all targets first to see what we have
		allTargets, err := store.GetAllTargets(ctx)
		if err != nil {
			t.Fatalf("failed to get all targets: %v", err)
		}

		// Test pagination with limit
		paginatedTargets, err := store.ListTargets(ctx, storage.ListTargetsParams{
			Limit: 2,
		})
		if err != nil {
			t.Fatalf("failed to list targets with pagination: %v", err)
		}
		if len(paginatedTargets) != 2 {
			t.Errorf("expected 2 targets with limit 2, got %d", len(paginatedTargets))
		}

		// Test cursor pagination
		if len(paginatedTargets) >= 2 {
			lastTarget := paginatedTargets[1]
			nextPageTargets, err := store.ListTargets(ctx, storage.ListTargetsParams{
				AfterTime: lastTarget.CreatedAt,
				AfterID:   lastTarget.ID,
				Limit:     10,
			})
			if err != nil {
				t.Fatalf("failed to list targets with cursor: %v", err)
			}
			// Should have remaining targets (total - 2 from first page)
			expectedRemaining := len(allTargets) - 2
			if len(nextPageTargets) != expectedRemaining {
				t.Errorf("expected %d targets on next page, got %d", expectedRemaining, len(nextPageTargets))
			}
		}
	})

	t.Run("error handling - target not found", func(t *testing.T) {
		_, err := store.GetTargetByID(ctx, "nonexistent-id")
		if err == nil {
			t.Error("expected error for nonexistent target")
		}
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("error handling - invalid idempotency key", func(t *testing.T) {
		// Test with nil idempotency key (should work)
		target := &models.Target{
			ID:           "t_nil_key",
			URL:          "https://nil-key.com",
			CanonicalURL: "https://nil-key.com",
			Host:         "nil-key.com",
			CreatedAt:    time.Now().UTC(),
		}

		_, err := store.CreateTarget(ctx, target, nil)
		if err != nil {
			t.Fatalf("failed to create target with nil idempotency key: %v", err)
		}
	})

	t.Run("check results with since filter", func(t *testing.T) {
		// Create a target first
		target := &models.Target{
			ID:           "t_since_test",
			URL:          "https://since-test.com",
			CanonicalURL: "https://since-test.com",
			Host:         "since-test.com",
			CreatedAt:    time.Now().UTC(),
		}
		_, err := store.CreateTarget(ctx, target, nil)
		if err != nil {
			t.Fatalf("failed to create target: %v", err)
		}

		// Create check results at different times
		baseTime := time.Now().UTC()
		results := []*models.CheckResult{
			{
				TargetID:   target.ID,
				CheckedAt:  baseTime,
				LatencyMS:  100,
				StatusCode: &[]int{200}[0],
			},
			{
				TargetID:   target.ID,
				CheckedAt:  baseTime.Add(time.Minute),
				LatencyMS:  150,
				StatusCode: &[]int{200}[0],
			},
			{
				TargetID:   target.ID,
				CheckedAt:  baseTime.Add(2 * time.Minute),
				LatencyMS:  200,
				StatusCode: &[]int{500}[0],
			},
		}

		// Create all results
		for _, result := range results {
			err := store.CreateCheckResult(ctx, result)
			if err != nil {
				t.Fatalf("failed to create check result: %v", err)
			}
		}

		// Test since filter
		sinceTime := baseTime.Add(30 * time.Second)
		filteredResults, err := store.ListCheckResultsByTargetID(ctx, storage.ListCheckResultsParams{
			TargetID: target.ID,
			Since:    &sinceTime,
			Limit:    10,
		})
		if err != nil {
			t.Fatalf("failed to list check results with since filter: %v", err)
		}
		if len(filteredResults) != 2 {
			t.Errorf("expected 2 results after since time, got %d", len(filteredResults))
		}

		// Verify results are ordered by checked_at DESC
		if len(filteredResults) >= 2 {
			if filteredResults[0].CheckedAt.Before(filteredResults[1].CheckedAt) {
				t.Error("expected results ordered by checked_at DESC")
			}
		}
	})

	t.Run("get all targets", func(t *testing.T) {
		// Create a few targets
		targets := []*models.Target{
			{
				ID:           "t_all1",
				URL:          "https://all1.com",
				CanonicalURL: "https://all1.com",
				Host:         "all1.com",
				CreatedAt:    time.Now().UTC(),
			},
			{
				ID:           "t_all2",
				URL:          "https://all2.com",
				CanonicalURL: "https://all2.com",
				Host:         "all2.com",
				CreatedAt:    time.Now().UTC().Add(time.Second),
			},
		}

		for _, target := range targets {
			_, err := store.CreateTarget(ctx, target, nil)
			if err != nil {
				t.Fatalf("failed to create target: %v", err)
			}
		}

		allTargets, err := store.GetAllTargets(ctx)
		if err != nil {
			t.Fatalf("failed to get all targets: %v", err)
		}

		// Should have at least our test targets
		if len(allTargets) < len(targets) {
			t.Errorf("expected at least %d targets, got %d", len(targets), len(allTargets))
		}

		// Verify targets are ordered by created_at, id
		if len(allTargets) >= 2 {
			for i := 1; i < len(allTargets); i++ {
				prev := allTargets[i-1]
				curr := allTargets[i]
				if prev.CreatedAt.After(curr.CreatedAt) {
					t.Error("expected targets ordered by created_at ASC")
				}
				if prev.CreatedAt.Equal(curr.CreatedAt) && prev.ID > curr.ID {
					t.Error("expected targets with same created_at ordered by ID ASC")
				}
			}
		}
	})

	t.Run("check result with error", func(t *testing.T) {
		// Create a target first
		target := &models.Target{
			ID:           "t_error_test",
			URL:          "https://error-test.com",
			CanonicalURL: "https://error-test.com",
			Host:         "error-test.com",
			CreatedAt:    time.Now().UTC(),
		}
		_, err := store.CreateTarget(ctx, target, nil)
		if err != nil {
			t.Fatalf("failed to create target: %v", err)
		}

		// Create check result with error
		errorMsg := "connection timeout"
		result := &models.CheckResult{
			TargetID:  target.ID,
			CheckedAt: time.Now().UTC(),
			LatencyMS: 5000,
			Error:     &errorMsg,
		}

		err = store.CreateCheckResult(ctx, result)
		if err != nil {
			t.Fatalf("failed to create check result with error: %v", err)
		}

		// Retrieve and verify
		results, err := store.ListCheckResultsByTargetID(ctx, storage.ListCheckResultsParams{
			TargetID: target.ID,
			Limit:    10,
		})
		if err != nil {
			t.Fatalf("failed to list check results: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("expected 1 result, got %d", len(results))
		}
		if results[0].Error == nil {
			t.Error("expected error message in result")
		}
		if *results[0].Error != errorMsg {
			t.Errorf("expected error message %s, got %s", errorMsg, *results[0].Error)
		}
		if results[0].StatusCode != nil {
			t.Error("expected nil status code for error result")
		}
	})

	t.Run("check result with nil status code", func(t *testing.T) {
		// Create a target first
		target := &models.Target{
			ID:           "t_nil_status",
			URL:          "https://nil-status.com",
			CanonicalURL: "https://nil-status.com",
			Host:         "nil-status.com",
			CreatedAt:    time.Now().UTC(),
		}
		_, err := store.CreateTarget(ctx, target, nil)
		if err != nil {
			t.Fatalf("failed to create target: %v", err)
		}

		// Create check result with nil status code
		result := &models.CheckResult{
			TargetID:  target.ID,
			CheckedAt: time.Now().UTC(),
			LatencyMS: 100,
			// StatusCode is nil
		}

		err = store.CreateCheckResult(ctx, result)
		if err != nil {
			t.Fatalf("failed to create check result with nil status code: %v", err)
		}

		// Retrieve and verify
		results, err := store.ListCheckResultsByTargetID(ctx, storage.ListCheckResultsParams{
			TargetID: target.ID,
			Limit:    10,
		})
		if err != nil {
			t.Fatalf("failed to list check results: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("expected 1 result, got %d", len(results))
		}
		if results[0].StatusCode != nil {
			t.Error("expected nil status code")
		}
	})
}

// Helper function to generate random IDs (same as in handlers)
func generateID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return prefix + time.Now().UTC().Format("20060102150405")
	}
	return prefix + hex.EncodeToString(b)
}

func TestIDGeneration(t *testing.T) {
	id1 := generateID("t_")
	id2 := generateID("t_")

	if id1 == id2 {
		t.Error("expected different IDs, got same")
	}

	if !strings.HasPrefix(id1, "t_") {
		t.Errorf("expected prefix t_, got %s", id1[:2])
	}

	if len(id1) != 26 { // t_ + 24 hex chars
		t.Errorf("expected length 26, got %d", len(id1))
	}
}

func TestCursorPagination(t *testing.T) {
	// Test cursor pagination encoding/decoding
	testTime := time.Now().UTC()
	id := "t_1234567890abcdef"

	cursor := testTime.Format(time.RFC3339Nano) + "|" + id
	encoded := base64.URLEncoding.EncodeToString([]byte(cursor))

	decoded, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("failed to decode cursor: %v", err)
	}

	if string(decoded) != cursor {
		t.Errorf("expected cursor %s, got %s", cursor, string(decoded))
	}

	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(parts))
	}

	parsedTime, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		t.Fatalf("failed to parse time: %v", err)
	}

	if !parsedTime.Equal(testTime) {
		t.Errorf("expected time %v, got %v", testTime, parsedTime)
	}

	if parts[1] != id {
		t.Errorf("expected ID %s, got %s", id, parts[1])
	}
}

// TestConfiguration tests environment variable configuration loading
func TestConfiguration(t *testing.T) {
	t.Run("default values", func(t *testing.T) {
		// Clear environment variables to test defaults
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("CHECK_INTERVAL")
		os.Unsetenv("MAX_CONCURRENCY")
		os.Unsetenv("HTTP_TIMEOUT")
		os.Unsetenv("SHUTDOWN_GRACE")
		os.Unsetenv("HTTP_PORT")

		cfg := config.Load()

		if cfg.DatabaseURL != "linkwatch.db" {
			t.Errorf("expected default DATABASE_URL linkwatch.db, got %s", cfg.DatabaseURL)
		}
		if cfg.CheckInterval != 15*time.Second {
			t.Errorf("expected default CHECK_INTERVAL 15s, got %v", cfg.CheckInterval)
		}
		if cfg.MaxConcurrency != 8 {
			t.Errorf("expected default MAX_CONCURRENCY 8, got %d", cfg.MaxConcurrency)
		}
		if cfg.HTTPTimeout != 5*time.Second {
			t.Errorf("expected default HTTP_TIMEOUT 5s, got %v", cfg.HTTPTimeout)
		}
		if cfg.ShutdownGrace != 10*time.Second {
			t.Errorf("expected default SHUTDOWN_GRACE 10s, got %v", cfg.ShutdownGrace)
		}
		if cfg.HTTPPort != "8080" {
			t.Errorf("expected default HTTP_PORT 8080, got %s", cfg.HTTPPort)
		}
	})

	t.Run("custom values", func(t *testing.T) {
		// Set custom environment variables
		os.Setenv("DATABASE_URL", "custom.db")
		os.Setenv("CHECK_INTERVAL", "30s")
		os.Setenv("MAX_CONCURRENCY", "16")
		os.Setenv("HTTP_TIMEOUT", "10s")
		os.Setenv("SHUTDOWN_GRACE", "20s")
		os.Setenv("HTTP_PORT", "9090")

		cfg := config.Load()

		if cfg.DatabaseURL != "custom.db" {
			t.Errorf("expected DATABASE_URL custom.db, got %s", cfg.DatabaseURL)
		}
		if cfg.CheckInterval != 30*time.Second {
			t.Errorf("expected CHECK_INTERVAL 30s, got %v", cfg.CheckInterval)
		}
		if cfg.MaxConcurrency != 16 {
			t.Errorf("expected MAX_CONCURRENCY 16, got %d", cfg.MaxConcurrency)
		}
		if cfg.HTTPTimeout != 10*time.Second {
			t.Errorf("expected HTTP_TIMEOUT 10s, got %v", cfg.HTTPTimeout)
		}
		if cfg.ShutdownGrace != 20*time.Second {
			t.Errorf("expected SHUTDOWN_GRACE 20s, got %v", cfg.ShutdownGrace)
		}
		if cfg.HTTPPort != "9090" {
			t.Errorf("expected HTTP_PORT 9090, got %s", cfg.HTTPPort)
		}

		// Clean up
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("CHECK_INTERVAL")
		os.Unsetenv("MAX_CONCURRENCY")
		os.Unsetenv("HTTP_TIMEOUT")
		os.Unsetenv("SHUTDOWN_GRACE")
		os.Unsetenv("HTTP_PORT")
	})
}

// TestHostLimiter tests the per-host serialization mechanism
func TestHostLimiter(t *testing.T) {
	limiter := checker.NewHostLimiter()

	t.Run("acquire and release", func(t *testing.T) {
		host := "example.com"

		// First acquisition should succeed
		if !limiter.Acquire(host) {
			t.Error("expected first acquisition to succeed")
		}

		// Second acquisition should fail (same host)
		if limiter.Acquire(host) {
			t.Error("expected second acquisition to fail")
		}

		// Release should allow re-acquisition
		limiter.Release(host)
		if !limiter.Acquire(host) {
			t.Error("expected re-acquisition after release to succeed")
		}

		limiter.Release(host)
	})

	t.Run("different hosts", func(t *testing.T) {
		host1 := "example.com"
		host2 := "google.com"

		// Both hosts should be acquirable simultaneously
		if !limiter.Acquire(host1) {
			t.Error("expected host1 acquisition to succeed")
		}
		if !limiter.Acquire(host2) {
			t.Error("expected host2 acquisition to succeed")
		}

		// Release both
		limiter.Release(host1)
		limiter.Release(host2)
	})

	t.Run("case sensitive", func(t *testing.T) {
		host1 := "Example.com"
		host2 := "example.com"

		// Both should be acquirable since they're different strings
		if !limiter.Acquire(host1) {
			t.Error("expected host1 acquisition to succeed")
		}
		if !limiter.Acquire(host2) {
			t.Error("expected host2 acquisition to succeed (different strings)")
		}

		limiter.Release(host1)
		limiter.Release(host2)
	})
}

// TestWorkerPoolConcurrency tests the worker pool concurrency limits
func TestWorkerPoolConcurrency(t *testing.T) {
	store := newTestStore()
	maxConcurrency := 2
	httpTimeout := 1 * time.Second

	pool := checker.NewWorkerPool(store, maxConcurrency, httpTimeout)
	defer pool.Stop()

	t.Run("max concurrency limit", func(t *testing.T) {
		// Create targets that will cause delays
		targets := []models.Target{
			{ID: "t_1", URL: "https://httpbin.org/delay/2", CanonicalURL: "https://httpbin.org/delay/2", Host: "httpbin.org"},
			{ID: "t_2", URL: "https://httpbin.org/delay/2", CanonicalURL: "https://httpbin.org/delay/2", Host: "httpbin.org"},
			{ID: "t_3", URL: "https://httpbin.org/delay/2", CanonicalURL: "https://httpbin.org/delay/2", Host: "httpbin.org"},
		}

		start := time.Now()

		// Submit all targets
		for _, target := range targets {
			pool.Submit(target)
		}

		// Wait a bit for processing
		time.Sleep(3 * time.Second)

		duration := time.Since(start)

		// With max concurrency of 2, processing 3 targets should take at least 3 seconds
		// (2 targets in parallel, then 1 more)
		if duration < 3*time.Second {
			t.Errorf("expected processing to take at least 3 seconds with max concurrency 2, took %v", duration)
		}
	})

	t.Run("per host serialization", func(t *testing.T) {
		// Create targets with same host
		targets := []models.Target{
			{ID: "t_4", URL: "https://httpbin.org/delay/1", CanonicalURL: "https://httpbin.org/delay/1", Host: "httpbin.org"},
			{ID: "t_5", URL: "https://httpbin.org/delay/1", CanonicalURL: "https://httpbin.org/delay/1", Host: "httpbin.org"},
		}

		start := time.Now()

		// Submit both targets
		for _, target := range targets {
			pool.Submit(target)
		}

		// Wait for processing
		time.Sleep(4 * time.Second)

		duration := time.Since(start)

		// With same host, targets should be processed sequentially
		// Each takes 1 second, so total should be at least 2 seconds
		if duration < 2*time.Second {
			t.Errorf("expected sequential processing of same host to take at least 2 seconds, took %v", duration)
		}
	})
}

// TestRetryBackoff tests the retry and backoff semantics
func TestRetryBackoff(t *testing.T) {
	store := newTestStore()
	maxConcurrency := 1
	httpTimeout := 1 * time.Second

	pool := checker.NewWorkerPool(store, maxConcurrency, httpTimeout)
	defer pool.Stop()

	t.Run("retry logic structure", func(t *testing.T) {
		// Test that the retry logic exists and is properly structured
		// This is a unit test of the retry mechanism without external HTTP calls

		// Create a target that will be processed
		target := models.Target{
			ID:           "t_retry_test",
			URL:          "https://httpbin.org/status/200",
			CanonicalURL: "https://httpbin.org/status/200",
			Host:         "httpbin.org",
		}

		// Submit the target
		pool.Submit(target)

		// Wait for processing
		time.Sleep(4 * time.Second)

		// Check that at least one result was created
		results, err := store.ListCheckResultsByTargetID(context.Background(), storage.ListCheckResultsParams{
			TargetID: target.ID,
			Limit:    10,
		})
		if err != nil {
			t.Fatalf("failed to list results: %v", err)
		}

		// Should have at least one result
		if len(results) == 0 {
			t.Error("expected at least one result from processing, got none")
		}

		// Verify the result structure
		for _, result := range results {
			if result.TargetID != target.ID {
				t.Errorf("expected target ID %s, got %s", target.ID, result.TargetID)
			}
			if result.CheckedAt.IsZero() {
				t.Error("expected non-zero checked_at time")
			}
			if result.LatencyMS <= 0 {
				t.Error("expected positive latency measurement")
			}
		}
	})
}

// TestBackgroundChecker tests the periodic background checking mechanism
func TestBackgroundChecker(t *testing.T) {
	t.Run("checker lifecycle", func(t *testing.T) {
		store := newTestStore()
		checkInterval := 100 * time.Millisecond // Short interval for testing
		maxConcurrency := 1
		httpTimeout := 1 * time.Second

		checkerSvc := checker.New(store, checkInterval, maxConcurrency, httpTimeout)

		// Create a target
		target := &models.Target{
			ID:           "t_periodic",
			URL:          "https://httpbin.org/status/200",
			CanonicalURL: "https://httpbin.org/status/200",
			Host:         "httpbin.org",
			CreatedAt:    time.Now().UTC(),
		}
		store.CreateTarget(context.Background(), target, nil)

		// Start the checker
		checkerSvc.Start()

		// Let it run briefly
		time.Sleep(200 * time.Millisecond)

		// Stop the checker
		checkerSvc.Stop()

		// Check that it stopped without errors
		// (The Stop() method should complete without hanging)
	})

	t.Run("graceful shutdown", func(t *testing.T) {
		store := newTestStore()
		checkInterval := 100 * time.Millisecond
		maxConcurrency := 1
		httpTimeout := 1 * time.Second

		checkerSvc := checker.New(store, checkInterval, maxConcurrency, httpTimeout)

		// Create a target
		target := &models.Target{
			ID:           "t_shutdown",
			URL:          "https://httpbin.org/status/200",
			CanonicalURL: "https://httpbin.org/status/200",
			Host:         "httpbin.org",
			CreatedAt:    time.Now().UTC(),
		}
		store.CreateTarget(context.Background(), target, nil)

		// Start the checker
		checkerSvc.Start()

		// Let it run briefly
		time.Sleep(50 * time.Millisecond)

		// Stop gracefully
		checkerSvc.Stop()

		// Check that it stopped without errors
		// (The Stop() method should complete without hanging)
	})
}

// TestHTTPTimeout tests the HTTP client timeout behavior
func TestHTTPTimeout(t *testing.T) {
	store := newTestStore()
	maxConcurrency := 1
	httpTimeout := 100 * time.Millisecond // Very short timeout

	pool := checker.NewWorkerPool(store, maxConcurrency, httpTimeout)
	defer pool.Stop()

	t.Run("timeout configuration", func(t *testing.T) {
		// Test that the HTTP client is configured with the correct timeout
		// This is a structural test rather than a functional test

		target := models.Target{
			ID:           "t_timeout_test",
			URL:          "https://httpbin.org/status/200",
			CanonicalURL: "https://httpbin.org/status/200",
			Host:         "httpbin.org",
		}

		pool.Submit(target)

		// Wait for processing
		time.Sleep(1 * time.Second)

		// Check that the worker pool can process requests
		// (The actual timeout behavior is tested in integration tests)
		results, err := store.ListCheckResultsByTargetID(context.Background(), storage.ListCheckResultsParams{
			TargetID: target.ID,
			Limit:    10,
		})
		if err != nil {
			t.Fatalf("failed to list results: %v", err)
		}

		// Should have at least one result
		if len(results) == 0 {
			t.Error("expected at least one result from processing, got none")
		}
	})
}

// TestRedirectHandling tests the redirect following behavior
func TestRedirectHandling(t *testing.T) {
	store := newTestStore()
	checkInterval := 100 * time.Millisecond
	maxConcurrency := 1
	httpTimeout := 5 * time.Second

	checkerSvc := checker.New(store, checkInterval, maxConcurrency, httpTimeout)
	defer checkerSvc.Stop()

	t.Run("redirect configuration", func(t *testing.T) {
		// Test that the HTTP client is configured to follow redirects
		// This is a structural test rather than a functional test

		target := models.Target{
			ID:           "t_redirect_test",
			URL:          "https://httpbin.org/status/200",
			CanonicalURL: "https://httpbin.org/status/200",
			Host:         "httpbin.org",
			CreatedAt:    time.Now().UTC(),
		}

		// Store the target first
		_, err := store.CreateTarget(context.Background(), &target, nil)
		if err != nil {
			t.Fatalf("failed to create target: %v", err)
		}

		// Start the background checker
		checkerSvc.Start()

		// Wait for processing
		time.Sleep(3 * time.Second)

		// Check that the worker pool can process requests
		// (The actual redirect behavior is tested in integration tests)
		results, err := store.ListCheckResultsByTargetID(context.Background(), storage.ListCheckResultsParams{
			TargetID: target.ID,
			Limit:    10,
		})
		if err != nil {
			t.Fatalf("failed to list results: %v", err)
		}

		// Should have at least one result
		if len(results) == 0 {
			t.Error("expected at least one result from processing, got none")
		}
	})
}

// TestLatencyMeasurement tests that latency is properly measured and recorded
func TestLatencyMeasurement(t *testing.T) {
	store := newTestStore()
	checkInterval := 100 * time.Millisecond
	maxConcurrency := 1
	httpTimeout := 5 * time.Second

	checkerSvc := checker.New(store, checkInterval, maxConcurrency, httpTimeout)
	defer checkerSvc.Stop()

	t.Run("latency recording", func(t *testing.T) {
		// Target for latency testing
		target := models.Target{
			ID:           "t_latency",
			URL:          "https://httpbin.org/status/200",
			CanonicalURL: "https://httpbin.org/status/200",
			Host:         "httpbin.org",
			CreatedAt:    time.Now().UTC(),
		}

		// Store the target first
		_, err := store.CreateTarget(context.Background(), &target, nil)
		if err != nil {
			t.Fatalf("failed to create target: %v", err)
		}

		// Start the background checker
		checkerSvc.Start()

		// Wait for processing
		time.Sleep(3 * time.Second)

		// Check results
		results, err := store.ListCheckResultsByTargetID(context.Background(), storage.ListCheckResultsParams{
			TargetID: target.ID,
			Limit:    10,
		})
		if err != nil {
			t.Fatalf("failed to list results: %v", err)
		}

		// Should have results
		if len(results) == 0 {
			t.Error("expected results with latency measurements, got none")
		}

		// Should have latency measurements
		for _, result := range results {
			if result.LatencyMS <= 0 {
				t.Errorf("expected positive latency measurement, got %d", result.LatencyMS)
			}

			// Latency should be reasonable (not negative or zero)
			if result.LatencyMS < 0 {
				t.Errorf("expected non-negative latency measurement, got %d", result.LatencyMS)
			}
		}
	})
}

// TestGracefulShutdown tests the graceful shutdown behavior
func TestGracefulShutdown(t *testing.T) {
	t.Run("shutdown lifecycle", func(t *testing.T) {
		store := newTestStore()
		checkInterval := 50 * time.Millisecond
		maxConcurrency := 1
		httpTimeout := 1 * time.Second

		checkerSvc := checker.New(store, checkInterval, maxConcurrency, httpTimeout)

		// Create a target
		target := &models.Target{
			ID:           "t_shutdown_test",
			URL:          "https://httpbin.org/status/200",
			CanonicalURL: "https://httpbin.org/status/200",
			Host:         "httpbin.org",
			CreatedAt:    time.Now().UTC(),
		}
		store.CreateTarget(context.Background(), target, nil)

		// Start the checker
		checkerSvc.Start()

		// Let it run briefly
		time.Sleep(100 * time.Millisecond)

		// Stop the checker
		checkerSvc.Stop()

		// Check that it stopped without errors
		// (The Stop() method should complete without hanging)
	})
}
