package cli

// Bounded fan-out for commands that ask the API the same question about many
// projects (container list, volume list, vm list, stats, db/realtime/stream
// list, storage list, analytics sites list, ...). One pool, used everywhere.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// projectFanoutWorkers is how many per-project requests run at once.
const projectFanoutWorkers = 8

// fanoutRateLimitBackoff is how long the whole pool pauses after a 429 that
// outlived the HTTP layer's own retries, before the item is tried once more.
var fanoutRateLimitBackoff = 2 * time.Second

// runPool runs fn for indexes [0, count) with at most workers calls in flight
// and returns the errors by index. Once the root context is cancelled
// (Ctrl-C) the indexes not yet started get its error instead of running.
func runPool(count, workers int, fn func(index int) error) []error {
	errs := make([]error, count)
	if count == 0 {
		return errs
	}
	if workers < 1 {
		workers = 1
	}
	if workers > count {
		workers = count
	}
	ctx := RootContext()
	next := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range next {
				if ctxErr := ctx.Err(); ctxErr != nil {
					errs[index] = ctxErr
					continue
				}
				errs[index] = fn(index)
			}
		}()
	}
	for index := 0; index < count; index++ {
		next <- index
	}
	close(next)
	wg.Wait()
	return errs
}

// fanOut is runPool for read-only lookups: an item answered with 429 pauses
// the whole pool for a short backoff and is retried once. Each request keeps
// its own timeout (--timeout / HUBFLY_TIMEOUT through RequestTimeout).
func fanOut(count int, fn func(index int) error) []error {
	ctx := RootContext()
	var mu sync.Mutex
	var pausedUntil time.Time
	waitForPause := func() error {
		mu.Lock()
		until := pausedUntil
		mu.Unlock()
		if wait := time.Until(until); wait > 0 {
			return SleepContext(ctx, wait)
		}
		return nil
	}
	return runPool(count, projectFanoutWorkers, func(index int) error {
		if err := waitForPause(); err != nil {
			return err
		}
		err := fn(index)
		if !isRateLimited(err) {
			return err
		}
		mu.Lock()
		if until := time.Now().Add(fanoutRateLimitBackoff); until.After(pausedUntil) {
			pausedUntil = until
		}
		mu.Unlock()
		debugf("fan-out item %d rate limited; backing off %s and retrying once", index, fanoutRateLimitBackoff)
		if sleepErr := waitForPause(); sleepErr != nil {
			return sleepErr
		}
		return fn(index)
	})
}

func isRateLimited(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusTooManyRequests
	}
	var statusErr *httpStatusError
	return errors.As(err, &statusErr) && statusErr.Status == http.StatusTooManyRequests
}

// fanOutProjects calls fn for every project concurrently (bounded) and
// returns the results and errors in the order of projects, so callers print
// exactly what a sequential loop would have printed.
func fanOutProjects[T any](projects []project, fn func(item project) (T, error)) ([]T, []error) {
	results := make([]T, len(projects))
	errs := fanOut(len(projects), func(index int) error {
		value, err := fn(projects[index])
		if err != nil {
			return err
		}
		results[index] = value
		return nil
	})
	return results, errs
}
