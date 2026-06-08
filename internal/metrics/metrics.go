package metrics

import "sync/atomic"

// M is the global metrics registry. All fields are safe for concurrent access.
var M Registry

type Registry struct {
	// Streaming
	ActiveStreams atomic.Int64

	// NNTP
	NNTPArticlesFetched atomic.Int64
	NNTPBytesFetched    atomic.Int64
	NNTPFetchFailures   atomic.Int64

	// Cache
	CacheHits      atomic.Int64
	CacheMisses    atomic.Int64
	CacheEvictions atomic.Int64

	// Read-ahead
	ReadAheadCancellations atomic.Int64

	// Publication
	PublishedVirtualFiles   atomic.Int64
	FallbackReleaseAttempts atomic.Int64

	// Subtitles
	SubtitleDownloads atomic.Int64
	SubtitleFailures  atomic.Int64
}

type Snapshot struct {
	ActiveStreams            int64 `json:"active_streams"`
	ActiveNNTPConnections   int64 `json:"active_nntp_connections"`
	IdleNNTPConnections     int64 `json:"idle_nntp_connections"`
	QueuedInteractiveFetches int64 `json:"queued_interactive_fetches"`
	QueuedBackgroundFetches  int64 `json:"queued_background_fetches"`
	CacheMemoryBytes         int64 `json:"cache_memory_bytes"`
	CacheDiskBytes           int64 `json:"cache_disk_bytes"`
	CacheHitsTotal           int64 `json:"cache_hits_total"`
	CacheMissesTotal         int64 `json:"cache_misses_total"`
	CacheEvictionsTotal      int64 `json:"cache_evictions_total"`
	NNTPArticlesFetchedTotal int64 `json:"nntp_articles_fetched_total"`
	NNTPBytesFetchedTotal    int64 `json:"nntp_bytes_fetched_total"`
	NNTPFetchFailuresTotal   int64 `json:"nntp_fetch_failures_total"`
	ReadAheadCancellations   int64 `json:"read_ahead_cancellations_total"`
	PublishedVirtualFiles    int64 `json:"published_virtual_files_total"`
	FallbackReleaseAttempts  int64 `json:"fallback_release_attempts_total"`
	SubtitleDownloadsTotal   int64 `json:"subtitle_downloads_total"`
	SubtitleFailuresTotal    int64 `json:"subtitle_failures_total"`
}

type NNTPStats struct {
	Active int64
	Idle   int64
}

type CacheStats struct {
	MemoryBytes int64
	DiskBytes   int64
}

type QueueStats struct {
	Interactive int64
	Background  int64
}

// Collect assembles a Snapshot from atomic counters and live runtime stats.
func (r *Registry) Collect(nntp NNTPStats, cache CacheStats, queued QueueStats) Snapshot {
	return Snapshot{
		ActiveStreams:             r.ActiveStreams.Load(),
		ActiveNNTPConnections:    nntp.Active,
		IdleNNTPConnections:      nntp.Idle,
		QueuedInteractiveFetches: queued.Interactive,
		QueuedBackgroundFetches:  queued.Background,
		CacheMemoryBytes:         cache.MemoryBytes,
		CacheDiskBytes:           cache.DiskBytes,
		CacheHitsTotal:           r.CacheHits.Load(),
		CacheMissesTotal:         r.CacheMisses.Load(),
		CacheEvictionsTotal:      r.CacheEvictions.Load(),
		NNTPArticlesFetchedTotal: r.NNTPArticlesFetched.Load(),
		NNTPBytesFetchedTotal:    r.NNTPBytesFetched.Load(),
		NNTPFetchFailuresTotal:   r.NNTPFetchFailures.Load(),
		ReadAheadCancellations:   r.ReadAheadCancellations.Load(),
		PublishedVirtualFiles:    r.PublishedVirtualFiles.Load(),
		FallbackReleaseAttempts:  r.FallbackReleaseAttempts.Load(),
		SubtitleDownloadsTotal:   r.SubtitleDownloads.Load(),
		SubtitleFailuresTotal:    r.SubtitleFailures.Load(),
	}
}
