package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSingleFlightDeduplicates(t *testing.T) {
	sf := NewSingleFlight()
	var calls atomic.Int32
	start := make(chan struct{})
	fn := func(ctx context.Context) ([]byte, error) {
		calls.Add(1)
		<-start
		time.Sleep(20 * time.Millisecond)
		return []byte("ok"), nil
	}

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := sf.Do(context.Background(), "block-1", fn)
			if err != nil {
				t.Error(err)
			}
			if string(data) != "ok" {
				t.Errorf("unexpected data %q", data)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 underlying call, got %d", got)
	}
}
