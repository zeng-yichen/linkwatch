package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"linkwatch/internal/api"
	"linkwatch/internal/models"
	"linkwatch/internal/storage"
	"linkwatch/internal/storage/sqlite"
	"linkwatch/internal/urlutil"
)

// Simple in-memory storage for testing
type testStore struct {
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
	if t, ok := s.targets[id]; ok {
		return &t, nil
	}
	return nil, storage.ErrNotFound
}

func (s *testStore) ListTargets(ctx context.Context, params storage.ListTargetsParams) ([]models.Target, error) {
	var targets []models.Target
	for _, t := range s.targets {
		if params.Host != "" && strings.ToLower(t.Host) != strings.ToLower(params.Host) {
			continue
		}
		targets = append(targets, t)
	}
	// Simple sorting - in real implementation would be more sophisticated
	if len(targets) > params.Limit {
		return targets[:params.Limit], nil
	}
	return targets, nil
}

func (s *testStore) GetAllTargets(ctx context.Context) ([]models.Target, error) {
	var targets []models.Target
	for _, t := range s.targets {
		targets = append(targets, t)
	}
	return targets, nil
}

func (s *testStore) CreateCheckResult(ctx context.Context, result *models.CheckResult) error {
	s.results[result.TargetID] = append(s.results[result.TargetID], *result)
	return nil
}

func (s *testStore) ListCheckResultsByTargetID(ctx context.Context, params storage.ListCheckResultsParams) ([]models.CheckResult, error) {
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
