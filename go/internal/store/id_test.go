package store

import (
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Records created in the same clock tick must not share an ID; a collision
// silently replaces the earlier record (seen as a flaky log-stream test on
// macOS, whose clock has microsecond resolution).
func TestIDsAreUniqueAndIncreasingUnderConcurrency(t *testing.T) {
	const workers, each = 8, 5000
	var mu sync.Mutex
	seen := make(map[string]bool, workers*each)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			previous := int64(0)
			local := make([]string, 0, each)
			for range each {
				value := id("run")
				n, err := strconv.ParseInt(strings.TrimPrefix(value, "run-"), 10, 64)
				if err != nil || n <= previous {
					t.Errorf("id %q is not increasing after %d", value, previous)
					return
				}
				previous = n
				local = append(local, value)
			}
			mu.Lock()
			defer mu.Unlock()
			for _, value := range local {
				if seen[value] {
					t.Errorf("duplicate id %s", value)
				}
				seen[value] = true
			}
		}()
	}
	wg.Wait()
	if len(seen) != workers*each {
		t.Fatalf("got %d unique ids, want %d", len(seen), workers*each)
	}
}
