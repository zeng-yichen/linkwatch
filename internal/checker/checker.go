package checker

import (
	"context"
	"log"
	"sync"
	"time"

	"linkwatch/internal/storage"
)

// Checker is responsible for periodically scheduling URL checks.
type Checker struct {
	store         storage.Storer
	pool          *WorkerPool
	checkInterval time.Duration
	stopChan      chan struct{}
	wg            sync.WaitGroup
}

// New creates a new Checker.
func New(store storage.Storer, interval time.Duration, maxConcurrency int, httpTimeout time.Duration) *Checker {
	return &Checker{
		store:         store,
		pool:          NewWorkerPool(store, maxConcurrency, httpTimeout),
		checkInterval: interval,
		stopChan:      make(chan struct{}),
	}
}

// Start begins the periodic checking process.
func (c *Checker) Start() {
	log.Printf("starting background checker with interval: %s", c.checkInterval)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(c.checkInterval)
		defer ticker.Stop()

		// Perform an initial check on startup
		c.scheduleChecks()

		for {
			select {
			case <-ticker.C:
				c.scheduleChecks()
			case <-c.stopChan:
				log.Println("stopping background checker...")
				c.pool.Stop() // Stop the worker pool
				return
			}
		}
	}()
}

// Stop gracefully shuts down the checker and its worker pool.
func (c *Checker) Stop() {
	close(c.stopChan)
	c.wg.Wait()
	log.Println("background checker stopped")
}

// scheduleChecks fetches all targets and dispatches them to the worker pool.
func (c *Checker) scheduleChecks() {
	log.Println("scheduling checks for all targets...")
	targets, err := c.store.GetAllTargets(context.Background())
	if err != nil {
		log.Printf("error fetching targets for checking: %v", err)
		return
	}

	if len(targets) == 0 {
		log.Println("no targets to check")
		return
	}

	for _, t := range targets {
		c.pool.Submit(t)
	}
	log.Printf("submitted %d targets for checking", len(targets))
}
