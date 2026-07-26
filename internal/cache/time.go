package cache

import "time"

// unixNow returns the current time, used by FileCache.Get to refresh a
// cached file's mtime on access so it sorts as most-recently-used.
func unixNow() time.Time {
	return time.Now()
}
