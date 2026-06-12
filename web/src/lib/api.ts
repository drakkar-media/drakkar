import { browser } from '$app/environment';
import type {
  LibraryItem,
  MaintenanceResult,
  QueueItem,
  BulkQueueRetryResult,
  ReleaseItem,
  RequestItem,
  BulkSearchResult,
  BulkRepublishResult,
  DashboardHome,
  Status,
  IntegrationProbeReport,
  DiscoverDetails,
  DiscoverListResult,
  DiscoverSearchResult,
  SubtitleFile,
  SubtitleCandidate,
  BlocklistItem,
  LibraryDetail,
  QualityProfile,
  TaskSchedule,
  PolicySettings
} from '$lib/types';

function baseURL() {
  if (!browser) {
    return 'http://localhost:8080';
  }
  return window.location.origin;
}

function eventsURL() {
  return `${baseURL()}/api/events`;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${baseURL()}${path}`, init);
  if (!response.ok) {
    const text = await response.text();
    throw new Error(text || `${response.status} ${response.statusText}`);
  }
  return (await response.json()) as T;
}

export const api = {
  status: () => request<Status>('/api/status'),
  dashboardHome: () => request<DashboardHome>('/api/dashboard/home'),
  discoverSearch: (query: string) => request<DiscoverSearchResult>(`/api/discover/search?query=${encodeURIComponent(query)}`),
  discoverList: (mediaType: 'movie' | 'tv', page = 1) => request<DiscoverListResult>(`/api/discover/${mediaType}?page=${page}`),
  discoverDetails: (mediaType: 'movie' | 'tv', query: { title?: string; year?: number; tmdbId?: number; imdbId?: string }) => {
    const params = new URLSearchParams();
    if (query.title) params.set('title', query.title);
    if (query.year) params.set('year', String(query.year));
    if (query.tmdbId) params.set('tmdbId', String(query.tmdbId));
    if (query.imdbId) params.set('imdbId', query.imdbId);
    return request<DiscoverDetails>(`/api/discover/details/${mediaType}?${params.toString()}`);
  },
  probeIntegrations: () => request<IntegrationProbeReport>('/api/integrations/probe', { method: 'POST' }),
  queue: () => request<{ items: QueueItem[] }>('/api/queue'),
  retryQueue: (queueItemID: number) =>
    request<{ queueItemId: number; action: string; selectedReleaseId?: number; searchCandidateCount?: number }>(`/api/queue/${queueItemID}/retry`, { method: 'POST' }),
  retryFailedQueue: () => request<BulkQueueRetryResult>('/api/queue/retry-failed', { method: 'POST' }),
  clearFailedQueue: () => request<{ cleared: number }>('/api/queue/clear-failed', { method: 'POST' }),
  requests: () => request<{ requests: RequestItem[] }>('/api/requests'),
  library: () => request<{ items: LibraryItem[] }>('/api/library'),
  librarySearch: (query: string) => request<{ items: LibraryItem[] }>(`/api/library/search?q=${encodeURIComponent(query)}`),
  libraryDetail: (libraryItemID: number) => request<LibraryDetail>(`/api/library/${libraryItemID}/details`),
  libraryMissing: () => request<{ items: LibraryItem[] }>('/api/library/missing'),
  releases: (libraryItemID: number) => request<{ items: ReleaseItem[] }>(`/api/releases/${libraryItemID}`),
  subtitles: (libraryItemID: number) => request<{ items: SubtitleFile[] }>(`/api/subtitles/${libraryItemID}`),
  subtitleCandidates: (libraryItemID: number) => request<{ items: SubtitleCandidate[] }>(`/api/subtitle-candidates/${libraryItemID}`),
  blocklist: () => request<{ items: BlocklistItem[] }>('/api/blocklist'),
  syncRequests: () => request<{ seen: number; created: number }>('/api/requests/sync', { method: 'POST' }),
  searchPendingLibrary: () => request<BulkSearchResult>('/api/library/search-pending', { method: 'POST' }),
  searchLibrary: (libraryItemID: number) =>
    request<{ candidateCount: number; selectedReleaseId?: number }>(`/api/library/${libraryItemID}/search`, { method: 'POST' }),
  republishLibrary: (libraryItemID: number) =>
    request<{ status: string; libraryItemId: number }>(`/api/library/${libraryItemID}/republish`, { method: 'POST' }),
  restoreRejectedLibrary: (libraryItemID: number) =>
    request<{ libraryItemId: number; restored: number }>(`/api/library/${libraryItemID}/restore-rejected`, { method: 'POST' }),
  republishPendingLibrary: () => request<BulkRepublishResult>('/api/library/republish-pending', { method: 'POST' }),
  selectRelease: (releaseCandidateID: number) =>
    request<{ releaseCandidateId: number; action: string; selectedReleaseId?: number }>(`/api/releases/${releaseCandidateID}/select`, { method: 'POST' }),
  rejectRelease: (releaseCandidateID: number, reason = 'manual_reject') =>
    request<{ releaseCandidateId: number; action: string; selectedReleaseId?: number }>(`/api/releases/${releaseCandidateID}/reject`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ reason })
    }),
  restoreRelease: (releaseCandidateID: number) =>
    request<{ releaseCandidateId: number; action: string; selectedReleaseId?: number }>(`/api/releases/${releaseCandidateID}/restore`, { method: 'POST' }),
  skipRelease: (releaseCandidateID: number) =>
    request<{ releaseCandidateId: number; action: string; selectedReleaseId?: number }>(`/api/releases/${releaseCandidateID}/skip`, { method: 'POST' }),
  maintenance: (task: 'orphaned-content' | 'broken-media-symlinks' | 'orphaned-completed-symlinks') =>
    request<MaintenanceResult>(`/api/maintenance/${task}`, { method: 'POST' }),
  pruneCache: () => request<{ root: string; filesBefore: number; filesAfter: number; bytesBefore: number; bytesAfter: number; deletedFiles: number; deletedBytes: number; limitBytes: number }>('/api/cache/prune', { method: 'POST' }),
  clearBlocklist: (id: number) => request<{ status: string; blocklistItemId: number }>(`/api/blocklist/${id}`, { method: 'DELETE' }),
  clearAllBlocklist: () => request<{ cleared: number }>('/api/blocklist', { method: 'DELETE' }),
  searchSubtitles: (libraryItemID: number, languages: string[]) =>
    request<{ libraryItemId: number; candidateCount: number }>(`/api/subtitles/${libraryItemID}/search`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ languages })
    }),
  downloadSubtitleCandidate: (candidateID: number) =>
    request<{ libraryItemId: number; language: string; provider: string; createdPaths: string[] }>(`/api/subtitle-candidates/${candidateID}/download`, { method: 'POST' }),
  uploadSubtitle: async (libraryItemID: number, language: string, file: File) => {
    const form = new FormData();
    form.set('language', language);
    form.set('file', file);
    return request<{ libraryItemId: number; language: string; provider: string; createdPaths: string[] }>(`/api/subtitles/${libraryItemID}/upload`, {
      method: 'POST',
      body: form
    });
  },
  deleteSubtitle: (subtitleID: number) =>
    request<{ status: string; subtitleFileId: number }>(`/api/subtitle-files/${subtitleID}`, { method: 'DELETE' }),
  metrics: () => request<Record<string, number>>('/api/metrics'),
  listProfiles: () => request<{ profiles: QualityProfile[] }>('/api/profiles'),
  saveProfile: (p: QualityProfile) => request<QualityProfile>('/api/profiles', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(p) }),
  deleteProfile: (id: number) => request<{ deleted: number }>(`/api/profiles/${id}`, { method: 'DELETE' }),
  taskSchedules: () => request<{ items: TaskSchedule[] }>('/api/tasks/schedules'),
  policies: () => request<PolicySettings>('/api/policies'),
  savePolicies: (settings: PolicySettings) =>
    request<PolicySettings>('/api/policies', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(settings)
    }),
  healthSummary: () => request<{ total: number; checked: number; healthy: number; neverChecked: number }>('/api/health/summary'),
  healthEntries: () => request<{ items: { id: number; libraryItemId: number; libraryPath: string; targetPath: string; createdAt: string; lastCheckedAt?: string; healthOk?: boolean }[] }>('/api/health/entries'),
  runHealthCheck: () => request<{ checked: number; healthy: number }>('/api/health/check', { method: 'POST' }),
  backfillMetadata: () => request<{ processedMovies: number; processedShows: number; enriched: number; failed: number }>('/api/library/backfill-metadata', { method: 'POST' }),
  fillMissingEpisodes: () => request<{ showsProcessed: number; episodesFound: number; itemsCreated: number }>('/api/library/fill-missing-episodes', { method: 'POST' }),
  logs: (opts?: { limit?: number; level?: string }) => {
    const params = new URLSearchParams();
    if (opts?.limit) params.set('limit', String(opts.limit));
    if (opts?.level) params.set('level', opts.level);
    return request<{ lines: { raw: string }[] }>(`/api/logs?${params.toString()}`);
  },
  vfs: (path?: string) => {
    const params = new URLSearchParams();
    if (path) params.set('path', path);
    return request<{ entries: { name: string; isDir: boolean; size?: number }[] }>(`/api/vfs?${params.toString()}`);
  },
  // Plex integration
  plexTest: () => request<{ ok: boolean; serverName?: string; libraries?: { key: string; title: string; type: string }[]; error?: string }>('/api/plex/test', { method: 'POST' }),
  plexRefresh: () => request<{ status: string }>('/api/plex/refresh', { method: 'POST' }),
  plexLibraries: () => request<{ libraries: { key: string; title: string; type: string }[] }>('/api/plex/libraries'),
  // Manual search via Hydra
  manualSearch: (query: string, kind: 'movie' | 'tv' | 'all' = 'all') =>
    request<{ items: { title: string; externalUrl: string; indexer: string; sizeBytes: number; score: number; resolution: string; source: string; codec: string }[] }>(
      `/api/search/manual?q=${encodeURIComponent(query)}&kind=${kind}`
    ),
  // Library replacement — find better release for existing item
  searchReplacements: (libraryItemID: number) =>
    request<{ candidateCount: number; selectedReleaseId?: number }>(`/api/library/${libraryItemID}/search`, { method: 'POST' }),
  // Release calendar — library items by release/air date
  releaseCalendar: (month?: string) => {
    const params = new URLSearchParams();
    if (month) params.set('month', month);
    return request<{ entries: { id: number; libraryItemId: number; type: string; title: string; releaseDate: string; tmdbId?: number; posterUrl?: string; available: boolean; queueState?: string }[] }>(`/api/release-calendar?${params.toString()}`);
  },
  // Active VFS streams
  streams: () => request<{ sessions: { sessionId: string; virtualFileId: number; fileName: string; fileSizeBytes: number; openedAt: string; currentOffset: number }[] }>('/api/streams'),
  stopStream: (sessionId: string) => request<{ stopped: boolean }>(`/api/streams/${encodeURIComponent(sessionId)}/stop`, { method: 'POST' }),
  // Per-library item quality profile override
  setLibraryProfile: (libraryItemID: number, profileId: number) =>
    request<{ libraryItemId: number; profileId: number }>(`/api/library/${libraryItemID}/profile`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ profileId })
    })
};

export function subscribeEvents(onMessage: () => void): () => void {
  if (!browser) {
    return () => {};
  }
  const source = new EventSource(eventsURL());
  source.addEventListener('message', () => onMessage());
  source.onerror = () => {};
  return () => source.close();
}
