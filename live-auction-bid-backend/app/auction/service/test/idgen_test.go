package test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"live-auction-bid/backend/app/auction/service/internal/pkg/idgen"
)

func TestIDGenNewReturnsSnowflakeString(t *testing.T) {
	id := idgen.New("lot")
	if !isPositiveDecimalID(id) {
		t.Fatalf("expected decimal snowflake id, got %q", id)
	}

	next := idgen.New("bid")
	if !isPositiveDecimalID(next) {
		t.Fatalf("expected decimal snowflake id, got %q", next)
	}
	if parseID(t, next) <= parseID(t, id) {
		t.Fatalf("expected sequential ids to increase, first=%s next=%s", id, next)
	}
}

func TestIDGenNewUserIDReturnsSnowflakeString(t *testing.T) {
	userID := idgen.NewUserID()
	if !isPositiveDecimalID(userID) {
		t.Fatalf("expected decimal user snowflake id, got %q", userID)
	}
}

func TestSnowflakeGeneratorIncrementsWithinSameMillisecond(t *testing.T) {
	nowMs := time.Now().UnixMilli()
	g, err := idgen.NewGenerator(42, idgen.WithNowFunc(func() int64 {
		return nowMs
	}))
	if err != nil {
		t.Fatalf("new generator failed: %v", err)
	}

	var previous int64
	for i := 0; i < 32; i++ {
		current := g.NextID()
		if i > 0 && current <= previous {
			t.Fatalf("expected ids to increase in same millisecond, previous=%d current=%d", previous, current)
		}
		previous = current
	}
}

func TestSnowflakeGeneratorHandlesClockRollback(t *testing.T) {
	baseMs := time.Now().UnixMilli()
	times := []int64{baseMs + 2, baseMs + 1, baseMs}
	index := 0
	g, err := idgen.NewGenerator(7, idgen.WithNowFunc(func() int64 {
		if index >= len(times) {
			return times[len(times)-1]
		}
		nowMs := times[index]
		index++
		return nowMs
	}))
	if err != nil {
		t.Fatalf("new generator failed: %v", err)
	}

	first := g.NextID()
	second := g.NextID()
	third := g.NextID()
	if first >= second || second >= third {
		t.Fatalf("expected monotonic ids during rollback, got %d, %d, %d", first, second, third)
	}
}

func TestSnowflakeGeneratorRejectsInvalidWorkerID(t *testing.T) {
	if _, err := idgen.NewGenerator(-1); err == nil {
		t.Fatal("expected negative worker id to fail")
	}
	if _, err := idgen.NewGenerator(1024); err == nil {
		t.Fatal("expected worker id above 1023 to fail")
	}
}

func TestIDGenNewIsConcurrentSafe(t *testing.T) {
	const goroutines = 32
	const perGoroutine = 128

	start := make(chan struct{})
	errs := make(chan string, goroutines*perGoroutine)
	ids := make(map[string]struct{}, goroutines*perGoroutine)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < perGoroutine; j++ {
				id := idgen.New("evt")
				if !isPositiveDecimalID(id) {
					errs <- "invalid id: " + id
					continue
				}
				mu.Lock()
				if _, found := ids[id]; found {
					errs <- "duplicate id: " + id
				}
				ids[id] = struct{}{}
				mu.Unlock()
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
	if len(ids) != goroutines*perGoroutine {
		t.Fatalf("expected %d unique ids, got %d", goroutines*perGoroutine, len(ids))
	}
}

func isPositiveDecimalID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	value, err := strconv.ParseInt(id, 10, 64)
	return err == nil && value > 0
}

func parseID(t *testing.T, id string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("parse id %q failed: %v", id, err)
	}
	return value
}
