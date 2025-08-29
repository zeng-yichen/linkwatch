package checker

import (
	"context"
	"crypto/tls"
	"log"
	"net/http"
	"sync"
	"time"

	"linkwatch/internal/models"
	"linkwatch/internal/storage"
)

// WorkerPool manages a pool of goroutines to perform HTTP checks concurrently.
type WorkerPool struct {
	store       storage.Storer
	jobs        chan models.Target
	httpClient  *http.Client
	hostLimiter *HostLimiter
	wg          sync.WaitGroup
	stopOnce    sync.Once
}

// NewWorkerPool creates a new worker pool.
func NewWorkerPool(store storage.Storer, maxConcurrency int, httpTimeout time.Duration) *WorkerPool {
	pool := &WorkerPool{
		store:       store,
		jobs:        make(chan models.Target, maxConcurrency*2),
		hostLimiter: NewHostLimiter(),
		httpClient: &http.Client{
			Timeout: httpTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}

	pool.startWorkers(maxConcurrency)
	return pool
}

// startWorkers launches the worker goroutines.
func (p *WorkerPool) startWorkers(count int) {
	p.wg.Add(count)
	for i := 0; i < count; i++ {
		go func() {
			defer p.wg.Done()
			for target := range p.jobs {
				p.performCheck(target)
			}
		}()
	}
}

// Submit adds a target to the job queue for checking.
func (p *WorkerPool) Submit(target models.Target) {
	select {
	case p.jobs <- target:
	default:
		log.Printf("job queue full, skipping check for target %s", target.ID)
	}
}

// Stop gracefully stops all workers.
func (p *WorkerPool) Stop() {
	p.stopOnce.Do(func() {
		close(p.jobs)
		p.wg.Wait()
	})
}

// performCheck executes the HTTP check for a single target.
func (p *WorkerPool) performCheck(target models.Target) {
	if !p.hostLimiter.Acquire(target.Host) {
		log.Printf("skipping check for %s, host %s is already being checked", target.URL, target.Host)
		return
	}
	defer p.hostLimiter.Release(target.Host)

	attempts := 0
	maxAttempts := 3
	backoff := 200 * time.Millisecond

	var statusCode *int
	var errMsg *string
	var startTime time.Time
	var latency time.Duration

	retry := func(code int, err error) bool {
		if err != nil {
			return true
		}
		return code >= 500 && code <= 599
	}

	for {
		attempts++
		startTime = time.Now()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target.CanonicalURL, nil)
		if err != nil {
			m := err.Error()
			errMsg = &m
			break
		}

		resp, err := p.httpClient.Do(req)
		latency = time.Since(startTime)
		if err != nil {
			m := err.Error()
			errMsg = &m
		} else {
			status := resp.StatusCode
			statusCode = &status
			resp.Body.Close()
		}

		code := 0
		if statusCode != nil {
			code = *statusCode
		}
		if attempts < maxAttempts && retry(code, err) {
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		break
	}

	result := models.CheckResult{
		ID:         "", // DB/storage layer may set ID; not required in interface
		TargetID:   target.ID,
		CheckedAt:  startTime,
		LatencyMS:  latency.Milliseconds(),
		StatusCode: statusCode,
		Error:      errMsg,
	}
	if dbErr := p.store.CreateCheckResult(context.Background(), &result); dbErr != nil {
		log.Printf("error saving check result for target %s: %v", target.ID, dbErr)
	}
}
