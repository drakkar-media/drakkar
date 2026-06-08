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
  preferProper: boolean;
  preferRepack: boolean;
  rejectCam: boolean;
  minSizeMb: number;
  maxSizeMb: number;
  createdAt?: string;
  updatedAt?: string;
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

export type QueueDecisionAction =
  | 'do_nothing'
  | 'remove'
  | 'remove_and_blocklist'
  | 'remove_blocklist_and_search'
  | 'search_again';

export type PolicySettings = {
  queueDecisionActions: Record<string, QueueDecisionAction>;
  ignoredPatterns: string[];
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

export type BlocklistItem = {
  id: number;
  key: string;
  reason: string;
  expiresAt?: string;
};
