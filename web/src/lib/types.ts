export type QualityProfile = {
  id?: number;
  name: string;
  isDefault: boolean;
  resolutions: string[];
  sources: string[];
  codecs: string[];
  languages: string[];
  audioFormats: string[];
  hdrFormats: string[];
  excludePatterns: string[];
  preferProper: boolean;
  preferRepack: boolean;
  rejectCam: boolean;
  allowUpgrade: boolean;
  minimumUpgradeCustomFormatScore: number;
  cutoffResolution: string;
  minimumAgeHours: number;
  minMbPerMinute: number;
  maxMbPerMinute: number;
  createdAt?: string;
  updatedAt?: string;
};

export type GrabHistoryEntry = {
  id: number;
  libraryItemId: number;
  releaseCandidateId?: number;
  title: string;
  indexerName: string;
  score: number;
  resolution: string;
  grabbedAt: string;
};

export type CustomFormat = {
  id?: number;
  name: string;
  pattern: string;
  score: number;
  enabled: boolean;
  source?: string;
};

export type SubtitleProfile = {
  id?: number;
  name: string;
  languages: string[];
  preferHearingImpaired: boolean;
  requireExactLanguage: boolean;
  isDefault: boolean;
  createdAt?: string;
  updatedAt?: string;
};

export type IndexerPolicy = {
  id?: number;
  indexerName: string;
  scoreModifier: number;
  enabled: boolean;
  note: string;
  createdAt?: string;
  updatedAt?: string;
};

export type ReleaseBlockRule = {
  id?: number;
  type: 'release_group' | 'title_pattern' | 'regex' | 'missing_release_group';
  pattern: string;
  mediaType: 'movie' | 'tv' | 'both';
  action: 'block' | 'penalty';
  scorePenalty: number;
  enabled: boolean;
  source: 'default' | 'trash' | 'custom';
  note: string;
  createdAt?: string;
  updatedAt?: string;
};

export type BlockTestMatch = {
  ruleId: number;
  type: string;
  pattern: string;
  action: string;
  reason: string;
};

export type BlockTestResult = {
  allowed: boolean;
  blocked: boolean;
  scorePenalty: number;
  matchedRules: BlockTestMatch[];
};

export type QualityDefinition = {
  id: number;
  mediaType: string;
  qualityKey: string;
  title: string;
  minMbPerMinute: number;
  maxMbPerMinute: number;
  sortOrder: number;
};

export type IntegrationStatus = {
  enabled: boolean;
  configured: boolean;
  detail?: string;
  count?: number;
};

export type Integrations = {
  seerr: IntegrationStatus;
  nzbhydra2: IntegrationStatus;
  usenet: IntegrationStatus;
  tmdb: IntegrationStatus;
  tvdb: IntegrationStatus;
  subtitles: IntegrationStatus;
  subtitleProviders: Record<string, IntegrationStatus>;
};

export type Status = {
  service: string;
  version: string;
  healthy: boolean;
  startedAt: string;
  settings: Record<string, unknown>;
  integrations: Integrations;
  fuseMountPath: string;
  diskCacheLimitBytes: number;
  readAheadLimitBytes: number;
  memoryHotCacheBytes: number;
  backgroundQueueDepth: number;
};

export type IntegrationProbeResult = {
  name: string;
  ok: boolean;
  detail: string;
  checkedAt: string;
  durationMs: number;
};

export type IntegrationProbeReport = {
  checkedAt: string;
  results: IntegrationProbeResult[];
};

export type QueueItem = {
  queueItemId: number;
  libraryItemId: number;
  libraryTitle: string;
  state: string;
  failureReason: string;
  selectedReleaseId?: number;
  nzbDocumentId?: number;
  nzbFileName?: string;
  nzbFileCount: number;
  nzbSegmentCount: number;
  onHold: boolean;
  createdAt?: string;
  updatedAt?: string;
};

export type WorkQueueStatus = {
  paused: boolean;
  depth: number;
};

export type BulkQueueRetryResult = {
  processed: number;
  retried: number;
  failed: number;
  processedQueues?: number[];
  failedQueues?: number[];
};

export type RequestItem = {
  id: number;
  externalId: string;
  requestType: string;
  title: string;
  mediaType: string;
  libraryItemId?: number;
  qualityProfileId?: number;
  qualityProfileName?: string;
  queueState: string;
  createdAt: string;
};

export type BulkSearchResult = {
  processed: number;
  searched: number;
  selected: number;
  failed: number;
  processedItems?: number[];
  failedItems?: number[];
};

export type BulkRepublishResult = {
  processed: number;
  republished: number;
  failed: number;
  processedLibrary?: number[];
  failedLibrary?: number[];
};

export type TaskSchedule = {
  id: string;
  label: string;
  group: string;
  interval: string;
  automated: boolean;
  lastRunAt?: string;
  lastRunState: string;
};

/**
 * Automated response applied to a queue item when housekeeping detects a
 * given failure/decision reason (see {@link PolicySettings.queueDecisionActions}).
 */
export type QueueDecisionAction =
  | 'do_nothing'
  | 'remove'
  | 'remove_and_blocklist'
  | 'remove_blocklist_and_search'
  | 'search_again';

export type PolicySettings = {
  /** Keyed by queue failure/decision reason code. */
  queueDecisionActions: Record<string, QueueDecisionAction>;
  ignoredPatterns: string[];
  duplicateNzbBehavior: string;
  failNzbWithoutVideo: boolean;
  importStrategy: string;
  manualUploadCategory: string;
  blocklistTtlDays: number;
};

export type LibraryItem = {
  id: number;
  mediaType: string;
  title: string;
  year?: number;
  overview?: string;
  posterUrl?: string;
  backdropUrl?: string;
  available: boolean;
  requestedAt: string;
  queueState: string;
  failureReason: string;
  selectedReleaseId?: number;
  tmdbId?: number;
  tvdbId?: number;
  imdbId?: string;
  availableCount?: number;
  missingCount?: number;
  seasonNumber?: number;
  episodeNumber?: number;
};

export type LibraryPage = {
  items: LibraryItem[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
  totalMonitored: number;
  sumAvailable: number;
  sumMissing: number;
  countActive: number;
};

export type DashboardHome = {
  hero?: LibraryItem;
  recentlyAdded: LibraryItem[];
  trendingMovies: LibraryItem[];
  trendingTv: LibraryItem[];
};

export type DiscoverMediaItem = {
  mediaType: string;
  title: string;
  year?: number;
  overview?: string;
  posterUrl?: string;
  backdropUrl?: string;
  tmdbId?: number;
  imdbId?: string;
};

export type DiscoverSearchResult = {
  movies: DiscoverMediaItem[];
  tv: DiscoverMediaItem[];
};

export type DiscoverListResult = {
  page: number;
  totalPages: number;
  items: DiscoverMediaItem[];
};

export type DiscoverCast = {
  id?: number;
  name: string;
  character?: string;
  profileUrl?: string;
};

export type DiscoverDetails = {
  mediaType: string;
  title: string;
  year?: number;
  overview?: string;
  tagline?: string;
  posterUrl?: string;
  backdropUrl?: string;
  tmdbId?: number;
  imdbId?: string;
  originalLanguage?: string;
  runtimeMinutes?: number;
  status?: string;
  network?: string;
  numberOfSeasons?: number;
  numberOfEpisodes?: number;
  voteAverage?: number;
  voteCount?: number;
  budget?: number;
  revenue?: number;
  genres?: string[];
  productionCompanies?: string[];
  cast?: DiscoverCast[];
  recommendations?: DiscoverMediaItem[];
  similar?: DiscoverMediaItem[];
};

/** The actual media file currently backing a library item's selected release -- what's really being served, not a log of past grab attempts. */
export type CurrentFile = {
  fileName: string;
  fileSizeBytes: number;
  releaseTitle: string;
  indexerName?: string;
  resolution?: string;
  score: number;
  subtitleLanguages?: string[];
};

export type LibraryDetail = {
  id: number;
  mediaType: string;
  title: string;
  year?: number;
  overview?: string;
  posterUrl?: string;
  backdropUrl?: string;
  available: boolean;
  queueState: string;
  failureReason: string;
  selectedReleaseId?: number;
  tmdbId?: number;
  tvdbId?: number;
  imdbId?: string;
  availableCount: number;
  missingCount: number;
  seasons: SeasonDetail[];
  tvShowId?: number;
  monitoringMode?: string;
  currentFile?: CurrentFile;
};

export type SeasonDetail = {
  seasonNumber: number;
  name: string;
  episodeCount: number;
  availableCount: number;
  missingCount: number;
  episodes: EpisodeDetail[];
};

export type EpisodeDetail = {
  seasonNumber: number;
  episodeNumber: number;
  title: string;
  status: string;
  libraryItemId?: number;
  /** Languages this episode already has a downloaded subtitle for, at a glance without expanding its subtitle panel. */
  subtitleLanguages?: string[];
  airDate?: string;
};

export type ReleaseItem = {
  selectedReleaseId: number;
  releaseCandidateId: number;
  libraryItemId: number;
  title: string;
  externalUrl?: string;
  indexerName?: string;
  sizeBytes: number;
  postedAt?: string;
  score: number;
  customFormatScore: number;
  selected: boolean;
  rejected: boolean;
  rejectReason: string;
  failureCount: number;
  lastFailureReason: string;
  archiveCount: number;
  archiveVolumeCount: number;
  archiveStatuses: string;
  archiveRejects: string;
  archives?: ReleaseArchive[];
  failedAttempts?: FailedReleaseAttempt[];
  explanations?: string[];
  compatibilityWarnings?: string[];
  nzbDocumentId?: number;
  nzbFileName?: string;
};

export type FailedReleaseAttempt = {
  reason: string;
  createdAt: string;
};

export type ReleaseArchive = {
  kind: string;
  status: string;
  rejectReason: string;
  volumeCount: number;
  entries?: ReleaseArchiveEntry[];
};

export type ReleaseArchiveEntry = {
  path: string;
  sizeBytes: number;
  packedSizeBytes: number;
  compressionMethod: string;
  encrypted: boolean;
  solid: boolean;
  sourceVolumeIndex: number;
  sourceArchiveOffset: number;
};

export type MaintenanceResult = {
  taskName: string;
  deletedFiles: number;
  deletedRows: number;
  scannedFiles: number;
  scannedRows: number;
  resetItems: number;
};

export type SubtitleFile = {
  id: number;
  libraryItemId: number;
  provider: string;
  language: string;
  path: string;
  createdAt: string;
};

export type SubtitleCandidate = {
  id: number;
  libraryItemId: number;
  provider: string;
  language: string;
  title: string;
  releaseName: string;
  format: string;
  hearingImpaired: boolean;
  score: number;
  externalId: string;
  createdAt: string;
};

export type ManualSearchItem = {
  title: string;
  externalUrl: string;
  indexer: string;
  sizeBytes: number;
  score: number;
  resolution?: string;
  source?: string;
  codec?: string;
  audio?: string;
  hdr?: string;
};

export type SubtitleLibraryRow = {
  libraryItemId: number;
  mediaType: string;
  title: string;
  showTitle?: string;
  seasonNumber?: number;
  episodeNumber?: number;
  available: boolean;
  languages: string[];
  candidateCount: number;
  requestedAt: string;
};

export type SubtitleLibraryPage = {
  items: SubtitleLibraryRow[];
  page: number;
  pageSize: number;
  total: number;
  totalPages: number;
};

export type BlocklistItem = {
  id: number;
  key: string;
  keyType?: string;
  reason: string;
  createdAt: string;
  expiresAt?: string;
  selectedReleaseId?: number;
  libraryItemId?: number;
  releaseTitle?: string;
  indexerName?: string;
  sizeBytes?: number;
  postedAt?: string;
};

// ReleaseActionResult is the shared response shape for the release actions
// (select/reject/restore/skip/manual-import) that mutate a release
// candidate's selection state.
export type ReleaseActionResult = {
  releaseCandidateId: number;
  action: string;
  selectedReleaseId?: number;
};

// DeletedCount is the shared response shape for the various "delete one
// record" endpoints (profiles, block rules, custom formats, indexer
// policies, subtitle profiles).
export type DeletedCount = { deleted: number };

export type SpeedTestResult = {
  fileName: string;
  fileSizeBytes: number;
  bytesRead: number;
  durationSeconds: number;
  throughputMbps: number;
  cpuPercent: number;
};

// QueuedResult is the shared response shape for endpoints that enqueue a
// background job rather than returning a result synchronously.
export type QueuedResult = { queued: boolean };

export type BlocklistMutation = {
  key: string;
  /**
   * How `key` should be matched against future release candidates:
   * `raw` for an exact stored value, `external_url` to block a specific
   * indexer URL, or `release_signature` to block by normalized
   * title/size/indexer fingerprint. Omitted when the backend should derive
   * it from the other fields.
   */
  keyType?: 'raw' | 'external_url' | 'release_signature';
  externalUrl?: string;
  releaseTitle?: string;
  indexerName?: string;
  sizeMb?: number;
  postedDate?: string;
  reason: string;
  expiresAt?: string;
};

export type UsenetProvider = {
  name: string;
  host: string;
  port: number;
  tls: boolean;
  username: string;
  password: string;
  maxConnections: number;
  priority: number;
  retentionDays: number;
  backup: boolean;
  enabled: boolean;
};

/**
 * Complete backend configuration, as read from and written to
 * `/api/settings`.
 *
 * Mirrors the server-side settings struct; sensitive fields (passwords, API
 * keys, WireGuard config text) are returned to authenticated clients as-is
 * and are expected to be redacted/masked by the UI, not by this type.
 */
export type FullSettings = {
  database: { host: string; port: number; name: string; username: string; password: string };
  valkey: { host: string; port: number; password: string };
  nzbhydra2: { url: string; apiKey: string; searchCacheTtlSeconds: number; feedCacheTtlSeconds: number; feedMaxResults: number };
  seerr: { url: string; apiKey: string; searchCacheTtlSeconds: number; feedCacheTtlSeconds: number; feedMaxResults: number };
  usenet: {
    maxDownloadConnections: number;
    streamingPriorityPercent: number;
    articleBufferSize: number;
    providers: UsenetProvider[];
  };
  metadata: { tmdb: { apiKey: string }; tvdb: { apiKey: string }; language: string; cacheTtlHours: number };
  subtitles: {
    enabled: boolean;
    languages: string[];
    providers: Record<string, { enabled: boolean; apiKey: string; username: string; password: string }>;
  };
  plex: { url: string; token: string; sectionKey: string };
  jellyfin: { url: string; apiKey: string };
  library: { defaultMovieProfile: string; defaultTvProfile: string };
  indexer: {
    tvRssSyncIntervalMinutes: number;
    movieRssSyncIntervalMinutes: number;
    minimumAgeMinutes: number;
    retentionDays: number;
    maximumSizeMB: number;
    searchDelayMs: number;
    backgroundSearchWorkers: number;
    releaseGraceHours: number;
  };
  notifications: {
    discordWebhookUrl: string;
    genericWebhookUrl: string;
    onGrab: boolean;
    onAvailable: boolean;
    onFailed: boolean;
  };
  privacy: {
    mode: 'direct' | 'socks5' | 'wireguard';
    socks5: { host: string; port: number; username: string; password: string; timeoutSeconds: number };
    wireguard: { configText: string; timeoutSeconds: number };
    // Indexers exempted from the privacy tunnel, e.g. ones that are already
    // trusted or that block traffic from VPN/proxy IP ranges.
    excludedIndexers: string[];
    // When true, pushes the SOCKS5 config into NZBHydra2's own proxy
    // settings on every save (NZBHydra2 is a separate process Drakkar can't
    // route traffic for otherwise). Only meaningful for SOCKS5 mode.
    syncNzbHydra2Proxy: boolean;
  };
};

/**
 * Live status of the currently-active privacy routing tunnel.
 *
 * `protectedTraffic` lists which outbound traffic categories (e.g. indexer,
 * NNTP) are actually routed through the tunnel — not all traffic is
 * necessarily protected depending on `mode` and settings. `wireguard` is
 * populated only when `mode` is `'wireguard'` and the interface is up.
 */
export type PrivacyStatus = {
  mode: 'direct' | 'socks5' | 'wireguard';
  status: string;
  protectedTraffic: string[];
  endpoint?: string;
  error?: string;
  wireguard?: {
    interfaceAddress?: string[];
    dns?: string[];
    endpoint?: string;
    allowedIps?: string[];
    persistentKeepalive?: number;
  };
};

export type User = {
  id: number;
  username: string;
  role: string;
  createdAt: string;
};

export type APIToken = {
  id: number;
  userId: number;
  name: string;
  createdAt: string;
  lastUsedAt?: string;
  expiresAt?: string;
};
