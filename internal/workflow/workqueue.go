package workflow

import (
	"context"
	"log/slog"
	"sync"
)

// workItem represents one library item that needs a search pass.
type workItem struct {
	libraryItemID int64
	priority      int // higher = processed first; 0 = normal, 10 = webhook-triggered
}

// WorkQueue is a goroutine-safe priority queue for library item search work.
// High-priority items (webhook-triggered) are placed at the front; normal
// items at the back. A pool of workers drains the queue concurrently.
type WorkQueue struct {
	mu      sync.Mutex
	items   []workItem
	seen    map[int64]int
	signal  chan struct{}
	workers int
}

// NewWorkQueue creates a work queue. Call Start() to begin processing.
func NewWorkQueue(workers int) *WorkQueue {
	if workers < 1 {
		workers = 1
	}
	return &WorkQueue{
		signal:  make(chan struct{}, 1),
		seen:    make(map[int64]int),
		workers: workers,
	}
}

// Push adds a library item to the queue. High-priority items go to the front.
func (q *WorkQueue) Push(libraryItemID int64, priority int) {
	q.mu.Lock()
	if existingPriority, ok := q.seen[libraryItemID]; ok {
		if priority > existingPriority {
			for i := range q.items {
				if q.items[i].libraryItemID == libraryItemID {
					q.items[i].priority = priority
					if i > 0 {
						item := q.items[i]
						copy(q.items[1:i+1], q.items[0:i])
						q.items[0] = item
					}
					break
				}
			}
			q.seen[libraryItemID] = priority
		}
		q.mu.Unlock()
		return
	}
	item := workItem{libraryItemID: libraryItemID, priority: priority}
	if priority > 0 {
		q.items = append([]workItem{item}, q.items...)
	} else {
		q.items = append(q.items, item)
	}
	q.seen[libraryItemID] = priority
	q.mu.Unlock()
	// Signal workers non-blockingly.
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// pop removes and returns the next item, or false if empty.
func (q *WorkQueue) pop() (workItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return workItem{}, false
	}
	item := q.items[0]
	q.items = q.items[1:]
	delete(q.seen, item.libraryItemID)
	return item, true
}

// Depth returns the current queue depth.
func (q *WorkQueue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Start launches worker goroutines and returns a stop function.
// fn is called once per dequeued item.
func (q *WorkQueue) Start(ctx context.Context, fn func(ctx context.Context, libraryItemID int64)) {
	sem := make(chan struct{}, q.workers)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-q.signal:
				for {
					item, ok := q.pop()
					if !ok {
						break
					}
					sem <- struct{}{} // limit concurrency
					go func(it workItem) {
						defer func() { <-sem }()
						slog.Debug("workqueue: processing library item", "library_item_id", it.libraryItemID, "priority", it.priority)
						fn(ctx, it.libraryItemID)
					}(item)
				}
			}
		}
	}()
}
