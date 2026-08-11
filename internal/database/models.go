package database

import "time"

// QueueState represents the pipeline stage of a queue_items row, progressing
// from the initial request through search, ranking, selection, download, and
// publishing (or degraded/failed on error).
type QueueState string

const (
	QueueRequested   QueueState = "requested"
	QueueSearching   QueueState = "searching"
	QueueRanking     QueueState = "ranking"
	QueueSelected    QueueState = "selected"
	QueueFetchingNZB QueueState = "fetching_nzb"
	QueueIndexing    QueueState = "indexing"
	QueuePreflight   QueueState = "preflight"
	QueuePublishing  QueueState = "publishing"
	QueueAvailable   QueueState = "available"
	QueueDegraded    QueueState = "degraded"
	QueueFailed      QueueState = "failed"
)

// QueueSnapshot is a read model combining a queue_items row with its library
// item and (if selected) NZB metadata, used by the dashboard/API layer.
type QueueSnapshot struct {
	QueueItemID     int64      `json:"queueItemId"`
	LibraryItemID   int64      `json:"libraryItemId"`
	LibraryTitle    string     `json:"libraryTitle"`
	State           QueueState `json:"state"`
	FailureReason   string     `json:"failureReason"`
	IdempotencyKey  string     `json:"idempotencyKey"`
	SelectedRelease *int64     `json:"selectedReleaseId,omitempty"`
	NZBDocumentID   *int64     `json:"nzbDocumentId,omitempty"`
	NZBFileName     string     `json:"nzbFileName,omitempty"`
	NZBFileCount    int        `json:"nzbFileCount"`
	NZBSegmentCount int        `json:"nzbSegmentCount"`
	SeasonNumber    *int       `json:"seasonNumber,omitempty"`
	EpisodeNumber   *int       `json:"episodeNumber,omitempty"`
	OnHold          bool       `json:"onHold"`
	// DispatchAttemptCount/DispatchBackoffUntil reflect the passive-resume
	// dispatch sweep's escalating per-item backoff (see
	// workflow.RecordDispatchAttempt) -- a non-nil, future
	// DispatchBackoffUntil means this item won't be automatically
	// re-dispatched until then, distinct from every other active state.
	DispatchAttemptCount int        `json:"dispatchAttemptCount"`
	DispatchBackoffUntil *time.Time `json:"dispatchBackoffUntil,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

// ImportedNZB holds the fully parsed contents of a manually-imported NZB --
// its files, segments, and any inspected archive structure -- ready to be
// persisted.
type ImportedNZB struct {
	FileName       string
	XML            []byte
	ExternalURL    string
	IdempotencyKey string
	FileCount      int
	SegmentCount   int
	Files          []ImportedNZBFile
	Archives       []ImportedArchive
	MediaType      string // overrides default "manual_nzb" when set
	// ArchivePassword is the NZB's embedded <meta type="password"> value,
	// if the poster/indexer included one -- "" when absent. Confirmed live
	// (2026-08-11) via a sample of real archive_encrypted-blocklisted NZBs:
	// most (10/11 resolvable in the sample) actually carry a real password
	// here, which today is silently discarded -- see SESSION_TASKS.md.
	ArchivePassword string
}

// SabQueueItem models one row of the SABnzbd-compatible queue API response.
type SabQueueItem struct {
	LibraryItemID int64
	Title         string
	MediaType     string
	State         string
}

// SabHistoryItem models one row of the SABnzbd-compatible history API response.
type SabHistoryItem struct {
	LibraryItemID     int64
	Title             string
	MediaType         string
	State             string
	FailureReason     string
	SelectedReleaseID int64
	TotalBytes        int64
}

// ImportedNZBFile is one file entry within an ImportedNZB, with its yEnc
// segments in order.
type ImportedNZBFile struct {
	FileName      string
	Subject       string
	Poster        string
	PostedUnix    int64
	FileSizeBytes int64
	Segments      []ImportedNZBSegment
}

// ImportedNZBSegment is one yEnc-encoded article within an ImportedNZBFile,
// with its decoded byte range within the reassembled file.
type ImportedNZBSegment struct {
	Number             int
	MessageID          string
	EncodedSizeBytes   int64
	DecodedStartOffset int64
	DecodedEndOffset   int64
}

// ImportedArchive is the parsed archive structure (e.g. multi-volume RAR)
// detected while inspecting an ImportedNZB, prior to being persisted into the
// archives/archive_volumes/archive_entries tables.
type ImportedArchive struct {
	Kind         string
	Status       string
	RejectReason string
	Volumes      []ImportedArchiveVolume
	Entries      []ImportedArchiveEntry
}

// ImportedArchiveVolume is one physical volume file of a multi-volume
// archive, in the order it must be reassembled.
type ImportedArchiveVolume struct {
	Path        string
	VolumeIndex int
}

// ImportedArchiveEntry is one file packed inside an archive, with the byte
// ranges (Ranges) needed to reassemble it across volumes.
type ImportedArchiveEntry struct {
	Path              string
	SizeBytes         int64 // uncompressed size of the entry's real content
	PackedSizeBytes   int64 // compressed/stored size the entry occupies inside the archive volumes
	CompressionMethod string
	Encrypted         bool
	Solid             bool
	VolumeIndex       int
	ArchiveOffset     int64
	Ranges            []ImportedArchiveRange
	// EncryptionVerified is true only when Encrypted is true AND the
	// archive's own <meta type="password"> (see internal/nzb.Document)
	// successfully derived and verified an AES key for this entry's own
	// RAR5 encryption extra-area record -- see
	// internal/rarcrypto/rar5.go's package doc for what "verified" means
	// here. EncryptionSalt/EncryptionIV/EncryptionLg2Count are only
	// meaningful when this is true; they're what
	// stream.NewEncryptedRarReader needs to re-derive the same key at
	// read time (from the same password, stored separately on
	// nzb_documents.archive_password -- never the raw key itself).
	EncryptionVerified bool
	EncryptionSalt     [16]byte
	EncryptionIV       [16]byte
	EncryptionLg2Count uint8
}

// ImportedArchiveRange is one contiguous slice of an archive entry's data as
// it lives inside a single volume: EntryOffset is the position within the
// reassembled entry, ArchiveOffset is the position within that volume file.
type ImportedArchiveRange struct {
	VolumeIndex   int
	EntryOffset   int64
	ArchiveOffset int64
	LengthBytes   int64
}

// NZBMountEntry is one NZB document exposed through the WebDAV mount, keyed
// by its current queue State so the mount can reflect in-progress items.
type NZBMountEntry struct {
	DocumentID int64
	FileName   string
	XML        []byte
	State      QueueState
}

// ContentMountEntry is one virtual file exposed through the WebDAV content
// mount.
type ContentMountEntry struct {
	VirtualFileID     int64
	SelectedReleaseID int64
	Path              string
	FileName          string
	SizeBytes         int64
	ReaderKind        string // "inline", "direct_nzb", or "stored_rar" -- selects the stream.VirtualMediaFile implementation
}

// ReleaseVirtualFile is a virtual file joined with its release and media
// metadata (movie or show), used to build the mount's directory layout.
type ReleaseVirtualFile struct {
	VirtualFileID     int64
	SelectedReleaseID int64
	LibraryItemID     int64
	MediaType         string
	Path              string
	FileName          string
	MovieTitle        string
	MovieYear         int
	MovieTMDBID       int64
	ShowTitle         string
	ShowYear          int
	ShowTVDBID        int64
	SeasonNumber      int
	EpisodeNumber     int
}

// CompletedSymlinkEntry is one published symlink exposed through the
// completed-downloads mount.
type CompletedSymlinkEntry struct {
	PublicationID int64
	Name          string
	TargetPath    string
}

// LibraryItemSummary is the API representation of a library item's overview
// row, combining its request and queue state for list views.
type LibraryItemSummary struct {
	ID                int64      `json:"id"`
	MediaType         string     `json:"mediaType"`
	Title             string     `json:"title"`
	Available         bool       `json:"available"`
	RequestedAt       time.Time  `json:"requestedAt"`
	QueueState        QueueState `json:"queueState"`
	FailureReason     string     `json:"failureReason"`
	SelectedReleaseID *int64     `json:"selectedReleaseId,omitempty"`
}

// ReleaseSummary is the API representation of a selected or candidate
// release, combining scoring, archive contents, and failure history for the
// release-detail UI.
type ReleaseSummary struct {
	SelectedReleaseID     int64                   `json:"selectedReleaseId"`
	ReleaseCandidateID    int64                   `json:"releaseCandidateId"`
	LibraryItemID         int64                   `json:"libraryItemId"`
	Title                 string                  `json:"title"`
	ExternalURL           string                  `json:"externalUrl,omitempty"`
	IndexerName           string                  `json:"indexerName,omitempty"`
	SizeBytes             int64                   `json:"sizeBytes"`
	PostedAt              time.Time               `json:"postedAt,omitempty"`
	Score                 int                     `json:"score"`
	CustomFormatScore     int                     `json:"customFormatScore"`
	Selected              bool                    `json:"selected"`
	Rejected              bool                    `json:"rejected"`
	RejectReason          string                  `json:"rejectReason"`
	FailureCount          int                     `json:"failureCount"`
	LastFailureReason     string                  `json:"lastFailureReason"`
	ArchiveCount          int                     `json:"archiveCount"`
	ArchiveVolumeCount    int                     `json:"archiveVolumeCount"`
	ArchiveStatuses       string                  `json:"archiveStatuses"`
	ArchiveRejects        string                  `json:"archiveRejects"`
	VirtualFileCount      int                     `json:"virtualFileCount"`
	Archives              []ReleaseArchiveSummary `json:"archives,omitempty"`
	FailedAttempts        []FailedReleaseAttempt  `json:"failedAttempts,omitempty"`
	Explanations          []string                `json:"explanations,omitempty"`
	CompatibilityWarnings []string                `json:"compatibilityWarnings,omitempty"`
	CreatedAt             time.Time               `json:"createdAt"`
	NZBDocumentID         *int64                  `json:"nzbDocumentId,omitempty"`
	NZBFileName           string                  `json:"nzbFileName,omitempty"`
}

// FailedReleaseAttempt records one past failure of a release candidate, kept
// for display in the release-detail UI's failure history.
type FailedReleaseAttempt struct {
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}

// ReleaseArchiveSummary is the API representation of one archive contained in
// a release (see ImportedArchive).
type ReleaseArchiveSummary struct {
	Kind         string                `json:"kind"`
	Status       string                `json:"status"`
	RejectReason string                `json:"rejectReason"`
	VolumeCount  int                   `json:"volumeCount"`
	Entries      []ReleaseArchiveEntry `json:"entries,omitempty"`
}

// ReleaseArchiveEntry is the API representation of an archive entry (see
// ImportedArchiveEntry).
type ReleaseArchiveEntry struct {
	Path                string `json:"path"`
	SizeBytes           int64  `json:"sizeBytes"`       // uncompressed size of the entry's real content
	PackedSizeBytes     int64  `json:"packedSizeBytes"` // compressed/stored size the entry occupies inside the archive volumes
	CompressionMethod   string `json:"compressionMethod"`
	Encrypted           bool   `json:"encrypted"`
	Solid               bool   `json:"solid"`
	SourceVolumeIndex   int    `json:"sourceVolumeIndex"`
	SourceArchiveOffset int64  `json:"sourceArchiveOffset"`
}

// MediaRequestSummary is the API representation of a single incoming media
// request (movie or show) and the queue state it has progressed to.
type MediaRequestSummary struct {
	ID                 int64      `json:"id"`
	ExternalID         string     `json:"externalId"`
	RequestType        string     `json:"requestType"`
	Title              string     `json:"title"`
	MediaType          string     `json:"mediaType"`
	LibraryItemID      *int64     `json:"libraryItemId,omitempty"`
	QualityProfileID   *int64     `json:"qualityProfileId,omitempty"`
	QualityProfileName string     `json:"qualityProfileName,omitempty"`
	QueueState         QueueState `json:"queueState"`
	CreatedAt          time.Time  `json:"createdAt"`
}

// SubtitleFileSummary is the API representation of a downloaded subtitle file
// already stored for a library item.
type SubtitleFileSummary struct {
	ID            int64     `json:"id"`
	LibraryItemID int64     `json:"libraryItemId"`
	Provider      string    `json:"provider"`
	Language      string    `json:"language"`
	Path          string    `json:"path"`
	CreatedAt     time.Time `json:"createdAt"`
}

// SubtitleCandidateSummary is the API representation of a subtitle result
// returned by a provider search, not yet downloaded.
type SubtitleCandidateSummary struct {
	ID              int64     `json:"id"`
	LibraryItemID   int64     `json:"libraryItemId"`
	Provider        string    `json:"provider"`
	Language        string    `json:"language"`
	Title           string    `json:"title"`
	ReleaseName     string    `json:"releaseName"`
	Format          string    `json:"format"`
	HearingImpaired bool      `json:"hearingImpaired"`
	Score           int       `json:"score"`
	ExternalID      string    `json:"externalId"`
	DownloadURL     string    `json:"-"`
	CreatedAt       time.Time `json:"createdAt"`
}

// BlocklistItemSummary is the API representation of a single blocklist entry,
// joined with the release/library context it was blocked from when available.
type BlocklistItemSummary struct {
	ID                int64      `json:"id"`
	Key               string     `json:"key"`
	KeyType           string     `json:"keyType,omitempty"`
	Reason            string     `json:"reason"`
	CreatedAt         time.Time  `json:"createdAt"`
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
	SelectedReleaseID *int64     `json:"selectedReleaseId,omitempty"`
	LibraryItemID     *int64     `json:"libraryItemId,omitempty"`
	ReleaseTitle      string     `json:"releaseTitle,omitempty"`
	IndexerName       string     `json:"indexerName,omitempty"`
	SizeBytes         int64      `json:"sizeBytes,omitempty"`
	PostedAt          *time.Time `json:"postedAt,omitempty"`
}

// BlocklistMutation carries the fields accepted when a client manually adds a
// key to the blocklist.
type BlocklistMutation struct {
	Key       string     `json:"key"`
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// BlocklistPage is one page of a paginated blocklist listing.
type BlocklistPage struct {
	Items      []BlocklistItemSummary `json:"items"`
	Page       int                    `json:"page"`
	PageSize   int                    `json:"pageSize"`
	Total      int                    `json:"total"`
	TotalPages int                    `json:"totalPages"`
}

// BlocklistStats summarizes the blocklist for dashboard display.
type BlocklistStats struct {
	Total    int            `json:"total"`
	Expired  int            `json:"expired"`
	Active   int            `json:"active"`
	ByReason map[string]int `json:"byReason"`
}

// SearchCandidateRecord is a single indexer search result prior to being
// persisted as a release_candidates row, carrying the fields needed for
// scoring, deduplication, and merge-by-(indexer, title, size) comparison.
type SearchCandidateRecord struct {
	Title                 string
	ExternalURL           string
	IndexerName           string
	SizeBytes             int64
	PostedAt              time.Time
	Score                 int
	CustomFormatScore     int
	Explanations          []string
	CompatibilityWarnings []string
	Rejected              bool
	RejectReason          string
	FailureCount          int
	LastFailureReason     string
	Resolution            string
}

// GrabHistoryEntry records one release grab (selection) event for a library
// item, used to render the grab-history UI.
type GrabHistoryEntry struct {
	ID                 int64     `json:"id"`
	LibraryItemID      int64     `json:"libraryItemId"`
	ReleaseCandidateID *int64    `json:"releaseCandidateId,omitempty"`
	Title              string    `json:"title"`
	IndexerName        string    `json:"indexerName"`
	Score              int       `json:"score"`
	Resolution         string    `json:"resolution"`
	GrabbedAt          time.Time `json:"grabbedAt"`
}

// CustomFormat is a user- or system-defined scoring rule matched against
// release titles (e.g. a preferred/avoided release group or encode) that
// adds or subtracts from a candidate's ranking score.
type CustomFormat struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
	Score   int    `json:"score"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

// ReleaseBlockRule is a rule that rejects or penalizes release candidates
// matching a pattern (e.g. a banned release group), scoped to a media type.
type ReleaseBlockRule struct {
	ID           int64     `json:"id"`
	Type         string    `json:"type"`
	Pattern      string    `json:"pattern"`
	MediaType    string    `json:"mediaType"`
	Action       string    `json:"action"`
	ScorePenalty int       `json:"scorePenalty"`
	Enabled      bool      `json:"enabled"`
	Source       string    `json:"source"`
	Note         string    `json:"note"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// SubtitleProfile is a named set of subtitle search preferences (languages,
// hearing-impaired preference, exact-language requirement) that can be
// assigned as a library item's default.
type SubtitleProfile struct {
	ID                    int64     `json:"id"`
	Name                  string    `json:"name"`
	Languages             []string  `json:"languages"`
	PreferHearingImpaired bool      `json:"preferHearingImpaired"`
	RequireExactLanguage  bool      `json:"requireExactLanguage"`
	IsDefault             bool      `json:"isDefault"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

// IndexerPolicy applies a per-indexer score adjustment (or disables the
// indexer entirely) during candidate ranking.
type IndexerPolicy struct {
	ID            int64     `json:"id"`
	IndexerName   string    `json:"indexerName"`
	ScoreModifier int       `json:"scoreModifier"`
	Enabled       bool      `json:"enabled"`
	Note          string    `json:"note"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// CandidateHistory is a release candidate's prior failure record, looked up
// by external URL so a repeat candidate can inherit its failure count instead
// of retrying from zero.
type CandidateHistory struct {
	ExternalURL       string
	FailureCount      int
	LastFailureReason string
}

// StoredNZBDocument is the persisted NZB document backing an already-selected
// release, as opposed to ImportedNZB which represents one still being parsed.
type StoredNZBDocument struct {
	SelectedReleaseID int64
	FileName          string
	ExternalURL       string
	XML               []byte
}

// SubtitleLibraryFilter carries the filter and pagination parameters for
// listing library items in the subtitle-management UI.
type SubtitleLibraryFilter struct {
	MediaType   string
	Search      string
	MissingOnly bool
	Page        int
	PageSize    int
}

// SubtitleLibraryRow is one library item row in the subtitle-management
// listing, summarizing its subtitle coverage.
type SubtitleLibraryRow struct {
	LibraryItemID  int64     `json:"libraryItemId"`
	MediaType      string    `json:"mediaType"`
	Title          string    `json:"title"`
	ShowTitle      string    `json:"showTitle,omitempty"`
	SeasonNumber   int       `json:"seasonNumber,omitempty"`
	EpisodeNumber  int       `json:"episodeNumber,omitempty"`
	Available      bool      `json:"available"`
	Languages      []string  `json:"languages"`
	CandidateCount int       `json:"candidateCount"`
	RequestedAt    time.Time `json:"requestedAt"`
}

// SubtitleLibraryPage is one page of a paginated subtitle-library listing.
type SubtitleLibraryPage struct {
	Items      []SubtitleLibraryRow `json:"items"`
	Page       int                  `json:"page"`
	PageSize   int                  `json:"pageSize"`
	Total      int                  `json:"total"`
	TotalPages int                  `json:"totalPages"`
}

// SubtitleDeleteGroup batches the subtitle file paths for one
// provider/language combination on a library item so they can be deleted
// together.
type SubtitleDeleteGroup struct {
	LibraryItemID int64
	Provider      string
	Language      string
	Paths         []string
}

// SubtitleSearchInput carries the metadata needed to query subtitle providers
// for a specific library item.
type SubtitleSearchInput struct {
	LibraryItemID int64
	MediaType     string
	Title         string
	ShowTitle     string
	MovieYear     int
	ShowYear      int
	SeasonNumber  int
	EpisodeNumber int
	TMDBID        int64
	TVDBID        int64
}

// SubtitleCandidateRecord is a single provider search result prior to being
// persisted as a subtitle_candidates row.
type SubtitleCandidateRecord struct {
	Provider        string
	Language        string
	Title           string
	ReleaseName     string
	Format          string
	HearingImpaired bool
	Score           int
	ExternalID      string
	DownloadURL     string
}

// LibrarySearchInput carries the metadata needed to query indexers for a
// release matching a specific library item.
type LibrarySearchInput struct {
	LibraryItemID int64
	MediaType     string
	Title         string
	IMDbID        string
	MovieYear     int
	MovieTMDBID   int64 // used in tmdbid= query parameter (Radarr approach)
	ShowTitle     string
	EpisodeTitle  string
	ShowIMDbID    string
	ShowTVDBID    int64
	ShowTMDBID    int64 // used in tmdbid= query parameter for TV (Sonarr approach)
	ShowYear      int
	SeasonNumber  int
	EpisodeNumber int
	// EpisodeYear: the year of this specific episode's own air_date, when
	// known (0 otherwise). A long-running show's later seasons legitimately
	// air -- and get release-tagged -- years after ShowYear (the show's
	// first-air-date year), so a release matching this instead of ShowYear
	// is not a wrong-show signal and must not be treated as one.
	EpisodeYear     int
	TVShowID        int64    // DB primary key of tv_shows row, used for season pack tracking
	AlternateTitles []string // mirrors Radarr/Sonarr AlternativeTitles; checked as fallback
	RuntimeMinutes  int      // movie runtime; 0 for episodes/unknown; used for MB/min size checks
}

// SymlinkPublicationRecord identifies a completed download's publication,
// mapping its library path to the on-disk target the symlink must point to.
type SymlinkPublicationRecord struct {
	ID          int64
	LibraryPath string
	TargetPath  string
}

// QueueRetryTarget identifies a queue_items row eligible for a retry pass,
// with the fields needed to re-run selection for it.
type QueueRetryTarget struct {
	QueueItemID       int64
	LibraryItemID     int64
	SelectedReleaseID *int64
	MediaType         string
	IdempotencyKey    string
	State             QueueState
}

// PendingLibrarySearchTarget is a library item awaiting (or mid-) search,
// returned by the periodic pending-search scan.
type PendingLibrarySearchTarget struct {
	LibraryItemID     int64      `json:"libraryItemId"`
	MediaType         string     `json:"mediaType"`
	TVShowID          int64      `json:"tvShowId"`
	SeasonNumber      int        `json:"seasonNumber"`
	Selected          bool       `json:"selected"`
	SelectedReleaseID int64      `json:"selectedReleaseId"` // 0 if none
	ExternalURL       string     `json:"externalUrl,omitempty"`
	State             QueueState `json:"state"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	// DispatchAttemptCount/DispatchBackoffUntil persist the passive-resume
	// sweep's escalating per-item backoff (see RecordDispatchAttempt) --
	// DB-backed specifically so it survives a process restart, unlike the
	// in-memory map it replaced.
	DispatchAttemptCount int        `json:"dispatchAttemptCount"`
	DispatchBackoffUntil *time.Time `json:"dispatchBackoffUntil,omitempty"`
}

// FailedQueueRetryTarget is a queue item in the failed state considered for
// an automatic retry pass.
type FailedQueueRetryTarget struct {
	QueueItemID           int64  `json:"queueItemId"`
	LibraryItemID         int64  `json:"libraryItemId"`
	FailureReason         string `json:"failureReason"`
	HasSelectedRelease    bool   `json:"hasSelectedRelease"`
	CandidateFailureCount int    `json:"candidateFailureCount"`
}

// SelectedQueueRetryTarget is a minimal row for a queue item in the selected
// state, used to check whether it's stalled and needs a retry.
type SelectedQueueRetryTarget struct {
	QueueItemID   int64      `json:"queueItemId"`
	LibraryItemID int64      `json:"libraryItemId"`
	State         QueueState `json:"state"`
}

// PendingRepublishTarget is a library item flagged for re-publication (e.g.
// after a symlink target changed), returned by the pending-republish scan.
type PendingRepublishTarget struct {
	LibraryItemID int64 `json:"libraryItemId"`
}

// BlocklistClearResult reports how many blocklist entries a bulk-clear
// operation removed.
type BlocklistClearResult struct {
	Cleared int `json:"cleared"`
}

// RejectedReleaseRestoreResult reports how many previously-rejected release
// candidates were restored to eligibility for a library item.
type RejectedReleaseRestoreResult struct {
	LibraryItemID int64 `json:"libraryItemId"`
	Restored      int   `json:"restored"`
}
