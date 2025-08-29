package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"linkwatch/internal/models"
	"linkwatch/internal/storage"
	"linkwatch/internal/urlutil"
)

// Handlers holds dependencies for the API handlers.
type Handlers struct {
	store storage.Storer
}

// NewHandlers creates a new Handlers struct.
func NewHandlers(store storage.Storer) *Handlers {
	return &Handlers{store: store}
}

func generateID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return prefix + time.Now().UTC().Format("20060102150405")
	}
	return prefix + hex.EncodeToString(b)
}

// CreateTarget handles the creation of a new target.
func (h *Handlers) CreateTarget(w http.ResponseWriter, r *http.Request) {
	// 1. Parse request body
	var reqBody struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// 2. Canonicalize URL
	canonicalURL, err := urlutil.Canonicalize(reqBody.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 3. Parse URL to get host
	parsedURL, _ := url.Parse(canonicalURL)

	// 4. Create target
	target := &models.Target{
		ID:           generateID("t_"),
		URL:          reqBody.URL,
		CanonicalURL: canonicalURL,
		Host:         parsedURL.Hostname(),
		CreatedAt:    time.Now().UTC(),
	}

	// 5. Handle idempotency key
	idempotencyKey := r.Header.Get("Idempotency-Key")
	var keyPtr *string
	if idempotencyKey != "" {
		keyPtr = &idempotencyKey
	}

	// 6. Create the target
	createdTarget, err := h.store.CreateTarget(r.Context(), target, keyPtr)
	if err != nil && !errors.Is(err, storage.ErrDuplicateKey) {
		log.Printf("error creating target: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// 7. Set the status code
	statusCode := http.StatusCreated
	if errors.Is(err, storage.ErrDuplicateKey) {
		statusCode = http.StatusOK
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(createdTarget)
}

// ListTargets handles listing targets with pagination.
func (h *Handlers) ListTargets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 500 {
			limit = v
		}
	}
	// host filter (case-insensitive)
	host := strings.ToLower(strings.TrimSpace(q.Get("host")))

	var afterTime time.Time
	var afterID string
	if token := q.Get("page_token"); token != "" {
		// token is base64 of "<rfc3339nano>|<id>"
		if decoded, err := base64.URLEncoding.DecodeString(token); err == nil {
			parts := strings.SplitN(string(decoded), "|", 2)
			if len(parts) == 2 {
				if t, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
					afterTime = t
					afterID = parts[1]
				}
			}
		}
	}

	items, err := h.store.ListTargets(r.Context(), storage.ListTargetsParams{
		Host:      host,
		AfterTime: afterTime,
		AfterID:   afterID,
		Limit:     limit,
	})
	if err != nil {
		log.Printf("list targets error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	resp := struct {
		Items         []models.Target `json:"items"`
		NextPageToken string          `json:"next_page_token"`
	}{
		Items: items,
	}

	if len(items) == limit {
		last := items[len(items)-1]
		cursor := last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
		resp.NextPageToken = base64.URLEncoding.EncodeToString([]byte(cursor))
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ListCheckResults handles listing check results for a target.
func (h *Handlers) ListCheckResults(w http.ResponseWriter, r *http.Request) {
	// path: /v1/targets/{target_id}/results
	path := r.URL.Path
	parts := strings.Split(path, "/")
	if len(parts) < 5 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	targetID := parts[3]

	// ensure target exists
	if _, err := h.store.GetTargetByID(r.Context(), targetID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "target not found", http.StatusNotFound)
			return
		}
		log.Printf("get target error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()
	limit := 100
	if l := q.Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 1000 {
			limit = v
		}
	}

	var sincePtr *time.Time
	if s := q.Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			utc := t.UTC()
			sincePtr = &utc
		}
	}

	results, err := h.store.ListCheckResultsByTargetID(r.Context(), storage.ListCheckResultsParams{
		TargetID: targetID,
		Since:    sincePtr,
		Limit:    limit,
	})
	if err != nil {
		log.Printf("list results error: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	resp := struct {
		Items []models.CheckResult `json:"items"`
	}{Items: results}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Healthz is a simple health check endpoint.
func (h *Handlers) Healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
