package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func fanoutTestProjects(count int) []project {
	projects := make([]project, count)
	for i := range projects {
		projects[i] = project{ID: fmt.Sprintf("prj_%02d", i), Name: fmt.Sprintf("project-%02d", i)}
	}
	return projects
}

func TestFanOutProjectsPreservesOrder(t *testing.T) {
	projects := fanoutTestProjects(40)
	results, errs := fanOutProjects(projects, func(item project) (string, error) {
		// Later projects answer first, so completion order is reversed.
		var index int
		_, _ = fmt.Sscanf(item.ID, "prj_%d", &index)
		time.Sleep(time.Duration(40-index) * time.Millisecond / 4)
		return "result of " + item.ID, nil
	})
	if len(results) != len(projects) || len(errs) != len(projects) {
		t.Fatalf("got %d results, %d errors for %d projects", len(results), len(errs), len(projects))
	}
	for i, item := range projects {
		if errs[i] != nil {
			t.Fatalf("project %d: unexpected error %v", i, errs[i])
		}
		if want := "result of " + item.ID; results[i] != want {
			t.Fatalf("result %d = %q, want %q", i, results[i], want)
		}
	}
}

func TestFanOutBoundsConcurrency(t *testing.T) {
	var inFlight, peak, calls atomic.Int64
	errs := fanOut(60, func(int) error {
		current := inFlight.Add(1)
		for {
			seen := peak.Load()
			if current <= seen || peak.CompareAndSwap(seen, current) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		calls.Add(1)
		return nil
	})
	if len(errs) != 60 || calls.Load() != 60 {
		t.Fatalf("ran %d of 60 calls (%d errors slots)", calls.Load(), len(errs))
	}
	if got := peak.Load(); got > projectFanoutWorkers {
		t.Fatalf("peak concurrency %d exceeds the bound %d", got, projectFanoutWorkers)
	}
	if got := peak.Load(); got < 2 {
		t.Fatalf("peak concurrency %d: the pool did not run concurrently", got)
	}
}

func TestFanOutProjectsIsolatesErrors(t *testing.T) {
	projects := fanoutTestProjects(12)
	boom := errors.New("boom")
	results, errs := fanOutProjects(projects, func(item project) (int, error) {
		if item.ID == "prj_03" || item.ID == "prj_09" {
			return 0, boom
		}
		return len(item.Name), nil
	})
	for i := range projects {
		failed := i == 3 || i == 9
		if failed {
			if !errors.Is(errs[i], boom) {
				t.Fatalf("project %d: error %v, want boom", i, errs[i])
			}
			if results[i] != 0 {
				t.Fatalf("project %d: failed project kept result %d", i, results[i])
			}
			continue
		}
		if errs[i] != nil || results[i] != len(projects[i].Name) {
			t.Fatalf("project %d: result %d, error %v", i, results[i], errs[i])
		}
	}
}

func TestFanOutRetriesRateLimitOnce(t *testing.T) {
	previous := fanoutRateLimitBackoff
	fanoutRateLimitBackoff = 20 * time.Millisecond
	defer func() { fanoutRateLimitBackoff = previous }()

	var calls [3]atomic.Int64
	started := time.Now()
	errs := fanOut(3, func(index int) error {
		attempt := calls[index].Add(1)
		switch {
		case index == 0 && attempt == 1:
			return &apiError{Status: http.StatusTooManyRequests, Message: "slow down"}
		case index == 1:
			return &apiError{Status: http.StatusTooManyRequests, Message: "slow down"}
		case index == 2:
			return &apiError{Status: http.StatusForbidden, Message: "no"}
		}
		return nil
	})
	if errs[0] != nil || calls[0].Load() != 2 {
		t.Fatalf("item 0: error %v after %d calls, want success on the retry", errs[0], calls[0].Load())
	}
	if !isRateLimited(errs[1]) || calls[1].Load() != 2 {
		t.Fatalf("item 1: error %v after %d calls, want one retry then the 429", errs[1], calls[1].Load())
	}
	if errs[2] == nil || calls[2].Load() != 1 {
		t.Fatalf("item 2: error %v after %d calls, want no retry of a 403", errs[2], calls[2].Load())
	}
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("retried after %s, want a backoff of at least 20ms", elapsed)
	}
}

func TestRunPoolStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	previous := RootContext()
	SetRootContext(ctx)
	defer SetRootContext(previous)

	var calls atomic.Int64
	errs := runPool(50, 2, func(int) error {
		if calls.Add(1) == 2 {
			cancel()
		}
		time.Sleep(2 * time.Millisecond)
		return nil
	})
	if got := calls.Load(); got >= 50 {
		t.Fatalf("all %d calls ran after cancellation", got)
	}
	if !errors.Is(errs[len(errs)-1], context.Canceled) {
		t.Fatalf("last error = %v, want context.Canceled", errs[len(errs)-1])
	}
	if got := runPool(0, 4, func(int) error { return nil }); len(got) != 0 {
		t.Fatalf("empty pool returned %d errors", len(got))
	}
}
