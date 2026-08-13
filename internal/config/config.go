package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/drakkar-media/drakkar/internal/privacy/wireguard"
)

// Default* constants provide the fallback values applied by applyDefaults
// and DefaultRuntime when settings.json omits a field or is being created
// for the first time (see LoadOrCreate). They mirror the Docker Compose
// volume layout under /mnt/drakkar and /app/data.
const (
	DefaultSettingsPath         = "/app/data/settings.json"
	DefaultFuseMountPath        = "/mnt/drakkar/vfs"
	DefaultMovieLibraryPath     = "/mnt/drakkar/media/movies"
	DefaultTVLibraryPath        = "/mnt/drakkar/media/tv"
	DefaultBlockCachePath       = "/mnt/drakkar/cache/blocks"
	DefaultHeaderCachePath      = "/mnt/drakkar/cache/headers"
	DefaultRepairWorkspacePath  = "/mnt/drakkar/cache/repair-workspace"
	DefaultStagingNZBPath       = "/mnt/drakkar/staging/nzbs"
	DefaultFailedDiagnostics    = "/mnt/drakkar/failed"
	DefaultLogsPath             = "/app/data/logs"
	DefaultHTTPAddress          = ":8080"
	DefaultWebDAVAddress        = ":8888"
	DefaultDiskCacheLimitBytes  = int64(20 << 30)
	DefaultReadAheadLimitBytes  = int64(512 << 20)
	DefaultMemoryHotCacheBytes  = int64(512 << 20)
	DefaultRepairWorkspaceBytes = int64(20 << 30)
	DefaultNZBUploadLimitBytes  = int64(64 << 20)
	DefaultMaxDownloadConns     = 15
	DefaultStreamingPriorityPct = 80
	DefaultArticleBufferSize    = 40
)

// Settings is the complete, persisted application configuration. It is the
// on-disk (settings.json) and in-memory representation of every user-facing
// setting, and is loaded via Load/LoadOrCreate and persisted via Save.
type Settings struct {
	Database      DatabaseConfig      `json:"database"`
	Valkey        ValkeyConfig        `json:"valkey"`
	NZBHydra2     ServiceConfig       `json:"nzbhydra2"`
	SABNZBD       SABNZBDConfig       `json:"sabnzbd"`
	Seerr         ServiceConfig       `json:"seerr"`
	Usenet        UsenetConfig        `json:"usenet"`
	Metadata      MetadataConfig      `json:"metadata"`
	Subtitles     SubtitlesConfig     `json:"subtitles"`
	Plex          PlexConfig          `json:"plex"`
	Jellyfin      JellyfinConfig      `json:"jellyfin"`
	Library       LibraryConfig       `json:"library"`
	Indexer       IndexerConfig       `json:"indexer"`
	Notifications NotificationsConfig `json:"notifications"`
	Rclone        RcloneConfig        `json:"rclone"`
	Logging       LoggingConfig       `json:"logging"`
	Privacy       PrivacyConfig       `json:"privacy"`
}

// PrivacyConfig selects how Usenet/NNTP and NZB-indexer HTTP traffic is
// routed: "direct" (today's behavior, default), "socks5", or "wireguard".
// Exactly one mode is active at a time. ExcludedIndexers lists indexer
// names (matched against a release candidate's IndexerName) whose NZB
// downloads always use a direct connection regardless of Mode -- useful
// for a private/trusted indexer the user doesn't want routed.
type PrivacyConfig struct {
	Mode             string                 `json:"mode"`
	SOCKS5           PrivacySOCKS5Config    `json:"socks5"`
	WireGuard        PrivacyWireGuardConfig `json:"wireguard"`
	ExcludedIndexers []string               `json:"excludedIndexers"`
	// SyncNZBHydra2Proxy: when true (and Mode is "socks5"), push SOCKS5 into
	// NZBHydra2's own proxy settings (via its /internalapi/config API) on
	// every settings save and at startup, so NZBHydra2's own outbound
	// indexer traffic -- which Drakkar has no way to route itself, since
	// NZBHydra2 is a separate process with its own networking -- goes
	// through the same SOCKS5 proxy. Applied unconditionally on every
	// reload: false (or any mode other than "socks5") actively clears
	// NZBHydra2's proxy back to "no proxy" rather than leaving whatever was
	// last pushed in place, since NZBHydra2 has no WireGuard proxy type.
	// Left false by default since it mutates a different application's
	// config.
	SyncNZBHydra2Proxy bool `json:"syncNzbHydra2Proxy"`
}

// PrivacySOCKS5Config holds the connection settings for routing traffic
// through a SOCKS5 proxy when PrivacyConfig.Mode is PrivacyModeSOCKS5.
type PrivacySOCKS5Config struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

// PrivacyWireGuardConfig stores the raw imported .conf text server-side.
// ConfigText is never round-tripped with its secrets intact -- see
// RedactSecrets/MergeSecrets below -- the frontend instead reads a
// sanitized summary from GET /api/settings/privacy/status.
type PrivacyWireGuardConfig struct {
	ConfigText     string `json:"configText"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

const (
	PrivacyModeDirect    = "direct"
	PrivacyModeSOCKS5    = "socks5"
	PrivacyModeWireGuard = "wireguard"
)

// LoggingConfig controls the runtime log verbosity, hot-reloadable without a
// restart. Level accepts "trace", "debug", "info" (default), "warn", "error".
type LoggingConfig struct {
	Level string `json:"level"`
}

// RcloneConfig holds optional rclone remote control settings.
// When RCAddr is set Drakkar calls vfs/refresh after publishing new content
// so rclone's directory cache is invalidated immediately.
type RcloneConfig struct {
	// RCAddr: rclone remote control address (e.g. "http://drakkar_rclone:5572").
	// Leave empty to disable VFS refresh (rclone dir-cache-time handles staleness).
	RCAddr string `json:"rcAddr"`
}

// NotificationsConfig holds settings for outgoing event notifications.
// Mirrors Sonarr/Radarr Settings → Connect.
type NotificationsConfig struct {
	// DiscordWebhookURL: if set, sends Discord embeds on selected events.
	DiscordWebhookURL string `json:"discordWebhookUrl"`
	// GenericWebhookURL: if set, sends a JSON POST for every selected event.
	GenericWebhookURL string `json:"genericWebhookUrl"`
	// OnGrab: fire when a release is selected for download.
	OnGrab bool `json:"onGrab"`
	// OnAvailable: fire when an item finishes importing.
	OnAvailable bool `json:"onAvailable"`
	// OnFailed: fire when an item permanently fails.
	OnFailed bool `json:"onFailed"`
}

// IndexerConfig mirrors Sonarr/Radarr Settings → Indexers.
// Defaults are applied in DefaultIndexerConfig().
type IndexerConfig struct {
	// TvRssSyncIntervalMinutes: how often to poll TV/episode RSS feeds.
	// Valid range: 10–120, or 0 to disable. Sonarr default: 15. Minimum enforced: 15.
	TvRssSyncIntervalMinutes int `json:"tvRssSyncIntervalMinutes"`

	// MovieRssSyncIntervalMinutes: how often to poll movie RSS feeds.
	// Valid range: 10–120, or 0 to disable. Radarr default: 30. Minimum enforced: 30.
	MovieRssSyncIntervalMinutes int `json:"movieRssSyncIntervalMinutes"`

	// MinimumAgeMinutes: don't grab a release younger than this.
	// Gives time for the NZB to propagate across Usenet servers. Default: 0.
	MinimumAgeMinutes int `json:"minimumAgeMinutes"`

	// RetentionDays: skip releases older than this many days (0 = unlimited).
	// Matches your Usenet provider's actual retention window. Default: 0.
	RetentionDays int `json:"retentionDays"`

	// MaximumSizeMB: reject releases larger than this (0 = unlimited). Default: 0.
	MaximumSizeMB int `json:"maximumSizeMB"`

	// SearchDelayMs: minimum milliseconds between consecutive NZBHydra2 search
	// requests. 0 means no delay. Default: 2000 (matches Sonarr/Radarr single-
	// stream sequential pacing; prevents exhausting daily indexer API quotas).
	SearchDelayMs int `json:"searchDelayMs"`

	// BackgroundSearchWorkers: BullMQ worker concurrency for missing-item search
	// jobs. Higher values improve backlog throughput when Hydra throttling is low.
	// Default: 12.
	BackgroundSearchWorkers int `json:"backgroundSearchWorkers"`

	// ReleaseGraceHours: don't search for a movie/episode until this many hours
	// after its release_date/air_date (a calendar date with no time-of-day).
	// The automatic search pipeline already refuses to search anything whose
	// release/air date is strictly in the future; this adds a further grace
	// window on top of the release DAY itself, since episodes/movies release
	// at a specific time (often midnight in another timezone, or a broadcast
	// slot later in the day), not literally at 00:00 local time -- without a
	// grace period, an item becomes "searchable" the instant the calendar
	// flips, well before a real release actually posts to Usenet. 0 preserves
	// the original release-day behavior. Default: 12.
	ReleaseGraceHours int `json:"releaseGraceHours"`
}

// DefaultIndexerConfig returns the IndexerConfig defaults applied by
// applyDefaults when settings.json omits (or zero-values) an indexer field.
// Values mirror Sonarr/Radarr's own indexer defaults where an equivalent
// setting exists.
func DefaultIndexerConfig() IndexerConfig {
	return IndexerConfig{
		TvRssSyncIntervalMinutes:    15,
		MovieRssSyncIntervalMinutes: 30,
		MinimumAgeMinutes:           0,
		RetentionDays:               0,
		MaximumSizeMB:               0,
		// 2s between calls matches Sonarr/Radarr single-stream sequential pacing.
		// Prevents exhausting daily indexer API quotas during backlog bursts.
		// Set to 0 to disable (rely purely on NZBHydra2's own rate limiting).
		SearchDelayMs:           2000,
		BackgroundSearchWorkers: 10,
		ReleaseGraceHours:       12,
	}
}

// PlexConfig holds the Plex Media Server connection settings.
type PlexConfig struct {
	URL        string `json:"url"`        // e.g. http://192.168.1.10:32400
	Token      string `json:"token"`      // X-Plex-Token
	SectionKey string `json:"sectionKey"` // library section key (empty = all)
}

// JellyfinConfig holds the Jellyfin Media Server connection settings.
type JellyfinConfig struct {
	URL    string `json:"url"`    // e.g. http://192.168.1.10:8096
	APIKey string `json:"apiKey"` // Jellyfin API key
}

// DatabaseConfig holds the PostgreSQL connection settings.
type DatabaseConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// ValkeyConfig holds the Valkey (Redis-compatible) connection settings used
// for caching and the BullMQ-backed background job queues.
type ValkeyConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password"`
}

// ServiceConfig holds the connection and cache-tuning settings shared by the
// NZBHydra2 and Seerr integrations. Search/feed results are cached for the
// configured TTLs to avoid re-querying the upstream service on every request.
type ServiceConfig struct {
	URL                   string `json:"url"`
	APIKey                string `json:"apiKey"`
	SearchCacheTTLSeconds int    `json:"searchCacheTtlSeconds"`
	FeedCacheTTLSeconds   int    `json:"feedCacheTtlSeconds"`
	FeedMaxResults        int    `json:"feedMaxResults"`
}

// SABNZBDConfig secures the SABnzbd-compatible download-client API used by
// Sonarr and Radarr. An empty key leaves those endpoints disabled.
type SABNZBDConfig struct {
	APIKey string `json:"apiKey"`
}

// UsenetConfig controls download concurrency and holds the list of
// configured Usenet (NNTP) providers used for article retrieval.
type UsenetConfig struct {
	MaxDownloadConnections int              `json:"maxDownloadConnections"`
	StreamingPriorityPct   int              `json:"streamingPriorityPercent"`
	ArticleBufferSize      int              `json:"articleBufferSize"`
	Providers              []UsenetProvider `json:"providers"`
}

// UsenetProvider describes a single NNTP server connection. Enabled allows a
// provider to be kept in configuration while temporarily excluded from use;
// disabled and unreachable-looking providers (missing host/credentials) are
// skipped when wiring up article clients (see app.go).
type UsenetProvider struct {
	Name           string `json:"name"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	TLS            bool   `json:"tls"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	MaxConnections int    `json:"maxConnections"`
	Priority       int    `json:"priority"`
	RetentionDays  int    `json:"retentionDays"`
	Backup         bool   `json:"backup"`
	Enabled        bool   `json:"enabled"`
}

// MetadataConfig holds the TMDB/TVDB API credentials and metadata lookup
// settings used to enrich library items with titles, artwork, and air dates.
type MetadataConfig struct {
	TMDB          APIKeyConfig `json:"tmdb"`
	TVDB          APIKeyConfig `json:"tvdb"`
	Language      string       `json:"language"`
	CacheTTLHours int          `json:"cacheTtlHours"`
}

// LibraryConfig holds the default quality profiles applied to newly added
// movies and TV shows when no profile is explicitly selected.
type LibraryConfig struct {
	DefaultMovieProfile string `json:"defaultMovieProfile"`
	DefaultTvProfile    string `json:"defaultTvProfile"`
}

// APIKeyConfig wraps a single API key credential for a metadata provider.
type APIKeyConfig struct {
	APIKey string `json:"apiKey"`
}

// SubtitlesConfig controls whether subtitle downloading is enabled, which
// languages to fetch, and the per-provider credentials keyed by provider
// name (e.g. "subdl", "opensubtitles").
type SubtitlesConfig struct {
	Enabled   bool                    `json:"enabled"`
	Languages []string                `json:"languages"`
	Providers map[string]SubtitleAuth `json:"providers"`
}

// SubtitleAuth holds the enable flag and credentials for one subtitle
// provider. Not every provider uses every field (e.g. API-key-only
// providers leave Username/Password empty).
type SubtitleAuth struct {
	Enabled  bool   `json:"enabled"`
	APIKey   string `json:"apiKey"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Runtime holds the process-level filesystem paths and cache size limits
// that are fixed for the lifetime of the process (unlike Settings, which is
// hot-reloadable). These are derived from command-line flags/environment at
// startup, defaulting to the Default* constants via DefaultRuntime.
type Runtime struct {
	SettingsPath           string
	HTTPAddress            string
	WebDAVAddress          string
	AuthCookieSecure       bool
	AuthTrustProxyHeaders  bool
	FuseMountPath          string
	MovieLibraryPath       string
	TVLibraryPath          string
	BlockCachePath         string
	HeaderCachePath        string
	RepairWorkspacePath    string
	StagingNZBPath         string
	FailedDiagnosticsPath  string
	LogsPath               string
	DiskCacheLimitBytes    int64
	ReadAheadLimitBytes    int64
	MemoryHotCacheMaxBytes int64
	RepairWorkspaceMax     int64
	NZBUploadLimitBytes    int64
}

// DefaultRuntime returns a Runtime populated with the Docker-Compose-
// compatible default paths and cache limits.
func DefaultRuntime() Runtime {
	return Runtime{
		SettingsPath:           DefaultSettingsPath,
		HTTPAddress:            DefaultHTTPAddress,
		WebDAVAddress:          DefaultWebDAVAddress,
		FuseMountPath:          DefaultFuseMountPath,
		MovieLibraryPath:       DefaultMovieLibraryPath,
		TVLibraryPath:          DefaultTVLibraryPath,
		BlockCachePath:         DefaultBlockCachePath,
		HeaderCachePath:        DefaultHeaderCachePath,
		RepairWorkspacePath:    DefaultRepairWorkspacePath,
		StagingNZBPath:         DefaultStagingNZBPath,
		FailedDiagnosticsPath:  DefaultFailedDiagnostics,
		LogsPath:               DefaultLogsPath,
		DiskCacheLimitBytes:    DefaultDiskCacheLimitBytes,
		ReadAheadLimitBytes:    DefaultReadAheadLimitBytes,
		MemoryHotCacheMaxBytes: DefaultMemoryHotCacheBytes,
		RepairWorkspaceMax:     DefaultRepairWorkspaceBytes,
		NZBUploadLimitBytes:    DefaultNZBUploadLimitBytes,
	}
}

// Load reads and parses the settings file at path.
//
// Unknown JSON fields are rejected to catch typos/renames in settings.json.
// After decoding, applyDefaults fills any zero-valued field with its
// documented default and validate enforces required fields and cross-field
// constraints.
//
// Errors:
//   - returns a wrapped os.ErrNotExist-compatible error if path does not
//     exist (see LoadOrCreate, which handles that case).
//   - returns a parse or validation error otherwise.
func Load(path string) (Settings, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	var cfg Settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return Settings{}, fmt.Errorf("parse settings: %w", err)
	}
	applyDefaults(&cfg)
	if err := validate(cfg); err != nil {
		return Settings{}, err
	}
	return cfg, nil
}

// LoadOrCreate loads settings from path. If the file does not exist a minimal
// settings.json is created with Docker-Compose-compatible defaults and then
// loaded. Other errors (parse, validation) are returned as-is.
func LoadOrCreate(path string) (Settings, error) {
	cfg, err := Load(path)
	if err == nil {
		return cfg, nil
	}
	if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no such file") {
		return Settings{}, err
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return Settings{}, fmt.Errorf("create settings dir: %w", mkErr)
	}
	blank := defaultSettings()
	data, _ := json.MarshalIndent(blank, "", "  ")
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		return Settings{}, fmt.Errorf("write default settings: %w", writeErr)
	}
	return Load(path)
}

// defaultSettings builds the minimal Settings written to disk the first time
// LoadOrCreate runs. Only the fields with no safe zero-value default
// (database/valkey connection info) are set explicitly; everything else is
// filled in by applyDefaults.
func defaultSettings() Settings {
	s := Settings{}
	s.Database = DatabaseConfig{Host: "postgres", Port: 5432, Name: "drakkar", Username: "drakkar", Password: "change-me"}
	s.Valkey = ValkeyConfig{Host: "valkey", Port: 6379}
	applyDefaults(&s)
	return s
}

// applyDefaults mutates cfg in place, replacing zero-valued fields with
// their documented defaults. It is idempotent and safe to call on both
// freshly-decoded settings (Load) and settings about to be persisted (Save),
// so a partially-specified settings.json always ends up fully populated.
func applyDefaults(cfg *Settings) {
	if cfg == nil {
		return
	}
	if cfg.Usenet.MaxDownloadConnections <= 0 {
		cfg.Usenet.MaxDownloadConnections = DefaultMaxDownloadConns
	}
	if cfg.Usenet.StreamingPriorityPct <= 0 {
		cfg.Usenet.StreamingPriorityPct = DefaultStreamingPriorityPct
	}
	if cfg.Usenet.ArticleBufferSize <= 0 {
		cfg.Usenet.ArticleBufferSize = DefaultArticleBufferSize
	}
	if cfg.Indexer.TvRssSyncIntervalMinutes == 0 {
		cfg.Indexer.TvRssSyncIntervalMinutes = DefaultIndexerConfig().TvRssSyncIntervalMinutes
	}
	if cfg.Indexer.MovieRssSyncIntervalMinutes == 0 {
		cfg.Indexer.MovieRssSyncIntervalMinutes = DefaultIndexerConfig().MovieRssSyncIntervalMinutes
	}
	if cfg.Indexer.BackgroundSearchWorkers <= 0 {
		cfg.Indexer.BackgroundSearchWorkers = DefaultIndexerConfig().BackgroundSearchWorkers
	}
	if cfg.Indexer.SearchDelayMs <= 0 {
		cfg.Indexer.SearchDelayMs = DefaultIndexerConfig().SearchDelayMs
	}
	if cfg.Indexer.ReleaseGraceHours == 0 {
		cfg.Indexer.ReleaseGraceHours = DefaultIndexerConfig().ReleaseGraceHours
	}
	if len(cfg.Subtitles.Languages) == 0 {
		cfg.Subtitles.Languages = []string{"en"}
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Privacy.Mode == "" {
		cfg.Privacy.Mode = PrivacyModeDirect
	}
	if cfg.Privacy.SOCKS5.TimeoutSeconds <= 0 {
		cfg.Privacy.SOCKS5.TimeoutSeconds = 15
	}
	if cfg.Privacy.WireGuard.TimeoutSeconds <= 0 {
		cfg.Privacy.WireGuard.TimeoutSeconds = 15
	}
	if cfg.Privacy.ExcludedIndexers == nil {
		// A nil slice marshals to JSON null rather than []; the frontend
		// always treats this field as an array (e.g. calls .join() on it),
		// so a genuinely-empty list must still serialize as [].
		cfg.Privacy.ExcludedIndexers = []string{}
	}
}

// validate enforces required fields and cross-field constraints on cfg,
// collecting every problem found rather than failing on the first, so a
// single validation error can report all of them at once.
func validate(cfg Settings) error {
	var problems []string
	if cfg.Database.Host == "" {
		problems = append(problems, "database.host required")
	}
	if cfg.Database.Port <= 0 {
		problems = append(problems, "database.port must be positive")
	}
	if cfg.Database.Name == "" {
		problems = append(problems, "database.name required")
	}
	if cfg.Database.Username == "" {
		problems = append(problems, "database.username required")
	}
	if cfg.Valkey.Host == "" {
		problems = append(problems, "valkey.host required")
	}
	if cfg.Valkey.Port <= 0 {
		problems = append(problems, "valkey.port must be positive")
	}
	// External service URLs are optional — the app starts without them.
	if cfg.NZBHydra2.URL != "" {
		if err := validateURL("nzbhydra2.url", cfg.NZBHydra2.URL); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if cfg.Seerr.URL != "" {
		if err := validateURL("seerr.url", cfg.Seerr.URL); err != nil {
			problems = append(problems, err.Error())
		}
	}
	// Validate individual providers only when present.
	for i, provider := range cfg.Usenet.Providers {
		prefix := fmt.Sprintf("usenet.providers[%d]", i)
		if provider.Name == "" {
			problems = append(problems, prefix+".name required")
		}
		if provider.Port <= 0 {
			problems = append(problems, prefix+".port must be positive")
		}
		if provider.MaxConnections <= 0 {
			problems = append(problems, prefix+".maxConnections must be positive")
		}
	}
	switch cfg.Privacy.Mode {
	case "", PrivacyModeDirect:
	case PrivacyModeSOCKS5:
		if cfg.Privacy.SOCKS5.Host == "" || cfg.Privacy.SOCKS5.Port <= 0 {
			problems = append(problems, "privacy.socks5.host and privacy.socks5.port are required when privacy.mode is socks5")
		}
	case PrivacyModeWireGuard:
		if _, err := wireguard.ParseConfig(cfg.Privacy.WireGuard.ConfigText); err != nil {
			problems = append(problems, fmt.Sprintf("privacy.wireguard.configText: %v", err))
		}
	default:
		problems = append(problems, fmt.Sprintf("privacy.mode %q is not one of direct, socks5, wireguard", cfg.Privacy.Mode))
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validateURL(name, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s required", name)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s invalid", name)
	}
	return nil
}

// ValidatePaths checks that the configured library/cache/staging paths do
// not overlap the FUSE mount itself.
//
// Writing into the FUSE-mounted virtual filesystem (rather than the real
// backing directories it exposes) would corrupt the virtual view or create
// unresolvable recursive paths, so every configured path must live outside
// rt.FuseMountPath. The movie library path is additionally required to live
// under /mnt/drakkar, matching the expected volume layout.
func ValidatePaths(rt Runtime) error {
	abs := func(path string) string {
		out, err := filepath.Abs(path)
		if err != nil {
			return path
		}
		return filepath.Clean(out)
	}

	fuseRoot := abs(rt.FuseMountPath)
	checks := []string{
		rt.MovieLibraryPath,
		rt.TVLibraryPath,
		rt.BlockCachePath,
		rt.HeaderCachePath,
		rt.RepairWorkspacePath,
		rt.StagingNZBPath,
		rt.FailedDiagnosticsPath,
	}

	for _, target := range checks {
		clean := abs(target)
		if clean == fuseRoot || strings.HasPrefix(clean, fuseRoot+string(os.PathSeparator)) {
			return fmt.Errorf("path %s must remain outside fuse mount %s", clean, fuseRoot)
		}
	}

	if !strings.HasPrefix(abs(rt.MovieLibraryPath), filepath.Dir(filepath.Dir(fuseRoot))+string(os.PathSeparator)) {
		return fmt.Errorf("movie library path %s must live under /mnt/drakkar", rt.MovieLibraryPath)
	}
	return nil
}

// Save validates cfg and atomically writes it to path as indented JSON.
func Save(path string, cfg Settings) error {
	applyDefaults(&cfg)
	if err := validate(cfg); err != nil {
		return fmt.Errorf("invalid settings: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}

// RedactedSettings builds a read-only, display-safe view of cfg for the
// /api/status endpoint: every credential is replaced with the literal
// string "***" so it is unambiguously masked rather than merely absent.
// Unlike RedactSecrets (which blanks fields for a round-trippable edit
// form), this output is never intended to be sent back to Save/MergeSecrets.
func RedactedSettings(cfg Settings) map[string]any {
	return map[string]any{
		"database": map[string]any{
			"host":     cfg.Database.Host,
			"port":     cfg.Database.Port,
			"name":     cfg.Database.Name,
			"username": cfg.Database.Username,
			"password": "***",
		},
		"valkey": map[string]any{
			"host":     cfg.Valkey.Host,
			"port":     cfg.Valkey.Port,
			"password": "***",
		},
		"nzbhydra2": map[string]any{
			"url":                   cfg.NZBHydra2.URL,
			"apiKey":                "***",
			"searchCacheTtlSeconds": cfg.NZBHydra2.SearchCacheTTLSeconds,
			"feedCacheTtlSeconds":   cfg.NZBHydra2.FeedCacheTTLSeconds,
			"feedMaxResults":        cfg.NZBHydra2.FeedMaxResults,
		},
		"sabnzbd": map[string]any{
			"apiKey": "***",
		},
		"seerr": map[string]any{
			"url":    cfg.Seerr.URL,
			"apiKey": "***",
		},
		"usenet": map[string]any{
			"maxDownloadConnections":   cfg.Usenet.MaxDownloadConnections,
			"streamingPriorityPercent": cfg.Usenet.StreamingPriorityPct,
			"articleBufferSize":        cfg.Usenet.ArticleBufferSize,
			"providers":                redactUsenetProviders(cfg.Usenet.Providers),
		},
		"metadata": map[string]any{
			"tmdb": map[string]any{
				"apiKey": "***",
			},
			"tvdb": map[string]any{
				"apiKey": "***",
			},
		},
		"subtitles": map[string]any{
			"enabled":   cfg.Subtitles.Enabled,
			"languages": append([]string(nil), cfg.Subtitles.Languages...),
			"providers": redactSubtitleProviders(cfg.Subtitles.Providers),
		},
		"privacy": map[string]any{
			"mode":               cfg.Privacy.Mode,
			"excludedIndexers":   append([]string(nil), cfg.Privacy.ExcludedIndexers...),
			"syncNzbHydra2Proxy": cfg.Privacy.SyncNZBHydra2Proxy,
			"socks5": map[string]any{
				"host":     cfg.Privacy.SOCKS5.Host,
				"port":     cfg.Privacy.SOCKS5.Port,
				"username": cfg.Privacy.SOCKS5.Username,
				"password": "***",
			},
			"wireguard": map[string]any{
				"configured": cfg.Privacy.WireGuard.ConfigText != "",
			},
		},
	}
}

func redactUsenetProviders(providers []UsenetProvider) []map[string]any {
	out := make([]map[string]any, 0, len(providers))
	for _, provider := range providers {
		out = append(out, map[string]any{
			"name":           provider.Name,
			"host":           provider.Host,
			"port":           provider.Port,
			"tls":            provider.TLS,
			"username":       provider.Username,
			"password":       "***",
			"maxConnections": provider.MaxConnections,
			"enabled":        provider.Enabled,
		})
	}
	return out
}

func redactSubtitleProviders(providers map[string]SubtitleAuth) map[string]any {
	out := make(map[string]any, len(providers))
	for name, provider := range providers {
		out[name] = map[string]any{
			"enabled":  provider.Enabled,
			"apiKey":   "***",
			"username": provider.Username,
			"password": "***",
		}
	}
	return out
}

// RedactSecrets returns a copy of cfg with every credential field blanked to
// "". Used for the editable /api/settings response: the settings UI round-
// trips this struct (edit a field, PUT the whole thing back), so unlike
// RedactedSettings (a read-only "***" display copy for /api/status), secrets
// here must come back empty — MergeSecrets treats "" as "leave unchanged"
// when the edited settings are saved.
func RedactSecrets(cfg Settings) Settings {
	redacted := cfg
	redacted.Database.Password = ""
	redacted.Valkey.Password = ""
	redacted.NZBHydra2.APIKey = ""
	redacted.SABNZBD.APIKey = ""
	redacted.Seerr.APIKey = ""
	redacted.Metadata.TMDB.APIKey = ""
	redacted.Metadata.TVDB.APIKey = ""
	redacted.Plex.Token = ""
	redacted.Jellyfin.APIKey = ""
	redacted.Privacy.SOCKS5.Password = ""
	redacted.Privacy.WireGuard.ConfigText = ""

	providers := make([]UsenetProvider, len(cfg.Usenet.Providers))
	copy(providers, cfg.Usenet.Providers)
	for i := range providers {
		providers[i].Password = ""
	}
	redacted.Usenet.Providers = providers

	subProviders := make(map[string]SubtitleAuth, len(cfg.Subtitles.Providers))
	for name, auth := range cfg.Subtitles.Providers {
		auth.APIKey = ""
		auth.Password = ""
		subProviders[name] = auth
	}
	redacted.Subtitles.Providers = subProviders
	return redacted
}

// MergeSecrets fills any blank credential field in incoming with the
// matching value from current, so leaving a password/apiKey field untouched
// in the settings UI (which now receives "" from RedactSecrets) doesn't wipe
// the stored secret on save. Usenet providers are matched by Name; subtitle
// providers by map key. A provider that can't be matched (e.g. renamed in
// the same request as blanking its password) simply keeps its new blank
// value — no data is corrupted, the secret just needs re-entering.
func MergeSecrets(current, incoming Settings) Settings {
	merged := incoming
	if merged.Database.Password == "" {
		merged.Database.Password = current.Database.Password
	}
	if merged.Valkey.Password == "" {
		merged.Valkey.Password = current.Valkey.Password
	}
	if merged.NZBHydra2.APIKey == "" {
		merged.NZBHydra2.APIKey = current.NZBHydra2.APIKey
	}
	if merged.SABNZBD.APIKey == "" {
		merged.SABNZBD.APIKey = current.SABNZBD.APIKey
	}
	if merged.Seerr.APIKey == "" {
		merged.Seerr.APIKey = current.Seerr.APIKey
	}
	if merged.Metadata.TMDB.APIKey == "" {
		merged.Metadata.TMDB.APIKey = current.Metadata.TMDB.APIKey
	}
	if merged.Metadata.TVDB.APIKey == "" {
		merged.Metadata.TVDB.APIKey = current.Metadata.TVDB.APIKey
	}
	if merged.Plex.Token == "" {
		merged.Plex.Token = current.Plex.Token
	}
	if merged.Jellyfin.APIKey == "" {
		merged.Jellyfin.APIKey = current.Jellyfin.APIKey
	}
	if merged.Privacy.SOCKS5.Password == "" {
		merged.Privacy.SOCKS5.Password = current.Privacy.SOCKS5.Password
	}
	if merged.Privacy.WireGuard.ConfigText == "" {
		merged.Privacy.WireGuard.ConfigText = current.Privacy.WireGuard.ConfigText
	}

	currentProvidersByName := make(map[string]UsenetProvider, len(current.Usenet.Providers))
	for _, p := range current.Usenet.Providers {
		currentProvidersByName[p.Name] = p
	}
	providers := make([]UsenetProvider, len(merged.Usenet.Providers))
	copy(providers, merged.Usenet.Providers)
	for i, p := range providers {
		if p.Password == "" {
			if cur, ok := currentProvidersByName[p.Name]; ok {
				providers[i].Password = cur.Password
			}
		}
	}
	merged.Usenet.Providers = providers

	subProviders := make(map[string]SubtitleAuth, len(merged.Subtitles.Providers))
	for name, auth := range merged.Subtitles.Providers {
		if cur, ok := current.Subtitles.Providers[name]; ok {
			if auth.APIKey == "" {
				auth.APIKey = cur.APIKey
			}
			if auth.Password == "" {
				auth.Password = cur.Password
			}
		}
		subProviders[name] = auth
	}
	merged.Subtitles.Providers = subProviders
	return merged
}
