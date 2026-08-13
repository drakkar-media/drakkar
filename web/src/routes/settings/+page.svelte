<script lang="ts">
  /**
   * Central settings console covering every configurable subsystem: Integrations,
   * Providers, Indexers, Queue, Library, Rules, Quality, Custom Formats, Release
   * Filtering, Subtitle Profiles, Notifications, Privacy Routing, Logs, Tasks,
   * Media Players, Speed Test, and System — one tab per concern, selected via
   * `activeTab`.
   *
   * Edits use a draft/fullSettings pattern: `loadAll()` fetches the server's
   * `FullSettings` into `fullSettings` and deep-clones it into `draft`, and every
   * form control in every tab binds to fields on `draft`. Nothing reaches the
   * backend until `saveSettings()` PUTs the whole `draft` object and reconciles
   * both `fullSettings` and `draft` with the server's response. Because `loadAll()`
   * is re-triggered continuously (SSE events, a 30s poll), it intentionally skips
   * overwriting `draft` whenever `draft` differs from the last-loaded
   * `fullSettings` — otherwise an unrelated background event would silently
   * discard whatever the operator was mid-editing.
   */
  import { onMount } from 'svelte';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Wrench from '@lucide/svelte/icons/wrench';
  import X from '@lucide/svelte/icons/x';
  import Plus from '@lucide/svelte/icons/plus';
  import Search from '@lucide/svelte/icons/search';
  import Save from '@lucide/svelte/icons/save';
  import Tv from '@lucide/svelte/icons/tv';
  import PlugZap from '@lucide/svelte/icons/plug-zap';
  import Wifi from '@lucide/svelte/icons/wifi';
  import Settings2 from '@lucide/svelte/icons/settings-2';
  import FolderTree from '@lucide/svelte/icons/folder-tree';
  import ShieldAlert from '@lucide/svelte/icons/shield-alert';
  import ClipboardList from '@lucide/svelte/icons/clipboard-list';
  import SlidersHorizontal from '@lucide/svelte/icons/sliders-horizontal';
  import ScrollText from '@lucide/svelte/icons/scroll-text';
  import Library from '@lucide/svelte/icons/library';
  import Play from '@lucide/svelte/icons/play';
  import Clock3 from '@lucide/svelte/icons/clock-3';
  import CheckCircle2 from '@lucide/svelte/icons/check-circle-2';
  import AlertTriangle from '@lucide/svelte/icons/alert-triangle';
  import Star from '@lucide/svelte/icons/star';
  import Ban from '@lucide/svelte/icons/ban';
  import ChevronUp from '@lucide/svelte/icons/chevron-up';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import Pencil from '@lucide/svelte/icons/pencil';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import Copy from '@lucide/svelte/icons/copy';
  import Check from '@lucide/svelte/icons/check';
  import Webhook from '@lucide/svelte/icons/webhook';
  import Languages from '@lucide/svelte/icons/languages';
  import Gauge from '@lucide/svelte/icons/gauge';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import Upload from '@lucide/svelte/icons/upload';
  import Button from '$lib/components/Button.svelte';
  import BackupRestorePanel from '$lib/components/BackupRestorePanel.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Pagination from '$lib/components/Pagination.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import * as Table from '$lib/components/ui/table/index.js';
  import * as Select from '$lib/components/ui/select/index.js';
  import { api, subscribeEvents } from '$lib/api';
  import { bytes, dateTime } from '$lib/format';
  import { toastError, toastSuccess } from '$lib/toast';
  import { copyToClipboard } from '$lib/clipboard';
  import { runAction, confirmed } from '$lib/actions';
  import { debounce } from '$lib/debounce';
  import type { BlocklistItem, BlocklistMutation, BlockTestResult, CustomFormat, FullSettings, IndexerPolicy, IntegrationProbeReport, PolicySettings, PrivacyStatus, QualityDefinition, QualityProfile, QueueDecisionAction, ReleaseBlockRule, SpeedTestResult, Status, SubtitleProfile, TaskSchedule, UsenetProvider } from '$lib/types';

  type SettingsTab = 'integrations' | 'providers' | 'indexers' | 'queue' | 'library' | 'rules' | 'quality' | 'formats' | 'filtering' | 'subtitle-profiles' | 'notifications' | 'privacy' | 'logs' | 'tasks' | 'media-players' | 'speed-test' | 'system';

  const tabs: { id: SettingsTab; label: string; short: string; icon: typeof PlugZap }[] = [
    { id: 'integrations',  label: 'Integrations',  short: 'Apps',     icon: PlugZap },
    { id: 'providers',     label: 'Providers',     short: 'Feeds',    icon: Wifi },
    { id: 'indexers',      label: 'Indexers',      short: 'Indexers', icon: Search },
    { id: 'queue',         label: 'Queue',         short: 'Queue',    icon: Settings2 },
    { id: 'library',       label: 'Library',       short: 'Names',    icon: Library },
    { id: 'rules',         label: 'Blocklist',     short: 'Blocklist', icon: ShieldAlert },
    { id: 'quality',       label: 'Quality',       short: 'Quality',  icon: SlidersHorizontal },
    { id: 'formats',       label: 'Custom Formats',    short: 'Formats',   icon: Star },
    { id: 'filtering',        label: 'Release Filtering', short: 'Filtering', icon: Ban },
    { id: 'subtitle-profiles', label: 'Subtitle Profiles', short: 'Subtitles', icon: Languages },
    { id: 'notifications', label: 'Notifications',     short: 'Notify',    icon: Webhook },
    { id: 'privacy',       label: 'Privacy Routing',    short: 'Privacy',   icon: ShieldCheck },
    { id: 'logs',          label: 'Logs',          short: 'Logs',     icon: ScrollText },
    { id: 'tasks',         label: 'Tasks',         short: 'Tasks',    icon: ClipboardList },
    { id: 'media-players', label: 'Media Players', short: 'Players',  icon: Tv },
    { id: 'speed-test',    label: 'Speed Test',    short: 'Speed',    icon: Gauge },
    { id: 'system',        label: 'System',        short: 'System',   icon: FolderTree }
  ];

  const REASONS = ['manual', 'archive_rejected', 'missing_articles', 'nzb_parse_failed', 'unsupported_archive', 'no_video_content', 'wrong_title', 'quality_rejected'] as const;

  let status: Status | null = null;
  let fullSettings: FullSettings | null = null;
  let draft: FullSettings | null = null;
  let loading = true;
  let busy: Record<string, boolean> = {};
  function isBusy(key: string): boolean {
    return !!busy[key];
  }
  function setBusy(key: string, value: boolean) {
    busy = { ...busy, [key]: value };
  }
  function anyBusy(): boolean {
    return Object.values(busy).some(Boolean);
  }
  let blocklist: BlocklistItem[] = [];
  let blPage = 1;
  let blPageSize = 50;
  let blTotal = 0;
  let blTotalPages = 1;
  let blLoading = false;
  let blStats: { total: number; active: number; expired: number; byReason: Record<string, number> } | null = null;
  let lastProbe: IntegrationProbeReport | null = null;
  let profiles: QualityProfile[] = [];
  function profileOptionLabel(name: string): string {
    if (!name) return '— none —';
    const p = profiles.find((x) => x.name === name);
    return p ? `${p.name}${p.isDefault ? ' (default)' : ''}` : name;
  }
  let qualityDefs: QualityDefinition[] = [];
  let qualityDefsDirty: Set<number> = new Set();
  let qualityDefsSaving: Set<number> = new Set();
  let qualitySubTab: 'profiles' | 'definitions' = 'profiles';
  let policySettings: PolicySettings | null = null;
  let privacyStatus: PrivacyStatus | null = null;
  let privacyTestResult: { ok: boolean; error?: string } | null = null;
  let wireguardImportOpen = false;
  let wireguardImportText = '';
  /** Enabled indexer names from NZBHydra2, for the exclusion-list picker. Empty when NZBHydra2 isn't reachable — the UI falls back to manual entry. */
  let knownIndexerNames: string[] = [];
  let manualExcludedIndexer = '';

  let lastCachePrune: { root: string; filesBefore: number; filesAfter: number; bytesBefore: number; bytesAfter: number; deletedFiles: number; deletedBytes: number; limitBytes: number } | null = null;
  let activeTab: SettingsTab = 'integrations';
  let speedTestResults: SpeedTestResult[] = [];

  let blockQuery = '';
  let blockReasonFilter = 'all';
  let blockSortCol: 'reason' | 'key' | 'expires' | 'createdAt' = 'createdAt';
  let blockSortDir: 'asc' | 'desc' = 'desc';
  let blShowAllReasons = false;
  const BL_REASON_CHIP_LIMIT = 10;
  type BlocklistEditor = {
    id?: number;
    keyType: 'raw' | 'external_url' | 'release_signature';
    key: string;
    externalUrl: string;
    releaseTitle: string;
    indexerName: string;
    sizeMb: number;
    postedDate: string;
    reason: string;
    expiresAt: string;
  };

  function blankBlocklistEditor(): BlocklistEditor {
    return {
      keyType: 'external_url',
      key: '',
      externalUrl: '',
      releaseTitle: '',
      indexerName: '',
      sizeMb: 0,
      postedDate: '',
      reason: 'manual',
      expiresAt: '',
    };
  }

  let blockEditor: BlocklistEditor = blankBlocklistEditor();

  // ── Seerr webhook ───────────────────────────────────────────────────────────
  let webhookCopied = false;
  let webhookTokenCopied = false;
  let webhookToken: string | null = null;
  let webhookTokenCreating = false;
  $: webhookUrl = (typeof window !== 'undefined' ? window.location.origin : '') + '/api/webhooks/seerr';

  async function copyWebhookUrl() {
    await copyToClipboard(webhookUrl);
    webhookCopied = true;
    setTimeout(() => { webhookCopied = false; }, 2000);
  }

  async function createWebhookToken() {
    webhookTokenCreating = true;
    try {
      const result = await api.createApiToken('seerr-webhook');
      webhookToken = result.token;
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      webhookTokenCreating = false;
    }
  }

  async function copyWebhookToken() {
    if (!webhookToken) return;
    await copyToClipboard(webhookToken);
    webhookTokenCopied = true;
    setTimeout(() => { webhookTokenCopied = false; }, 2000);
  }

  type SABCopyField =
    | 'host'
    | 'port'
    | 'url-base'
    | 'radarr-root'
    | 'sonarr-root'
    | 'docker-volume';
  let sabCopiedField: SABCopyField | null = null;
  $: sabHost = typeof window !== 'undefined' ? window.location.hostname : '';
  $: sabPort = typeof window !== 'undefined'
    ? window.location.port || (window.location.protocol === 'https:' ? '443' : '80')
    : '';
  const sabURLBase = '/sabnzbd';
  const sabRadarrRootFolder = '/mnt/drakkar/media/movies';
  const sabSonarrRootFolder = '/mnt/drakkar/media/tv';
  const sabDockerVolume = '- /mnt/drakkar:/mnt/drakkar:rslave';

  async function copySABField(field: SABCopyField, value: string) {
    await copyToClipboard(value);
    sabCopiedField = field;
    setTimeout(() => {
      if (sabCopiedField === field) sabCopiedField = null;
    }, 2000);
  }

  // ── Logs tab state ──────────────────────────────────────────────────────────
  type LogEntry = { level: string; service: string; message: string; time: string; raw: string };
  let logEntries: LogEntry[] = [];
  let logLoading = false;
  let logLevelFilter = 'all';
  let logTerm = '';
  let logError = '';
  const logPageSize = 100;
  let logPage = 1;
  let logTotal = 0;
  $: logTotalPages = Math.max(1, Math.ceil(logTotal / logPageSize));

  async function loadLogs() {
    logLoading = true;
    logError = '';
    try {
      const data = await api.logs({ page: logPage, pageSize: logPageSize, level: logLevelFilter !== 'all' ? logLevelFilter : undefined });
      logTotal = data.total ?? 0;
      logEntries = (data.lines ?? []).map(({ raw }) => {
        try {
          const obj = JSON.parse(raw);
          return { level: obj.level ?? '', service: obj.service ?? obj.component ?? obj.module ?? '', message: obj.message ?? obj.msg ?? raw, time: obj.time ?? '', raw };
        } catch { return { level: '', service: '', message: raw, time: '', raw }; }
      });
    } catch (e) { logError = e instanceof Error ? e.message : String(e); }
    finally { logLoading = false; }
  }

  function changeLogLevel() {
    logPage = 1;
    void loadLogs();
  }

  function changeLogPage(e: CustomEvent<number>) {
    logPage = e.detail;
    void loadLogs();
  }

  function fmtLogDate(iso: string) {
    if (!iso) return '';
    try { return new Date(iso).toLocaleString('en-GB', { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }); } catch { return iso; }
  }

  $: filteredLogs = logEntries
    .filter(e => (logLevelFilter === 'all' || e.level === logLevelFilter) && (!logTerm || `${e.service} ${e.message} ${e.raw}`.toLowerCase().includes(logTerm.toLowerCase())))
    .sort((a, b) => b.time.localeCompare(a.time));

  // ── Tasks tab state ─────────────────────────────────────────────────────────
  type TaskResult = { ok: boolean; detail: string; ranAt: string };
  type TaskDef = { id: string; label: string; description: string; group: string; interval: string; manual: boolean; run: () => Promise<unknown> };

  let taskRunning: Record<string, boolean> = {};
  let taskResults: Record<string, TaskResult> = {};
  let taskSchedules: TaskSchedule[] = [];
  let taskSchedulesLoading = true;
  // Operations (manual-only actions) collapsed by default -- least
  // frequently needed, and the automated groups above already show live
  // schedule status without expanding anything.
  let collapsedTaskGroups = new Set<string>(['Operations']);
  function toggleTaskGroup(group: string) {
    const next = new Set(collapsedTaskGroups);
    if (next.has(group)) next.delete(group); else next.add(group);
    collapsedTaskGroups = next;
  }

  // Task IDs match backend task scheduler IDs (internal/app/app.go ListTaskSchedules).
  const taskDefs: TaskDef[] = [
    // === Indexing (automated) ===
    { id: 'seerr_sync',          label: 'Sync Seerr Requests',   description: 'Import new and updated requests from Seerr.',                                                      group: 'Indexing',   interval: '10m',  manual: true,  run: async () => { await api.syncRequests();                  return 'started in background'; } },
    { id: 'pending_queue_push',  label: 'Dispatch Pending Queue', description: 'Push pending library items into the bounded background work queue.',                               group: 'Indexing',   interval: '30s',  manual: false, run: async () => '' },
    { id: 'hydra_recent_tv',     label: 'Recent TV Feed',         description: 'Fetch Hydra recent-TV RSS feed and index new TV releases.',                                        group: 'Indexing',   interval: 'RSS',  manual: false, run: async () => '' },
    { id: 'hydra_recent_movie',  label: 'Recent Movie Feed',      description: 'Fetch Hydra recent-movie RSS feed and index new movie releases.',                                  group: 'Indexing',   interval: 'RSS',  manual: false, run: async () => '' },
    { id: 'queue_housekeeping',  label: 'Queue Housekeeping',     description: 'Reset stuck/stale queue items then retry failed downloads. Runs every 10 min.',                   group: 'Indexing',   interval: '10m',  manual: false, run: async () => '' },
    { id: 'backlog_search',      label: 'Backlog Search',         description: 'Search missing library items — one search per show+season per batch, 1-hour cooldown per item.',  group: 'Indexing',   interval: '30m',  manual: true,  run: async () => { await api.searchPendingLibrary();          return 'started in background'; } },
    { id: 'content_maintenance', label: 'Content Maintenance',    description: 'Fill missing episode library items and run quality upgrade searches. Runs every 6 h.',             group: 'Indexing',   interval: '6h',   manual: false, run: async () => '' },
    // === Publishing (automated) ===
    { id: 'publishing_maintenance', label: 'Publishing Maintenance', description: 'Republish pending library items and reset orphaned available items. Runs every 30 min.',       group: 'Publishing', interval: '30m',  manual: false, run: async () => '' },
    // === Maintenance (automated + manual) ===
    { id: 'health_check',        label: 'Symlink Health Check',   description: 'Verify published symlinks still point to valid VFS targets.',                                      group: 'Maintenance',interval: '15m',  manual: true,  run: async () => { await api.runHealthCheck();                 return 'started in background'; } },
    { id: 'nzb_health_check',    label: 'Deep NZB Article Check', description: 'Full NNTP article scan — probes segments, resets missing-article or sample-only publications.',   group: 'Maintenance',interval: '168h', manual: false, run: async () => '' },
    { id: 'article_health_check',label: 'Article Health Check',   description: 'Probe first NNTP segment of every direct-NZB item. Resets items with expired or missing articles.',group:'Maintenance',interval: '6h',   manual: false, run: async () => '' },
    { id: 'storage_maintenance', label: 'Storage Maintenance',    description: 'Remove orphaned VFS content, broken media symlinks, and prune the block cache. Runs every 6 h.',  group: 'Maintenance',interval: '6h',   manual: false, run: async () => '' },
    // === Operations (individually-triggered via API) ===
    // Every entry below except Push Library to Seerr is a finer-grained
    // slice of one of the automated composite tasks above (e.g. Search
    // Quality Upgrades + Fill Missing Episodes together are what Content
    // Maintenance already runs every 6h) -- kept as its own manual "run
    // just this part now" button rather than removed, but the description
    // now says so explicitly. Before this, someone glancing at a full list
    // of automated tasks AND these manual ones with no visible connection
    // between them reasonably read it as unexplained duplication.
    { id: 'retry_failed_queue',       label: 'Retry Failed Queue',       description: 'Immediately retry all failed queue items using current fallback policy. Also runs automatically every 10 min as part of Queue Housekeeping.',                  group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.retryFailedQueue();                    return 'started in background'; } },
    { id: 'search_upgrades',          label: 'Search Quality Upgrades',  description: 'Re-search available items whose quality profile allows a better release. Also runs automatically every 6 h as part of Content Maintenance.',                  group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.searchUpgrades();                       return 'started in background'; } },
    // fill_missing_episodes/cache_prune/backfill_metadata/seerr_push_library all
    // respond immediately with {queued: true} and do the real work in a
    // background goroutine — the real counts arrive later via a
    // 'library.*'/'cache.*' event (see onMount below), not on this response.
    // Reading result fields here was always undefined.
    { id: 'fill_missing_episodes',    label: 'Fill Missing Episodes',    description: 'Use TMDB episode lists to create library items for episodes not yet tracked. Also runs automatically every 6 h as part of Content Maintenance.',              group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.fillMissingEpisodes();                   return 'started in background'; } },
    { id: 'republish_pending',        label: 'Republish Pending',        description: 'Republish library items with a selected release but no current symlink. Also runs automatically every 30 min as part of Publishing Maintenance.',                  group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.republishPendingLibrary();               return 'started in background'; } },
    { id: 'reset_orphaned_available', label: 'Reset Orphaned Available', description: 'Reset available items with no symlink back to pending for re-search. Also runs automatically every 30 min as part of Publishing Maintenance.',                     group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.resetOrphanedAvailableItems();           return 'started in background'; } },
    { id: 'cache_prune',              label: 'Prune Block Cache',        description: 'Delete oldest decoded articles from the disk cache. Also runs automatically every 6 h as part of Storage Maintenance.',                                      group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.pruneCache();                            return 'started in background'; } },
    // backfill_metadata used to be the one genuinely-manual-only action
    // with no automated counterpart at all (confirmed via app.go audit,
    // 2026-08-12) -- fixed by adding a weekly metadata_backfill task, so
    // this is now the same "manual override alongside automation" pattern
    // as everything else in this group.
    { id: 'backfill_metadata',        label: 'Backfill Metadata',        description: 'Re-enrich movies and TV shows with new TMDB fields. Also runs automatically once a week.',                                      group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.backfillMetadata();                      return 'started in background'; } },
    // Deliberately NOT automated: unlike everything else here, this has a
    // real external side effect (creates new requests in Seerr) every time
    // it runs, rather than just refreshing/repairing local state -- worth
    // keeping under manual control.
    { id: 'seerr_push_library',       label: 'Push Library to Seerr',    description: 'Push library items missing from Seerr as new requests. Manual only -- creates real external Seerr requests, not automated.',                                  group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.pushMissingToSeerr();                    return 'started in background'; } },
  ];

  async function loadTaskSchedules() {
    try { taskSchedules = (await api.taskSchedules()).items ?? []; } catch { /* ignore */ }
    finally { taskSchedulesLoading = false; }
  }

  async function runTask(task: TaskDef) {
    taskRunning = { ...taskRunning, [task.id]: true };
    const ranAt = new Date().toISOString();
    try {
      const detail = String(await task.run());
      taskResults = { ...taskResults, [task.id]: { ok: true, detail, ranAt } };
      toastSuccess(`${task.label}: ${detail}`);
      await loadTaskSchedules();
    } catch (err) {
      const detail = err instanceof Error ? err.message : String(err);
      taskResults = { ...taskResults, [task.id]: { ok: false, detail, ranAt } };
      toastError(`${task.label} failed: ${detail}`);
    } finally {
      taskRunning = { ...taskRunning, [task.id]: false };
    }
  }

  function taskScheduleFor(task: TaskDef) { return taskSchedules.find((s) => s.id === task.id); }
  function fmtTaskTime(iso: string) { return new Date(iso).toLocaleString('en-GB', { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' }); }
  $: taskGroups = [...new Set(taskDefs.map((t) => t.group))];
  $: taskRunningCount = Object.values(taskRunning).filter(Boolean).length;

  // ── Quality Profiles tab state ──────────────────────────────────────────────
  const ALL_RESOLUTIONS = ['2160p', '1080p', '720p', '576p', '480p'];
  const ALL_SOURCES     = ['BluRay', 'Remux', 'WEB-DL', 'WEBRip', 'HDTV', 'DVDRip'];
  const ALL_CODECS      = ['x265', 'HEVC', 'x264', 'AVC', 'AV1', 'VP9'];
  const ALL_LANGUAGES   = ['nl', 'en', 'de', 'fr', 'es', 'pt', 'it', 'ja', 'ko', 'zh', 'multi'];
  const ALL_AUDIO       = ['Atmos', 'TrueHD', 'DTS-HD', 'DTS', 'DD+', 'AC3', 'AAC', 'FLAC', 'MP3'];
  const ALL_HDR         = ['DV', 'HDR10+', 'HDR10', 'HLG', 'SDR'];

  let selectedProfile: QualityProfile | null = null;
  let profileSaving = false;

  // ── Indexer Policies tab state ──────────────────────────────────────────────
  let indexerPolicies: IndexerPolicy[] = [];
  let editingPolicy: IndexerPolicy | null = null;
  let ipSaving = false;

  async function loadIndexerPolicies() {
    try {
      const res = await api.listIndexerPolicies();
      indexerPolicies = res.items ?? [];
    } catch { /* ignore */ }
  }

  async function saveIndexerPolicy() {
    if (!editingPolicy) return;
    ipSaving = true;
    try {
      let saved: IndexerPolicy;
      if (editingPolicy.id) {
        saved = await api.updateIndexerPolicy(editingPolicy);
        indexerPolicies = indexerPolicies.map(p => p.id === saved.id ? saved : p);
      } else {
        saved = await api.upsertIndexerPolicy(editingPolicy);
        const existing = indexerPolicies.findIndex(p => p.indexerName === saved.indexerName);
        if (existing >= 0) {
          indexerPolicies = indexerPolicies.map(p => p.indexerName === saved.indexerName ? saved : p);
        } else {
          indexerPolicies = [...indexerPolicies, saved];
        }
      }
      editingPolicy = null;
      toastSuccess('Saved');
    } catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
    finally { ipSaving = false; }
  }

  async function deleteIndexerPolicy(id: number) {
    if (!confirmed('Delete this indexer policy?')) return;
    await runAction(() => api.deleteIndexerPolicy(id), {
      setWorking: () => {},
      successMessage: () => 'Deleted',
      afterSuccess: () => {
        indexerPolicies = indexerPolicies.filter(p => p.id !== id);
        editingPolicy = null;
      }
    });
  }

  // ── Speed Test tab ───────────────────────────────────────────────────────────
  async function runSpeedTest() {
    await runAction(() => api.runSpeedTest(), {
      setWorking: (w) => setBusy('speedtest', w),
      successMessage: (r) => `${r.throughputMbps.toFixed(0)} Mbps`,
      afterSuccess: (r) => {
        speedTestResults = [r, ...speedTestResults].slice(0, 10);
      }
    });
  }

  // ── Subtitle Profiles tab state ─────────────────────────────────────────────
  let subtitleProfiles: SubtitleProfile[] = [];
  let editingSubtitleProfile: SubtitleProfile | null = null;
  let spSaving = false;

  async function loadSubtitleProfiles() {
    try {
      const res = await api.listSubtitleProfiles();
      subtitleProfiles = res.items ?? [];
    } catch { /* ignore */ }
  }

  async function saveSubtitleProfile() {
    if (!editingSubtitleProfile) return;
    spSaving = true;
    try {
      let saved: SubtitleProfile;
      if (editingSubtitleProfile.id) {
        saved = await api.updateSubtitleProfile(editingSubtitleProfile);
        subtitleProfiles = subtitleProfiles.map(p => p.id === saved.id ? saved : p);
      } else {
        saved = await api.createSubtitleProfile(editingSubtitleProfile);
        subtitleProfiles = [...subtitleProfiles, saved];
      }
      editingSubtitleProfile = null;
      toastSuccess('Saved');
    } catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
    finally { spSaving = false; }
  }

  async function deleteSubtitleProfile(id: number) {
    if (!confirmed('Delete this subtitle profile?')) return;
    await runAction(() => api.deleteSubtitleProfile(id), {
      setWorking: () => {},
      successMessage: () => 'Deleted',
      afterSuccess: () => {
        subtitleProfiles = subtitleProfiles.filter(p => p.id !== id);
        editingSubtitleProfile = null;
      }
    });
  }

  // ── Custom Formats tab state ────────────────────────────────────────────────
  let customFormats: CustomFormat[] = [];
  let cfSaving = false;
  let cfImportOpen = false;
  let cfImportJson = '';
  let cfImporting = false;

  function blankFormat(): CustomFormat {
    return { name: '', pattern: '', score: 0, enabled: true, source: 'custom' };
  }

  let editingFormat: CustomFormat | null = null;

  async function importCustomFormats() {
    cfImporting = true;
    try {
      const parsed = JSON.parse(cfImportJson);
      const formats: CustomFormat[] = Array.isArray(parsed) ? parsed : [parsed];
      const result = await api.importCustomFormats(formats);
      toastSuccess(`Imported ${result.imported} of ${result.total} custom formats`);
      cfImportOpen = false;
      cfImportJson = '';
      await loadCustomFormats();
    } catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
    finally { cfImporting = false; }
  }

  async function loadCustomFormats() {
    try {
      const res = await api.listCustomFormats();
      customFormats = res.items ?? [];
    } catch { /* ignore */ }
  }

  // ── Release block rules ──────────────────────────────────────────────────
  let blockRules: ReleaseBlockRule[] = [];
  let bfSaving = false;
  let editingRule: ReleaseBlockRule | null = null;
  let testTitle = '';
  let testMediaType: 'movie' | 'tv' | 'both' = 'both';
  let testResult: BlockTestResult | null = null;
  let testRunning = false;

  function blankRule(): ReleaseBlockRule {
    return { type: 'release_group', pattern: '', mediaType: 'both', action: 'block', scorePenalty: 0, enabled: true, source: 'custom', note: '' };
  }

  async function loadBlockRules() {
    try {
      const res = await api.listReleaseBlockRules();
      blockRules = res.items ?? [];
    } catch { /* ignore */ }
  }

  async function saveBlockRule() {
    if (!editingRule) return;
    bfSaving = true;
    try {
      let saved: ReleaseBlockRule;
      if (editingRule.id) {
        saved = await api.updateReleaseBlockRule(editingRule);
        blockRules = blockRules.map(r => r.id === saved.id ? saved : r);
      } else {
        saved = await api.createReleaseBlockRule(editingRule);
        blockRules = [...blockRules, saved];
      }
      editingRule = null;
      toastSuccess('Saved');
    } catch (err) { toastError(err instanceof Error ? err.message : String(err)); }
    finally { bfSaving = false; }
  }

  async function deleteBlockRule(id: number) {
    if (!confirmed('Delete this block rule?')) return;
    await runAction(() => api.deleteReleaseBlockRule(id), {
      setWorking: () => {},
      successMessage: () => 'Deleted',
      afterSuccess: () => {
        blockRules = blockRules.filter(r => r.id !== id);
        editingRule = null;
      }
    });
  }

  async function runBlockTest() {
    if (!testTitle.trim()) return;
    testRunning = true;
    testResult = null;
    try {
      testResult = await api.testReleaseBlockRule(testTitle.trim(), testMediaType);
    } catch (err) { toastError(err instanceof Error ? err.message : String(err)); }
    finally { testRunning = false; }
  }

  $: blockRuleGroups = {
    release_group: blockRules.filter(r => r.type === 'release_group'),
    title_pattern: blockRules.filter(r => r.type === 'title_pattern'),
    regex: blockRules.filter(r => r.type === 'regex'),
    missing_release_group: blockRules.filter(r => r.type === 'missing_release_group'),
  };

  async function saveFormat() {
    if (!editingFormat) return;
    cfSaving = true;
    try {
      if (editingFormat.id) {
        const updated = await api.updateCustomFormat(editingFormat);
        customFormats = customFormats.map(f => f.id === updated.id ? updated : f);
      } else {
        const created = await api.createCustomFormat(editingFormat);
        customFormats = [...customFormats, created];
      }
      editingFormat = null;
      toastSuccess('Custom format saved');
    } catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
    finally { cfSaving = false; }
  }

  async function deleteFormat(id: number) {
    if (!confirmed('Delete this custom format?')) return;
    await runAction(() => api.deleteCustomFormat(id), {
      setWorking: () => {},
      successMessage: () => 'Custom format deleted',
      afterSuccess: () => {
        customFormats = customFormats.filter(f => f.id !== id);
        if (editingFormat?.id === id) editingFormat = null;
      }
    });
  }

  function blankProfile(): QualityProfile {
    return { name: 'New Profile', isDefault: false, resolutions: ['1080p', '2160p', '720p'], sources: ['WEB-DL', 'BluRay', 'WEBRip'], codecs: ['x265', 'x264'], languages: ['nl', 'en'], audioFormats: ['TrueHD', 'DTS-HD', 'DTS', 'DD+', 'AC3', 'AAC'], hdrFormats: ['HDR10', 'SDR'], excludePatterns: [], preferProper: true, preferRepack: true, rejectCam: true, allowUpgrade: false, minimumUpgradeCustomFormatScore: 0, cutoffResolution: '', minimumAgeHours: 0, minMbPerMinute: 0, maxMbPerMinute: 0 };
  }

  async function saveSelectedProfile() {
    if (!selectedProfile) return;
    profileSaving = true;
    try {
      const saved = await api.saveProfile(selectedProfile);
      toastSuccess(`Profile "${saved.name}" saved`);
      const pr = await api.listProfiles();
      profiles = pr.profiles ?? [];
      const found = profiles.find(p => p.name === saved.name);
      if (found) selectedProfile = { ...found };
    } catch (err) { toastError(err instanceof Error ? err.message : String(err)); }
    finally { profileSaving = false; }
  }

  async function deleteSelectedProfile(p: QualityProfile) {
    if (!p.id || p.isDefault) return;
    if (!confirmed(`Delete profile "${p.name}"?`)) return;
    await runAction(() => api.deleteProfile(p.id!), {
      setWorking: () => {},
      successMessage: () => `Profile "${p.name}" deleted`,
      afterSuccess: async () => {
        if (selectedProfile?.id === p.id) selectedProfile = null;
        const pr = await api.listProfiles();
        profiles = pr.profiles ?? [];
      }
    });
  }

  function profileMoveUp(arr: string[], i: number): string[] { if (i === 0) return arr; const n = [...arr]; [n[i-1], n[i]] = [n[i], n[i-1]]; return n; }
  function profileMoveDown(arr: string[], i: number): string[] { if (i >= arr.length - 1) return arr; const n = [...arr]; [n[i], n[i+1]] = [n[i+1], n[i]]; return n; }
  function profileToggle(arr: string[], v: string): string[] { return arr.includes(v) ? arr.filter(x => x !== v) : [...arr, v]; }

  // ── Plex OAuth state ────────────────────────────────────────────────────────
  type PlexPin = { pinId: number; code: string; authUrl: string };
  let plexPin: PlexPin | null = null;
  let plexPollInterval: number | undefined;

  async function startPlexOAuth() {
    try {
      const pin = await api.plexOauthStart();
      plexPin = pin;
      window.open(pin.authUrl, '_blank', 'noopener,noreferrer');
      window.clearInterval(plexPollInterval);
      plexPollInterval = window.setInterval(async () => {
        if (!plexPin) { window.clearInterval(plexPollInterval); return; }
        try {
          const result = await api.plexOauthPoll(plexPin.pinId);
          if (result.authorized && result.token) {
            window.clearInterval(plexPollInterval);
            plexPin = null;
            if (draft) { draft.plex.token = result.token; }
            toastSuccess('Plex token retrieved — save to apply');
          }
        } catch { /* retry next tick */ }
      }, 3000);
    } catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
  }

  function cancelPlexOAuth() {
    window.clearInterval(plexPollInterval);
    plexPin = null;
  }

  const queueDecisionRows = [
    // ── Drakkar-native failures ───────────────────────────────────────────────
    ['allCandidatesWrongTitle',     'All candidates matched search but had wrong titles'],
    ['allCandidatesRejected',       'All candidates were rejected (bad source, size, quality)'],
    ['noReleaseFound',              'No NZBs returned by indexers'],
    ['preflightFailed',             'Archive inspection (preflight) failed'],
    ['nzbFetch4xx',                 'NZB fetch returned a permanent HTTP error (401/404/410/451)'],
    ['nzbFetch403',                 'NZB fetch returned 403 (quota or rate-limit)'],
    ['nzbFetchFailed',              'NZB fetch failed for a transient reason'],
    ['publishFailed',               'Publishing (FUSE/symlink) failed'],
    ['badSource',                   'Bad source detected (CAM, TS, etc.)'],
    ['interruptedByRestart',        'Download was interrupted by a process restart'],
    // ── Sonarr/Radarr-compatible failures ────────────────────────────────────
    ['grabbedSeriesIdMismatch',     'Found matching series via grab history, but release was matched to series by ID. Automatic import is not possible.'],
    ['grabbedMovieIdMismatch',      'Found matching movie via grab history, but release was matched to movie by ID. Manual import required.'],
    ['episodeMissingInRelease',     'Episode was not found in the grabbed release'],
    ['unexpectedEpisodes',          'Episode(s) were unexpected considering the folder name'],
    ['notEpisodeUpgrade',           'Not an upgrade for existing episode file(s)'],
    ['notMovieUpgrade',             'Not an upgrade for existing movie file'],
    ['notCustomFormatUpgrade',      'Not a Custom Format upgrade'],
    ['noEligibleFiles',             'No files found are eligible for import'],
    ['episodeAlreadyImported',      'Episode file already imported'],
    ['noAudioTracks',               'No audio tracks detected'],
    ['invalidSeasonEpisode',        'Invalid season or episode'],
    ['singleEpisodeContainsSeason', 'Single episode file contains all episodes in seasons'],
    ['unableToDetermineSample',     'Unable to determine if file is a sample'],
    ['sample',                      'Sample'],
    ['archiveNeedsExtraction',      'Found archive file, might need to be extracted'],
    ['missingArticles',             'Missing articles / expired Usenet parts']
  ] as const;

  const queueDecisionLabels: Record<string, string> = {
    do_nothing:              'Do Nothing',
    remove:                  'Remove',
    remove_and_blocklist:    'Remove and Blocklist',
    remove_blocklist_and_search: 'Remove, Blocklist, and Search',
    search_again:            'Search Again'
  };

  // ── Select-trigger label maps (shadcn Select has no built-in "show selected
  // option" behaviour like a native <select>, so the trigger label is looked
  // up here) ──────────────────────────────────────────────────────────────
  const duplicateNzbBehaviorLabels: Record<string, string> = { mark_failed: 'Mark Failed', ignore_existing: 'Ignore Existing', download_again_with_suffix: 'Download Again (with suffix)', replace_existing: 'Replace Existing' };
  const importStrategyLabels: Record<string, string> = { symlink: 'Symlink', strm: 'STRM', copy: 'Copy' };
  const blockEditorKeyTypeLabels: Record<string, string> = { external_url: 'External URL', release_signature: 'Release Signature', raw: 'Raw Key' };
  const rfTypeLabels: Record<string, string> = { release_group: 'Release Group', title_pattern: 'Title Pattern', regex: 'Regex', missing_release_group: 'Missing Release Group' };
  const rfMediaTypeLabels: Record<string, string> = { both: 'Both', movie: 'Movie only', tv: 'TV only' };
  const rfActionLabels: Record<string, string> = { block: 'Block (reject release)', penalty: 'Penalty (reduce score)' };
  const testMediaTypeLabels: Record<string, string> = { both: 'Both', movie: 'Movie', tv: 'TV' };
  const logLevelLabels: Record<string, string> = { all: 'All levels', info: 'Info', warn: 'Warn', error: 'Error', debug: 'Debug' };

  function readTabFromURL(): SettingsTab {
    if (typeof window === 'undefined') return 'integrations';
    const raw = new URL(window.location.href).searchParams.get('tab');
    return tabs.some((t) => t.id === raw) ? (raw as SettingsTab) : 'integrations';
  }

  function setActiveTab(tab: SettingsTab) {
    activeTab = tab;
    if (typeof window === 'undefined') return;
    const url = new URL(window.location.href);
    url.searchParams.set('tab', tab);
    window.history.replaceState({}, '', url);
    if (tab === 'logs' && logEntries.length === 0) void loadLogs();
  }

  function cloneSettings(s: FullSettings): FullSettings {
    return JSON.parse(JSON.stringify(s));
  }

  function emptyProvider(): UsenetProvider {
    return { name: '', host: '', port: 563, tls: true, username: '', password: '', maxConnections: 10, priority: 0, retentionDays: 0, backup: false, enabled: true };
  }

  async function loadAll() {
    loading = true;
    try {
      const [s, pr, qdRes, pol, fs] = await Promise.all([
        api.status(),
        api.listProfiles(),
        api.listQualityDefinitions(),
        api.policies(),
        api.getSettings()
      ]);
      status = s;
      profiles = pr.profiles;
      qualityDefs = qdRes.definitions ?? [];
      policySettings = pol;
      // loadAll() re-runs on every SSE message and 30s poll tick, not just on
      // an explicit refresh click. Overwriting `draft` unconditionally here
      // used to silently discard whatever the user was mid-editing in the
      // settings form the moment an unrelated background event fired (e.g.
      // a cache prune or someone else's library search completing). Only
      // replace `draft` when there's nothing unsaved to lose.
      const hasUnsavedEdits = !!draft && !!fullSettings && JSON.stringify(draft) !== JSON.stringify(fullSettings);
      fullSettings = fs;
      if (!hasUnsavedEdits) {
        draft = cloneSettings(fs);
        // Apply frontend defaults for fields that may be absent from older settings.json
        if (draft && !draft.indexer) {
          draft.indexer = { tvRssSyncIntervalMinutes: 15, movieRssSyncIntervalMinutes: 30, minimumAgeMinutes: 0, retentionDays: 0, maximumSizeMB: 0, searchDelayMs: 2000, backgroundSearchWorkers: 12, releaseGraceHours: 12 };
        } else if (draft?.indexer) {
          if (!draft.indexer.backgroundSearchWorkers) {
            draft.indexer.backgroundSearchWorkers = 12;
          }
          if (draft.indexer.releaseGraceHours === undefined) {
            draft.indexer.releaseGraceHours = 12;
          }
        }
        if (draft && !draft.jellyfin) {
          draft.jellyfin = { url: '', apiKey: '' };
        }
        if (draft && !draft.notifications) {
          draft.notifications = { discordWebhookUrl: '', genericWebhookUrl: '', onGrab: false, onAvailable: true, onFailed: false };
        }
        if (draft && !draft.privacy) {
          draft.privacy = {
            mode: 'direct',
            socks5: { host: '', port: 1080, username: '', password: '', timeoutSeconds: 15 },
            wireguard: { configText: '', timeoutSeconds: 15 },
            excludedIndexers: [],
            syncNzbHydra2Proxy: false
          };
        } else if (draft?.privacy) {
          if (!draft.privacy.excludedIndexers) {
            // Older/partial settings.json (or an omitempty gap server-side) can
            // omit this array entirely -- treated as undefined/null here would
            // throw on the .join() call in the Privacy Routing tab.
            draft.privacy.excludedIndexers = [];
          }
          if (draft.privacy.syncNzbHydra2Proxy === undefined) {
            draft.privacy.syncNzbHydra2Proxy = false;
          }
        }
      }
      try {
        privacyStatus = await api.getPrivacyStatus();
      } catch {
        // Non-fatal -- the status panel just shows nothing until reachable.
      }
      try {
        const res = await api.listIndexerNames();
        knownIndexerNames = res.names;
      } catch {
        // Non-fatal -- the exclusion-list picker falls back to manual entry.
      }
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      loading = false;
    }
  }

  async function saveQualityDef(d: QualityDefinition) {
    qualityDefsSaving = new Set([...qualityDefsSaving, d.id]);
    try {
      const updated = await api.updateQualityDefinition(d);
      qualityDefs = qualityDefs.map(x => x.id === updated.id ? updated : x);
      qualityDefsDirty.delete(d.id);
      qualityDefsDirty = new Set(qualityDefsDirty);
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      qualityDefsSaving.delete(d.id);
      qualityDefsSaving = new Set(qualityDefsSaving);
    }
  }

  async function saveSettings() {
    if (!draft) return;
    setBusy('save-settings', true);
    try {
      const saved = await api.saveSettings(draft);
      fullSettings = saved;
      draft = cloneSettings(saved);
      toastSuccess('Settings saved');
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy('save-settings', false);
    }
  }

  function addProvider() {
    if (!draft) return;
    draft.usenet.providers = [...draft.usenet.providers, emptyProvider()];
  }

  function removeProvider(i: number) {
    if (!draft) return;
    draft.usenet.providers = draft.usenet.providers.filter((_, idx) => idx !== i);
  }

  async function loadBlocklist() {
    blLoading = true;
    try {
      const [page, stats] = await Promise.all([
        api.blocklistPaged({ page: blPage, pageSize: blPageSize, q: blockQuery || undefined, reason: blockReasonFilter !== 'all' ? blockReasonFilter : undefined, sort: blockSortCol === 'expires' ? 'expiresAt' : blockSortCol === 'createdAt' ? 'createdAt' : blockSortCol, dir: blockSortDir }),
        api.blocklistStats()
      ]);
      blocklist = page.items ?? [];
      blTotal = page.total;
      blTotalPages = page.totalPages;
      blStats = stats;
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      blLoading = false;
    }
  }

  async function clearBlocklist(id: number) {
    if (!confirmed('Clear this runtime blocklist entry?')) return;
    setBusy(`clear-blocklist-${id}`, true);
    try {
      await api.clearBlocklist(id);
      toastSuccess('Blocklist item cleared');
      await loadBlocklist();
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(`clear-blocklist-${id}`, false);
    }
  }

  async function clearAllBlocklist() {
    if (!confirmed('Clear all active runtime blocklist entries?')) return;
    setBusy('clear-all-blocklist', true);
    try {
      const r = await api.clearAllBlocklist();
      toastSuccess(`Cleared ${r.cleared} blocklist entr${r.cleared === 1 ? 'y' : 'ies'}`);
      blPage = 1;
      await loadBlocklist();
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy('clear-all-blocklist', false);
    }
  }

  async function clearBlocklistByReason(reason: string) {
    if (!confirmed(`Clear all active runtime blocklist entries with reason "${reason}"?`)) return;
    setBusy(`clear-blocklist-reason-${reason}`, true);
    try {
      const r = await api.clearBlocklistByReason(reason);
      toastSuccess(`Cleared ${r.cleared} ${reason} entr${r.cleared === 1 ? 'y' : 'ies'}`);
      blPage = 1;
      await loadBlocklist();
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(`clear-blocklist-reason-${reason}`, false);
    }
  }

  async function copyBlocklistKey(key: string) {
    try {
      await copyToClipboard(key);
      toastSuccess('Blocklist key copied');
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    }
  }

  function resetBlockEditor() {
    blockEditor = blankBlocklistEditor();
  }

  function toDatetimeLocal(value?: string) {
    if (!value) return '';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '';
    return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  }

  function startEditBlocklist(item: BlocklistItem) {
    blockEditor = {
      id: item.id,
      keyType: 'raw',
      key: item.key,
      externalUrl: item.keyType === 'external_url' ? item.key.replace(/^external_url:/, '') : '',
      releaseTitle: item.releaseTitle || '',
      indexerName: item.indexerName || '',
      sizeMb: item.sizeBytes ? Math.round(item.sizeBytes / (1024 * 1024)) : 0,
      postedDate: item.postedAt ? item.postedAt.slice(0, 10) : '',
      reason: item.reason || 'manual',
      expiresAt: toDatetimeLocal(item.expiresAt)
    };
  }

  async function saveBlocklistEntry() {
    const payload: BlocklistMutation = {
      keyType: blockEditor.keyType,
      key: blockEditor.key.trim(),
      externalUrl: blockEditor.externalUrl.trim(),
      releaseTitle: blockEditor.releaseTitle.trim(),
      indexerName: blockEditor.indexerName.trim(),
      sizeMb: blockEditor.sizeMb,
      postedDate: blockEditor.postedDate.trim(),
      reason: blockEditor.reason.trim() || 'manual',
      expiresAt: blockEditor.expiresAt ? new Date(blockEditor.expiresAt).toISOString() : undefined
    };
    setBusy('save-blocklist-entry', true);
    try {
      if (blockEditor.id) {
        await api.updateBlocklist(blockEditor.id, payload);
        toastSuccess('Runtime blocklist entry updated');
      } else {
        await api.createManualBlocklist(payload);
        toastSuccess('Runtime blocklist entry created');
      }
      resetBlockEditor();
      blPage = 1;
      await loadBlocklist();
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy('save-blocklist-entry', false);
    }
  }

  function blocklistKeyLabel(item: BlocklistItem) {
    switch (item.keyType) {
      case 'external_url': return 'URL';
      case 'release_signature': return 'Signature';
      default: return 'Key';
    }
  }

  function blocklistContext(item: BlocklistItem) {
    const parts: string[] = [];
    if (item.releaseTitle) parts.push(item.releaseTitle);
    if (item.indexerName) parts.push(item.indexerName);
    if (item.sizeBytes) parts.push(bytes(item.sizeBytes));
    return parts.join(' • ');
  }

  // Orphaned-content / broken-symlink cleanup is now handled automatically by the
  // storage_maintenance scheduled task (every 6h). No individual API endpoints remain.

  async function pruneCache() {
    if (!confirmed('Prune the block cache now?')) return;
    // Backend responds immediately with {queued: true} and prunes in a
    // background goroutine — the real stats (previously expected directly
    // off this call, which made lastCachePrune/the toast always show
    // "undefined") arrive later via the 'cache.prune' event handled in
    // onMount below, which populates lastCachePrune itself.
    await runAction(() => api.pruneCache(), {
      setWorking: (v) => setBusy('prune-cache', v),
      successMessage: () => 'Cache prune queued — processing in background…'
    });
  }

  async function probeIntegrations() {
    setBusy('probe-integrations', true);
    try {
      lastProbe = await api.probeIntegrations();
      const ok = lastProbe.results.filter((r) => r.ok).length;
      toastSuccess(`Probes: ${ok}/${lastProbe.results.length} OK`);
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy('probe-integrations', false);
    }
  }

  async function savePolicies() {
    if (!policySettings) return;
    setBusy('save-policies', true);
    try {
      policySettings = await api.savePolicies(policySettings);
      toastSuccess('Queue policy saved');
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy('save-policies', false);
    }
  }

  onMount(() => {
    activeTab = readTabFromURL();
    void loadAll();
    void loadTaskSchedules();
    void loadCustomFormats();
    void loadBlockRules();
    void loadIndexerPolicies();
    void loadSubtitleProfiles();
    const backgroundTaskToasts: Record<string, (e: Record<string, unknown>) => string> = {
      'library.fill_missing_episodes': (e) => `Fill Missing Episodes complete: processed ${e.showsProcessed} shows, created ${e.itemsCreated} new items`,
      'cache.prune': (e) => `Prune Block Cache complete: deleted ${e.deletedFiles} files`,
      'library.backfill_metadata': (e) => `Backfill Metadata complete: enriched ${e.enriched ?? 0}, failed ${e.failed ?? 0}, skipped ${e.skipped ?? 0}`,
      'library.push_library': (e) => `Push Library to Seerr complete: movies ${e.moviesPushed}, shows ${e.showsPushed}`,
      'library.search_upgrades': (e) => `Search Quality Upgrades complete: checked ${e.checked}, upgraded ${e.upgraded}, failed ${e.failed}`,
      'library.search_pending': (e) => `Backlog Search complete: processed ${e.processed}, searched ${e.searched}, selected ${e.selected}, failed ${e.failed}`,
      'library.republish_pending': (e) => `Republish Pending complete: processed ${e.processed}, republished ${e.republished}, failed ${e.failed}`,
      'library.reset_orphaned': (e) => `Reset Orphaned Available complete: found ${e.found}, reset ${e.reset}, failed ${e.failed}`,
      'health.check': (e) => `Symlink Health Check complete: checked ${e.checked}, healthy ${e.healthy}`,
      'queue.retry_failed': (e) => `Retry Failed Queue complete: retried ${e.retried ?? 0}, failed ${e.failed ?? 0}`,
      'requests.sync': (e) => `Sync Seerr Requests complete: seen ${e.seen ?? 0}, created ${e.created ?? 0}`
    };
    // The Tasks tab's status pill/result line for fire-and-forget "Operations"
    // tasks was permanently frozen at the literal string "started in
    // background" (set the instant the queue-ack resolves) because nothing
    // ever wrote the real outcome back into taskResults once the background
    // job actually finished. Maps each completion event kind to the task id
    // it belongs to and a real detail string, mirrored from the toasts above.
    const backgroundTaskResultUpdates: Record<string, { taskId: string; detail: (e: Record<string, unknown>) => string }> = {
      'library.fill_missing_episodes': { taskId: 'fill_missing_episodes', detail: (e) => `processed ${e.showsProcessed} shows, created ${e.itemsCreated} items` },
      'cache.prune': { taskId: 'cache_prune', detail: (e) => `deleted ${e.deletedFiles} files` },
      'library.backfill_metadata': { taskId: 'backfill_metadata', detail: (e) => `enriched ${e.enriched ?? 0}, failed ${e.failed ?? 0}, skipped ${e.skipped ?? 0}` },
      'library.push_library': { taskId: 'seerr_push_library', detail: (e) => `movies ${e.moviesPushed}, shows ${e.showsPushed}` },
      'library.search_upgrades': { taskId: 'search_upgrades', detail: (e) => `checked ${e.checked}, upgraded ${e.upgraded}, failed ${e.failed}` },
      'library.search_pending': { taskId: 'backlog_search', detail: (e) => `processed ${e.processed}, searched ${e.searched}, selected ${e.selected}, failed ${e.failed}` },
      'library.republish_pending': { taskId: 'republish_pending', detail: (e) => `processed ${e.processed}, republished ${e.republished}, failed ${e.failed}` },
      'library.reset_orphaned': { taskId: 'reset_orphaned_available', detail: (e) => `found ${e.found}, reset ${e.reset}, failed ${e.failed}` },
      'health.check': { taskId: 'health_check', detail: (e) => `checked ${e.checked}, healthy ${e.healthy}` },
      'queue.retry_failed': { taskId: 'retry_failed_queue', detail: (e) => `retried ${e.retried ?? 0}, failed ${e.failed ?? 0}` },
      'requests.sync': { taskId: 'seerr_sync', detail: (e) => `seen ${e.seen ?? 0}, created ${e.created ?? 0}` }
    };
    const debouncedLoadAll = debounce(() => void loadAll(), 500);
    const unsub = subscribeEvents((event) => {
      const kind = event?.kind as string | undefined;
      if (kind === 'cache.prune') {
        lastCachePrune = event as unknown as typeof lastCachePrune;
      }
      if (kind && backgroundTaskToasts[kind]) {
        toastSuccess(backgroundTaskToasts[kind](event as Record<string, unknown>));
      }
      if (kind && backgroundTaskResultUpdates[kind]) {
        const { taskId, detail } = backgroundTaskResultUpdates[kind];
        taskResults = { ...taskResults, [taskId]: { ok: true, detail: detail(event as Record<string, unknown>), ranAt: new Date().toISOString() } };
      }
      if (!anyBusy()) debouncedLoadAll();
    });
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void loadAll();
    }, 30000);
    const taskTimer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void loadTaskSchedules();
    }, 30000);
    return () => {
      window.clearInterval(timer);
      window.clearInterval(taskTimer);
      window.clearInterval(plexPollInterval);
      unsub();
    };
  });

  $: integrationEntries = status ? Object.entries(status.integrations).filter(([n]) => n !== 'subtitleProviders') : [];
  $: subtitleProviderEntries = status ? Object.entries(status.integrations.subtitleProviders) : [];

  $: if (activeTab === 'rules' && !blLoading && blStats === null) { void loadBlocklist(); }
  $: filteredBlocklist = blocklist;


</script>

<svelte:head><title>Settings — Drakkar</title></svelte:head>

<PageHeader title="Settings" subtitle="Integrations, providers, queue policy, rules and system configuration.">
  <Button kind="secondary" on:click={loadAll} disabled={loading}>
    <RefreshCw size={16} />
    Refresh
  </Button>
  <Button kind="secondary" on:click={probeIntegrations} disabled={loading || isBusy('probe-integrations')}>
    <Wrench size={16} />
    Probe
  </Button>
</PageHeader>

<div class="settings-shell">
  <aside class="tab-rail">
    {#each tabs as tab}
      <button class="tab-btn" class:active={activeTab === tab.id} on:click={() => setActiveTab(tab.id)} type="button">
        <tab.icon size={15} />
        <span>{tab.label}</span>
      </button>
    {/each}
  </aside>

  <div class="tab-content">

    <!-- INTEGRATIONS -->
    {#if activeTab === 'integrations'}
      {#if draft}
        <div class="grid-2 integration-pair">
          <Panel title="NZBHydra2" subtitle="Newznab aggregator for NZB indexing.">
            <div class="form-grid">
              <label class="form-field">
                <span>URL</span>
                <input type="url" bind:value={draft.nzbhydra2.url} placeholder="http://nzbhydra2:5076" />
              </label>
              <label class="form-field">
                <span>API Key</span>
                <input type="password" bind:value={draft.nzbhydra2.apiKey} placeholder="••••••••" autocomplete="off" />
              </label>
              <label class="form-field">
                <span>Search Cache TTL (s)</span>
                <input type="number" bind:value={draft.nzbhydra2.searchCacheTtlSeconds} min="0" />
              </label>
              <label class="form-field">
                <span>Feed Cache TTL (s)</span>
                <input type="number" bind:value={draft.nzbhydra2.feedCacheTtlSeconds} min="0" />
              </label>
              <label class="form-field">
                <span>Feed Max Results</span>
                <input type="number" bind:value={draft.nzbhydra2.feedMaxResults} min="0" />
              </label>
            </div>
          </Panel>

          <Panel title="Seerr" subtitle="Request management.">
            <div class="form-grid">
              <label class="form-field">
                <span>URL</span>
                <input type="url" bind:value={draft.seerr.url} placeholder="http://seerr:5055" />
              </label>
              <label class="form-field">
                <span>API Key</span>
                <input type="password" bind:value={draft.seerr.apiKey} placeholder="••••••••" autocomplete="off" />
              </label>
            </div>

            <div class="webhook-setup">
              <div class="webhook-setup__header">
                <Webhook size={15} />
                <span>Webhook setup</span>
              </div>
              <p class="webhook-setup__desc">
                Configure a webhook in Seerr so Drakkar receives instant notifications when requests
                are approved — without waiting for the next 10-minute sync.
              </p>
              <ol class="webhook-setup__steps">
                <li>In Seerr, go to <strong>Settings → Notifications → Webhook</strong></li>
                <li>Enable the webhook and paste the URL below</li>
                <li>
                  Under <strong>Notification Types</strong>, enable at minimum:<br />
                  <code>Request Approved</code>, <code>Request Auto-Approved</code>
                </li>
                <li>Leave <strong>JSON Payload</strong> at its default (Seerr standard format)</li>
                <li>
                  <strong>Required:</strong> generate a Bearer token below and add it as an
                  <code>Authorization</code> header (<code>Bearer &lt;token&gt;</code>) in Seerr's
                  webhook settings — Drakkar now rejects any webhook call without a valid token
                </li>
                <li>Save and use <strong>Test</strong> to verify the connection</li>
              </ol>
              <div class="webhook-url-row">
                <code class="webhook-url">{webhookUrl}</code>
                <button class="copy-btn" on:click={copyWebhookUrl} title="Copy webhook URL">
                  {#if webhookCopied}
                    <Check size={14} />
                  {:else}
                    <Copy size={14} />
                  {/if}
                </button>
              </div>
              <div class="webhook-token-section">
                <div class="webhook-token-label">Bearer token (required)</div>
                {#if webhookToken}
                  <p class="webhook-token-hint">Copy this token now — it will not be shown again.</p>
                  <div class="webhook-url-row">
                    <code class="webhook-url">{webhookToken}</code>
                    <button class="copy-btn" on:click={copyWebhookToken} title="Copy token">
                      {#if webhookTokenCopied}<Check size={14} />{:else}<Copy size={14} />{/if}
                    </button>
                  </div>
                {:else}
                  <button class="copy-btn copy-btn--generate" on:click={createWebhookToken} disabled={webhookTokenCreating}>
                    {webhookTokenCreating ? 'Generating…' : 'Generate API Token'}
                  </button>
                {/if}
              </div>
            </div>
          </Panel>
        </div>

        <div class="grid-2 integration-pair">
          <Panel title="Metadata" subtitle="TMDB and TVDB API keys, language and cache settings.">
            <div class="form-grid">
              <label class="form-field">
                <span>TMDB API Key</span>
                <input type="password" bind:value={draft.metadata.tmdb.apiKey} placeholder="••••••••" autocomplete="off" />
              </label>
              <label class="form-field">
                <span>TVDB API Key</span>
                <input type="password" bind:value={draft.metadata.tvdb.apiKey} placeholder="••••••••" autocomplete="off" />
              </label>
              <label class="form-field">
                <span>Language</span>
                <input type="text" bind:value={draft.metadata.language} placeholder="en-US" />
              </label>
              <label class="form-field">
                <span>Cache TTL (hours)</span>
                <input type="number" bind:value={draft.metadata.cacheTtlHours} min="0" />
              </label>
            </div>
          </Panel>

          <Panel title="Subtitles" subtitle="Subtitle provider credentials and language preferences.">
            <div class="form-grid">
              <label class="form-field form-field--toggle">
                <span>Enabled</span>
                <input type="checkbox" bind:checked={draft.subtitles.enabled} />
              </label>
              <label class="form-field">
                <span>Languages (comma-separated)</span>
                <input
                  type="text"
                  value={draft.subtitles.languages.join(', ')}
                  on:change={(e) => {
                    if (!draft) return;
                    draft.subtitles.languages = (e.currentTarget as HTMLInputElement).value
                      .split(',').map(l => l.trim()).filter(Boolean);
                  }}
                  placeholder="en, nl"
                />
              </label>
            </div>
            {#each Object.entries(draft.subtitles.providers ?? {}) as [name, p]}
              <div class="sub-provider">
                <div class="sub-provider-head">
                  <strong>{name}</strong>
                  <label class="toggle-label">
                    <input type="checkbox" bind:checked={draft.subtitles.providers[name].enabled} />
                    <span>enabled</span>
                  </label>
                </div>
                <div class="form-grid form-grid--compact">
                  <label class="form-field">
                    <span>API Key</span>
                    <input type="password" bind:value={draft.subtitles.providers[name].apiKey} placeholder="••••••••" autocomplete="off" />
                  </label>
                  {#if name !== 'subdl'}
                  <label class="form-field">
                    <span>Username</span>
                    <input type="text" bind:value={draft.subtitles.providers[name].username} />
                  </label>
                  <label class="form-field">
                    <span>Password</span>
                    <input type="password" bind:value={draft.subtitles.providers[name].password} placeholder="••••••••" autocomplete="off" />
                  </label>
                  {/if}
                </div>
              </div>
            {/each}
          </Panel>
        </div>

        <div class="grid-2 integration-pair">
          <Panel title="SABnzbd API" subtitle="Optional download-client access for Sonarr and Radarr.">
            <div class="sab-api-head">
              <strong>SABnzbd-compatible endpoint</strong>
              <label class="toggle-label">
                <input id="sabnzbd-enabled" type="checkbox" disabled />
                <span>disabled</span>
              </label>
            </div>
            <div class="webhook-setup">
              <div class="webhook-setup__header">
                <Settings2 size={15} />
                <span>Sonarr / Radarr setup (disabled)</span>
              </div>
              <p class="webhook-setup__desc">
                SABnzbd access remains unavailable until Servarr ownership and zero-copy import behavior are finalized.
                Setup values below are retained for later use.
              </p>
              <ol class="webhook-setup__steps">
                <li>The endpoint cannot be enabled in this release</li>
                <li>In Sonarr or Radarr, go to <strong>Settings → Download Clients</strong> and add <strong>SABnzbd</strong></li>
                <li>Copy <strong>Host</strong> and <strong>Port</strong> below; enable SSL when Drakkar uses HTTPS</li>
                <li>Enable <strong>Advanced</strong> to reveal <strong>URL Base</strong>, then copy the value below</li>
                <li>Use category <code>tv</code> for Sonarr or <code>movies</code> for Radarr</li>
                <li>Token generation and client access remain locked while this integration is disabled</li>
                <li>Under <strong>Settings → Media Management → Root Folders</strong>, add the matching library path below</li>
                <li>For same-host Docker Compose, add the volume entry below to both services and recreate their containers</li>
              </ol>
              <div class="sab-copy-fields">
                <div class="sab-copy-field">
                  <label class="webhook-token-label" for="sab-host">Host</label>
                  <div class="webhook-url-row">
                    <input id="sab-host" class="webhook-url sab-copy-input" type="text" value={sabHost} readonly />
                    <button class="copy-btn" on:click={() => copySABField('host', sabHost)} title="Copy SABnzbd host">
                      {#if sabCopiedField === 'host'}<Check size={14} />{:else}<Copy size={14} />{/if}
                    </button>
                  </div>
                </div>
                <div class="sab-copy-field">
                  <label class="webhook-token-label" for="sab-port">Port</label>
                  <div class="webhook-url-row">
                    <input id="sab-port" class="webhook-url sab-copy-input" type="text" value={sabPort} readonly />
                    <button class="copy-btn" on:click={() => copySABField('port', sabPort)} title="Copy SABnzbd port">
                      {#if sabCopiedField === 'port'}<Check size={14} />{:else}<Copy size={14} />{/if}
                    </button>
                  </div>
                </div>
                <div class="sab-copy-field">
                  <label class="webhook-token-label" for="sab-url-base">URL Base</label>
                  <div class="webhook-url-row">
                    <input id="sab-url-base" class="webhook-url sab-copy-input" type="text" value={sabURLBase} readonly />
                    <button class="copy-btn" on:click={() => copySABField('url-base', sabURLBase)} title="Copy SABnzbd URL Base">
                      {#if sabCopiedField === 'url-base'}<Check size={14} />{:else}<Copy size={14} />{/if}
                    </button>
                  </div>
                </div>
              </div>
              <div class="webhook-token-section">
                <div class="webhook-token-label">API token (required)</div>
                <button class="copy-btn copy-btn--generate" disabled title="SABnzbd API is unavailable">
                  Generate API Token
                </button>
              </div>
              <div class="webhook-token-section">
                <div class="webhook-token-label">Media Manager Root Folders</div>
                <p class="webhook-setup__desc">
                  Root Folders are library destinations, not SABnzbd completed-download paths. Add the matching path in
                  <strong>Settings → Media Management → Root Folders</strong>.
                </p>
                <div class="sab-copy-fields">
                  <div class="sab-copy-field">
                    <label class="webhook-token-label" for="sab-radarr-root">Radarr Root Folder</label>
                    <div class="webhook-url-row">
                      <input id="sab-radarr-root" class="webhook-url sab-copy-input" type="text" value={sabRadarrRootFolder} readonly />
                      <button class="copy-btn" on:click={() => copySABField('radarr-root', sabRadarrRootFolder)} title="Copy Radarr root folder">
                        {#if sabCopiedField === 'radarr-root'}<Check size={14} />{:else}<Copy size={14} />{/if}
                      </button>
                    </div>
                  </div>
                  <div class="sab-copy-field">
                    <label class="webhook-token-label" for="sab-sonarr-root">Sonarr Root Folder</label>
                    <div class="webhook-url-row">
                      <input id="sab-sonarr-root" class="webhook-url sab-copy-input" type="text" value={sabSonarrRootFolder} readonly />
                      <button class="copy-btn" on:click={() => copySABField('sonarr-root', sabSonarrRootFolder)} title="Copy Sonarr root folder">
                        {#if sabCopiedField === 'sonarr-root'}<Check size={14} />{:else}<Copy size={14} />{/if}
                      </button>
                    </div>
                  </div>
                </div>
              </div>
              <div class="webhook-token-section">
                <div class="webhook-token-label">Docker Compose Volume</div>
                <p class="webhook-setup__desc">
                  On the same Docker host, add this entry under <code>volumes:</code> for both Sonarr and Radarr.
                  Keeping the same container path exposes the library folders and absolute FUSE symlink targets.
                </p>
                <div class="sab-copy-field">
                  <label class="webhook-token-label" for="sab-docker-volume">Volume entry (both services)</label>
                  <div class="webhook-url-row">
                    <input id="sab-docker-volume" class="webhook-url sab-copy-input" type="text" value={sabDockerVolume} readonly />
                    <button class="copy-btn" on:click={() => copySABField('docker-volume', sabDockerVolume)} title="Copy Docker Compose volume">
                      {#if sabCopiedField === 'docker-volume'}<Check size={14} />{:else}<Copy size={14} />{/if}
                    </button>
                  </div>
                </div>
                <p class="sab-path-note">
                  <code>rslave</code> lets FUSE mount changes propagate into each container without propagating container mounts back to the host.
                  Ensure the Sonarr/Radarr user can write its media folder and traverse the FUSE path.
                </p>
              </div>
            </div>
          </Panel>

          <Panel title="Default Quality Profiles" subtitle="Fallback profiles used when Seerr doesn't specify one.">
            <div class="form-grid">
              <label class="form-field">
                <span>Default Movie Profile</span>
                <Select.Root type="single" bind:value={draft.library.defaultMovieProfile}>
                  <Select.Trigger class="w-full">{profileOptionLabel(draft.library.defaultMovieProfile)}</Select.Trigger>
                  <Select.Content>
                    <Select.Item value="">— none —</Select.Item>
                    {#each profiles as p}
                      <Select.Item value={p.name}>{p.name}{p.isDefault ? ' (default)' : ''}</Select.Item>
                    {/each}
                  </Select.Content>
                </Select.Root>
              </label>
              <label class="form-field">
                <span>Default TV Profile</span>
                <Select.Root type="single" bind:value={draft.library.defaultTvProfile}>
                  <Select.Trigger class="w-full">{profileOptionLabel(draft.library.defaultTvProfile)}</Select.Trigger>
                  <Select.Content>
                    <Select.Item value="">— none —</Select.Item>
                    {#each profiles as p}
                      <Select.Item value={p.name}>{p.name}{p.isDefault ? ' (default)' : ''}</Select.Item>
                    {/each}
                  </Select.Content>
                </Select.Root>
              </label>
            </div>
          </Panel>
        </div>

        <div class="actions-row">
          <Button kind="primary" on:click={saveSettings} disabled={isBusy('save-settings')}>
            <Save size={16} />
            Save Integrations
          </Button>
        </div>
      {:else}
        <div class="empty">Loading settings…</div>
      {/if}

      <Panel title="Integration Probes" subtitle="Live reachability and auth checks. Click Probe above to run.">
        {#if lastProbe && lastProbe.results.length > 0}
          <div class="int-list">
            {#each lastProbe.results as item}
              <div class="int-row">
                <div class="int-info">
                  <strong>{item.name}</strong>
                  <span>{item.detail}</span>
                </div>
                <StatusPill tone={item.ok ? 'ok' : 'danger'}>
                  {item.ok ? `${item.durationMs} ms` : 'failed'}
                </StatusPill>
              </div>
            {/each}
          </div>
        {:else}
          <div class="empty">No probe results yet. Click Probe above.</div>
        {/if}
      </Panel>

    <!-- PROVIDERS -->
    {:else if activeTab === 'providers'}
      {#if draft}
        <Panel title="Connection Budget" subtitle="Global NNTP connection limits. Applies immediately after Save.">
          <div class="form-grid form-grid--3col">
            <label class="form-field">
              <span>Max Download Connections</span>
              <input type="number" bind:value={draft.usenet.maxDownloadConnections} min="1" max="500" />
            </label>
            <label class="form-field">
              <span>Streaming Priority %</span>
              <input type="number" bind:value={draft.usenet.streamingPriorityPercent} min="0" max="100" />
              <small class="field-hint">Share of Max Download Connections given to the read-ahead (prefetch) lane. The rest goes to the interactive playback-read lane, which always has its own guaranteed workers and is never blocked by prefetch. Applies immediately.</small>
            </label>
            <label class="form-field">
              <span>Article Buffer Size</span>
              <input type="number" bind:value={draft.usenet.articleBufferSize} min="1" max="500" />
            </label>
          </div>
        </Panel>

        <Panel title="Usenet Providers" subtitle="NNTP server credentials and per-provider connection pools.">
          <div class="provider-forms">
            {#each draft.usenet.providers as p, i}
              <div class="provider-edit-card">
                <div class="provider-edit-head">
                  <div class="pec-title">
                    <strong>{p.name || `Provider ${i + 1}`}</strong>
                    <StatusPill tone={p.enabled ? 'ok' : 'neutral'}>{p.enabled ? 'enabled' : 'disabled'}</StatusPill>
                  </div>
                  <button class="icon-btn danger" on:click={() => removeProvider(i)} title="Remove provider">
                    <X size={14} />
                  </button>
                </div>
                <div class="form-grid form-grid--2col">
                  <label class="form-field">
                    <span>Name</span>
                    <input type="text" bind:value={p.name} placeholder="Newshosting" />
                  </label>
                  <label class="form-field">
                    <span>Host</span>
                    <input type="text" bind:value={p.host} placeholder="news.example.com" />
                  </label>
                  <label class="form-field">
                    <span>Port</span>
                    <input type="number" bind:value={p.port} min="1" max="65535" />
                  </label>
                  <label class="form-field">
                    <span>Max Connections</span>
                    <input type="number" bind:value={p.maxConnections} min="1" max="500" />
                  </label>
                  <label class="form-field">
                    <span>Priority <small class="field-hint-inline">(lower = higher priority)</small></span>
                    <input type="number" bind:value={p.priority} min="0" />
                  </label>
                  <label class="form-field">
                    <span>Retention (days) <small class="field-hint-inline">(0 = unlimited)</small></span>
                    <input type="number" bind:value={p.retentionDays} min="0" />
                  </label>
                  <label class="form-field">
                    <span>Username</span>
                    <input type="text" bind:value={p.username} autocomplete="off" />
                  </label>
                  <label class="form-field">
                    <span>Password</span>
                    <input type="password" bind:value={p.password} placeholder="••••••••" autocomplete="off" />
                  </label>
                </div>
                <div class="provider-edit-footer">
                  <label class="toggle-label">
                    <input type="checkbox" bind:checked={p.tls} />
                    <span>TLS</span>
                  </label>
                  <label class="toggle-label">
                    <input type="checkbox" bind:checked={p.backup} />
                    <span>Backup server</span>
                  </label>
                  <label class="toggle-label">
                    <input type="checkbox" bind:checked={p.enabled} />
                    <span>Enabled</span>
                  </label>
                </div>
              </div>
            {/each}
          </div>
          <button class="add-btn" on:click={addProvider}>
            <Plus size={15} />
            Add Provider
          </button>
        </Panel>

        <div class="actions-row">
          <Button kind="primary" on:click={saveSettings} disabled={isBusy('save-settings')}>
            <Save size={16} />
            Save Providers
          </Button>
        </div>
      {:else}
        <div class="empty">Loading settings…</div>
      {/if}

    <!-- INDEXERS -->
    {:else if activeTab === 'indexers'}
      {#if draft}
        <Panel title="Indexer Settings" subtitle="Mirrors Sonarr/Radarr Settings → Indexers. Controls how Drakkar searches NZBHydra2.">
          <div class="form-grid form-grid--2col">
            <label class="form-field">
              <span>TV RSS Sync Interval (minutes)</span>
              <input type="number" min="15" max="120" bind:value={draft.indexer.tvRssSyncIntervalMinutes} />
              <small class="field-hint">How often to poll for new TV/episode releases. Minimum 15 min (Sonarr default). Applies immediately.</small>
            </label>
            <label class="form-field">
              <span>Movie RSS Sync Interval (minutes)</span>
              <input type="number" min="30" max="120" bind:value={draft.indexer.movieRssSyncIntervalMinutes} />
              <small class="field-hint">How often to poll for new movie releases. Minimum 30 min (Radarr default). Applies immediately.</small>
            </label>
            <label class="form-field">
              <span>Minimum Age (minutes)</span>
              <input type="number" min="0" bind:value={draft.indexer.minimumAgeMinutes} />
              <small class="field-hint">Don't grab a release posted less than this many minutes ago. Gives Usenet time to propagate. Sonarr/Radarr default: 0. Applies immediately.</small>
            </label>
            <label class="form-field">
              <span>Retention (days)</span>
              <input type="number" min="0" bind:value={draft.indexer.retentionDays} />
              <small class="field-hint">Skip releases older than this. Set to match your Usenet provider's retention. 0 = unlimited. Applies immediately.</small>
            </label>
            <label class="form-field">
              <span>Maximum Size (MB)</span>
              <input type="number" min="0" bind:value={draft.indexer.maximumSizeMB} />
              <small class="field-hint">Reject releases larger than this. 0 = no limit. Sonarr/Radarr default: 0. Applies immediately.</small>
            </label>
            <label class="form-field">
              <span>Search Delay (ms)</span>
              <input type="number" min="0" bind:value={draft.indexer.searchDelayMs} />
              <small class="field-hint">Minimum delay between consecutive NZBHydra2 API calls. 0 = no throttle (Sonarr/Radarr behaviour — NZBHydra2 handles per-indexer rate limiting). Applies immediately.</small>
            </label>
            <label class="form-field">
              <span>Background Search Workers</span>
              <input type="number" min="1" bind:value={draft.indexer.backgroundSearchWorkers} />
              <small class="field-hint">Concurrent BullMQ workers used for missing-item and backlog searches. Higher values drain big queues faster but increase Hydra/indexer load. Applies immediately.</small>
            </label>
            <label class="form-field">
              <span>Release Grace Period (hours)</span>
              <input type="number" min="0" bind:value={draft.indexer.releaseGraceHours} />
              <small class="field-hint">Don't search for a movie/episode until this many hours after its release/air date — a release posts at a specific time, not literally at midnight the moment the calendar date flips. 0 = search the instant the release day starts. Default: 12. Applies immediately.</small>
            </label>
          </div>
        </Panel>
        <div class="settings-actions">
          <Button kind="primary" on:click={saveSettings} disabled={isBusy('save-settings')}>
            <Save size={14} /> {isBusy('save-settings') ? 'Saving…' : 'Save Indexer Settings'}
          </Button>
        </div>
      {/if}

      <Panel title="Per-Indexer Policies" subtitle="Assign a static score modifier to releases from a specific indexer. Positive boosts, negative penalises.">
        <div class="settings-list-layout">
          <div class="settings-list">
            <div class="settings-list-header">
              <span>Policies</span>
              <Button kind="secondary" on:click={() => { editingPolicy = { indexerName: '', scoreModifier: 0, enabled: true, note: '' }; }}>
                <Plus size={14} /> New
              </Button>
            </div>
            {#if indexerPolicies.length === 0}
              <div class="empty-state">No per-indexer policies yet.</div>
            {/if}
            {#each indexerPolicies as p (p.id)}
              <button class="settings-list-item" class:active={editingPolicy?.id === p.id} on:click={() => { editingPolicy = { ...p }; }}>
                <span class="settings-list-item-name">{p.indexerName}</span>
                <div style="display:flex;align-items:center;gap:4px;flex-shrink:0">
                  <span class="settings-list-item-score" class:positive={p.scoreModifier > 0} class:negative={p.scoreModifier < 0}>{p.scoreModifier > 0 ? '+' : ''}{p.scoreModifier}</span>
                  {#if !p.enabled}<span class="settings-disabled-badge">off</span>{/if}
                </div>
              </button>
            {/each}
          </div>
          <div class="settings-editor">
            {#if editingPolicy}
              <div class="field">
                <label class="field-label" for="ip-name">Indexer Name <span class="field-hint">(exact match — case-sensitive)</span></label>
                <input id="ip-name" type="text" bind:value={editingPolicy.indexerName} placeholder="e.g. NZBFinder" disabled={!!editingPolicy.id} />
              </div>
              <div class="field">
                <label class="field-label" for="ip-score">Score Modifier</label>
                <input id="ip-score" type="number" bind:value={editingPolicy.scoreModifier} placeholder="e.g. 50 or -100" />
              </div>
              <div class="field">
                <label class="field-label" for="ip-note">Note <span class="field-hint">(optional)</span></label>
                <input id="ip-note" type="text" bind:value={editingPolicy.note} placeholder="Why this modifier exists" />
              </div>
              <label class="flag-row">
                <input type="checkbox" bind:checked={editingPolicy.enabled} />
                <div><strong>Enabled</strong><span>Apply this modifier when scoring releases</span></div>
              </label>
              <div class="editor-actions" style="margin-top:16px">
                {#if editingPolicy.id}
                  <Button kind="danger" on:click={() => editingPolicy?.id && deleteIndexerPolicy(editingPolicy.id)}>
                    <Trash2 size={15} /> Delete
                  </Button>
                {/if}
                <Button kind="ghost" on:click={() => { editingPolicy = null; }}>Cancel</Button>
                <Button kind="primary" on:click={saveIndexerPolicy} disabled={ipSaving}>
                  <Save size={15} /> {ipSaving ? 'Saving…' : 'Save'}
                </Button>
              </div>
            {:else}
              <div class="empty-state">Select a policy to edit, or add a new one.</div>
            {/if}
          </div>
        </div>
      </Panel>

    <!-- QUEUE -->
    {:else if activeTab === 'queue'}
      {#if draft}
        <div class="grid-2">
          <Panel title="Connection Budget" subtitle="Edit in the Providers tab. Shown here for reference.">
            <div class="kv-list">
              <div><span>Max connections</span><strong>{draft.usenet.maxDownloadConnections}</strong></div>
              <div><span>Streaming priority</span><strong>{draft.usenet.streamingPriorityPercent}%</strong></div>
              <div><span>Article buffer</span><strong>{draft.usenet.articleBufferSize}</strong></div>
              <div><span>Background queue depth</span><strong>{status?.backgroundQueueDepth ?? '—'}</strong></div>
            </div>
          </Panel>

          <Panel title="Queue Behavior" subtitle="Three independent worker lanes — read-ahead can never block interactive playback reads, at any load.">
            <div class="kv-list">
              <div><span>Interactive lane (playback reads)</span><strong>{100 - draft.usenet.streamingPriorityPercent}% of download connections</strong></div>
              <div><span>Read-ahead lane (prefetch)</span><strong>{draft.usenet.streamingPriorityPercent}% of download connections</strong></div>
              <div><span>Background lane</span><strong>{status?.backgroundQueueDepth ?? 0} queued</strong></div>
              <div><span>Retry path</span><strong>candidate fallback first</strong></div>
              <div><span>Seek prefetch</span><strong>deferred until first read</strong></div>
            </div>
          </Panel>
        </div>
      {/if}

      {#if policySettings}
        <Panel title="Queue Behavior" subtitle="How Drakkar handles duplicates, video validation and import method.">
          <div class="form-grid">
            <label class="form-field">
              <span>Duplicate NZB Behavior</span>
              <Select.Root type="single" bind:value={policySettings.duplicateNzbBehavior}>
                <Select.Trigger class="w-full">{duplicateNzbBehaviorLabels[policySettings.duplicateNzbBehavior] ?? policySettings.duplicateNzbBehavior}</Select.Trigger>
                <Select.Content>
                  <Select.Item value="mark_failed">Mark Failed</Select.Item>
                  <Select.Item value="ignore_existing">Ignore Existing</Select.Item>
                  <Select.Item value="download_again_with_suffix">Download Again (with suffix)</Select.Item>
                  <Select.Item value="replace_existing">Replace Existing</Select.Item>
                </Select.Content>
              </Select.Root>
            </label>
            <label class="form-field">
              <span>Import Strategy</span>
              <Select.Root type="single" bind:value={policySettings.importStrategy}>
                <Select.Trigger class="w-full">{importStrategyLabels[policySettings.importStrategy] ?? policySettings.importStrategy}</Select.Trigger>
                <Select.Content>
                  <Select.Item value="symlink">Symlink</Select.Item>
                  <Select.Item value="strm">STRM</Select.Item>
                  <Select.Item value="copy">Copy</Select.Item>
                </Select.Content>
              </Select.Root>
            </label>
            <label class="form-field">
              <span>Manual Upload Category</span>
              <input type="text" bind:value={policySettings.manualUploadCategory} placeholder="e.g. manual" />
            </label>
            <label class="form-field form-field--toggle" style="align-items:center;flex-direction:row;gap:12px">
              <span>Fail NZBs Without Video</span>
              <input type="checkbox" bind:checked={policySettings.failNzbWithoutVideo} />
            </label>
            <label class="form-field">
              <span>Blocklist Expiry (days)</span>
              <input type="number" min="0" bind:value={policySettings.blocklistTtlDays} placeholder="0 = never expire" />
            </label>
          </div>
          <div class="actions-row">
            <Button kind="secondary" on:click={savePolicies} disabled={loading || isBusy('save-policies')}>
              <Save size={16} />
              Save Behavior
            </Button>
          </div>
        </Panel>
      {/if}

      <Panel title="Automatic Queue Management" subtitle="Automated actions for failed releases. Applies only to known Drakkar failure reasons.">
        {#if policySettings}
          <div class="queue-rules">
            {#each queueDecisionRows as [key, label]}
              <label class="rule-row">
                <span class="rule-label">{label}</span>
                <Select.Root type="single" value={policySettings.queueDecisionActions[key]} onValueChange={(v) => { if (policySettings) policySettings.queueDecisionActions[key] = v as QueueDecisionAction; }}>
                  <Select.Trigger class="w-full">{queueDecisionLabels[policySettings.queueDecisionActions[key]] ?? policySettings.queueDecisionActions[key]}</Select.Trigger>
                  <Select.Content>
                    {#each Object.entries(queueDecisionLabels) as [v, text]}
                      <Select.Item value={v}>{text}</Select.Item>
                    {/each}
                  </Select.Content>
                </Select.Root>
              </label>
            {/each}
          </div>
          <div class="actions-row">
            <Button kind="secondary" on:click={savePolicies} disabled={loading || isBusy('save-policies')}>
              <Save size={16} />
              Save Queue Rules
            </Button>
          </div>
        {:else}
          <div class="empty">Policy settings unavailable.</div>
        {/if}
      </Panel>

    <!-- LIBRARY -->
    {:else if activeTab === 'library'}
      <div class="grid-2">
        <Panel title="Root Folders" subtitle="Host-side directories where Drakkar publishes media symlinks.">
          <div class="root-folders">
            <div class="root-row">
              <div class="root-path mono">/mnt/drakkar/media/movies</div>
              <StatusPill tone="ok">Movies</StatusPill>
            </div>
            <div class="root-row">
              <div class="root-path mono">/mnt/drakkar/media/tv</div>
              <StatusPill tone="ok">TV Shows</StatusPill>
            </div>
          </div>
          <div class="config-hint">
            <ShieldAlert size={14} />
            Root folder paths are compile-time defaults. Restart the container to change them via environment variables.
          </div>
        </Panel>

        <Panel title="Symlinks Instead of Copies" subtitle="How Drakkar publishes media — no disk duplication, instant availability.">
          <div class="hardlink-box">
            <div class="hardlink-icon">🔗</div>
            <div>
              <strong>Drakkar uses symlinks</strong>
              <p>Instead of copying or hardlinking files, Drakkar creates a lightweight symlink pointing to the virtual VFS path. This means <em>zero disk usage</em> for published media — the content stays remote on Usenet and is fetched on demand.</p>
              <p>Plex and Jellyfin follow the symlink into the FUSE mount transparently.</p>
            </div>
          </div>
        </Panel>
      </div>

      <div class="grid-2">
        <Panel title="Movie Naming" subtitle="Format used for published movie folders and files.">
          <div class="naming-section">
            <div class="naming-row">
              <span class="naming-label">Folder Format</span>
              <code class="naming-token">&#123;Movie Title&#125; (&#123;Release Year&#125;) &#123;tmdb-&#123;TmdbId&#125;&#125;</code>
            </div>
            <div class="naming-example mono">Dune (2021) &#123;tmdb-438631&#125;/</div>
            <div class="naming-row">
              <span class="naming-label">File Format</span>
              <code class="naming-token">&#123;Movie Title&#125; (&#123;Release Year&#125;).&#123;ext&#125;</code>
            </div>
            <div class="naming-example mono">Dune (2021).mkv</div>
          </div>
        </Panel>

        <Panel title="Episode Naming" subtitle="Format used for published TV show folders and episode files.">
          <div class="naming-section">
            <div class="naming-row">
              <span class="naming-label">Series Folder</span>
              <code class="naming-token">&#123;Series Title&#125; (&#123;Series Year&#125;) &#123;tvdb-&#123;TvdbId&#125;&#125;</code>
            </div>
            <div class="naming-example mono">Loki (2021) &#123;tvdb-362472&#125;/</div>
            <div class="naming-row">
              <span class="naming-label">Episode Format</span>
              <code class="naming-token">&#123;Series Title&#125; - S&#123;season:00&#125;E&#123;episode:00&#125;.&#123;ext&#125;</code>
            </div>
            <div class="naming-example mono">Loki (2021) - S02E01.mkv</div>
          </div>
        </Panel>
      </div>

    <!-- RULES -->
    {:else if activeTab === 'rules'}
      <Panel title="Runtime Blocklist" subtitle="Operational blocks created from failed fetches, archive rejects, missing articles, and manual runtime clears. Separate from Release Filtering rules and Custom Formats scoring.">
        <div class="bl-editor">
          <div class="bl-editor-head">
            <div>
              <strong>{blockEditor.id ? `Edit Entry #${blockEditor.id}` : 'Add Manual Entry'}</strong>
              <p>Use a structured URL/signature entry or paste a raw runtime key directly.</p>
            </div>
            {#if blockEditor.id}
              <Button kind="ghost" on:click={resetBlockEditor}>
                <X size={14} />
                Cancel Edit
              </Button>
            {/if}
          </div>
          <div class="form-grid form-grid--3col">
            <label class="form-field">
              <span>Entry Type</span>
              <Select.Root type="single" value={blockEditor.keyType} onValueChange={(v) => { blockEditor.keyType = v as BlocklistEditor['keyType']; }} disabled={!!blockEditor.id}>
                <Select.Trigger class="w-full">{blockEditorKeyTypeLabels[blockEditor.keyType]}</Select.Trigger>
                <Select.Content>
                  <Select.Item value="external_url">External URL</Select.Item>
                  <Select.Item value="release_signature">Release Signature</Select.Item>
                  <Select.Item value="raw">Raw Key</Select.Item>
                </Select.Content>
              </Select.Root>
            </label>
            <label class="form-field">
              <span>Reason</span>
              <input type="text" bind:value={blockEditor.reason} placeholder="manual" />
            </label>
            <label class="form-field">
              <span>Expires At</span>
              <input type="datetime-local" bind:value={blockEditor.expiresAt} />
            </label>
          </div>
          {#if blockEditor.keyType === 'external_url'}
            <div class="form-grid">
              <label class="form-field">
                <span>External URL</span>
                <input type="url" bind:value={blockEditor.externalUrl} placeholder="https://indexer.example/download/..." />
              </label>
            </div>
          {:else if blockEditor.keyType === 'release_signature'}
            <div class="form-grid form-grid--3col">
              <label class="form-field">
                <span>Release Title</span>
                <input type="text" bind:value={blockEditor.releaseTitle} placeholder="Dune.2021.2160p..." />
              </label>
              <label class="form-field">
                <span>Indexer Name</span>
                <input type="text" bind:value={blockEditor.indexerName} placeholder="NZB Finder" />
              </label>
              <label class="form-field">
                <span>Size (MB bucket)</span>
                <input type="number" min="0" bind:value={blockEditor.sizeMb} placeholder="7000" />
              </label>
            </div>
            <div class="form-grid">
              <label class="form-field">
                <span>Posted Date</span>
                <input type="date" bind:value={blockEditor.postedDate} />
              </label>
            </div>
          {:else}
            <div class="form-grid">
              <label class="form-field">
                <span>Raw Key</span>
                <input type="text" bind:value={blockEditor.key} placeholder="external_url:https://... or release_signature:..." />
              </label>
            </div>
          {/if}
          <div class="bl-editor-actions">
            <Button kind="primary" on:click={saveBlocklistEntry} disabled={isBusy('save-blocklist-entry')}>
              <Save size={14} />
              {blockEditor.id ? 'Update Entry' : 'Create Entry'}
            </Button>
          </div>
        </div>

        <!-- Stats chips -->
        {#if blStats}
          {@const sortedReasons = Object.entries(blStats.byReason).sort(([,a],[,b]) => b - a)}
          {@const visibleReasons = blShowAllReasons ? sortedReasons : sortedReasons.slice(0, BL_REASON_CHIP_LIMIT)}
          {@const hiddenCount = sortedReasons.length - BL_REASON_CHIP_LIMIT}
          <div class="bl-stats-row">
            <div class="bl-stat-chip">
              <span class="bl-stat-num">{blStats.active}</span>
              <span class="bl-stat-lbl">active</span>
            </div>
            <div class="bl-stat-chip warn">
              <span class="bl-stat-num">{blStats.expired}</span>
              <span class="bl-stat-lbl">expired</span>
            </div>
            {#each visibleReasons as [reason, count]}
              {@const label = reason.length > 40 ? reason.slice(0, 37) + '…' : reason}
              {@const colorKey = reason.split(':')[0].split('_')[0]}
              <button class="bl-reason-chip" class:active={blockReasonFilter === reason} title={reason}
                on:click={() => { blockReasonFilter = blockReasonFilter === reason ? 'all' : reason; blPage = 1; void loadBlocklist(); }}>
                <span class="reason-badge reason-{colorKey}">{label}</span>
                <span class="bl-reason-count">{count}</span>
              </button>
            {/each}
            {#if !blShowAllReasons && hiddenCount > 0}
              <button class="bl-show-more" on:click={() => blShowAllReasons = true}>+{hiddenCount} more</button>
            {:else if blShowAllReasons && sortedReasons.length > BL_REASON_CHIP_LIMIT}
              <button class="bl-show-more" on:click={() => blShowAllReasons = false}>show less</button>
            {/if}
          </div>
        {/if}

        <!-- Toolbar -->
        <div class="bl-toolbar">
          <div class="bl-search">
            <Search size={14} />
            <input bind:value={blockQuery} placeholder="Search key or reason…"
              on:input={() => { blPage = 1; void loadBlocklist(); }} />
          </div>
          {#if blockReasonFilter !== 'all'}
            <button class="bl-filter-active" on:click={() => { blockReasonFilter = 'all'; blPage = 1; void loadBlocklist(); }}>
              {blockReasonFilter} <X size={11} />
            </button>
          {/if}
          <div class="bl-stats-text mono">
            {blTotal} entr{blTotal === 1 ? 'y' : 'ies'}
          </div>
          <Select.Root type="single" value={String(blPageSize)} onValueChange={(v) => { blPageSize = Number(v); blPage = 1; void loadBlocklist(); }}>
            <Select.Trigger class="w-auto">{blPageSize} / page</Select.Trigger>
            <Select.Content>
              <Select.Item value="25">25 / page</Select.Item>
              <Select.Item value="50">50 / page</Select.Item>
              <Select.Item value="100">100 / page</Select.Item>
            </Select.Content>
          </Select.Root>
          {#if blTotal > 0}
            <Button kind="ghost" on:click={clearAllBlocklist} disabled={loading || isBusy('clear-all-blocklist')}>
              <X size={14} />
              Clear all
            </Button>
          {/if}
        </div>

        <!-- Table -->
        {#if blLoading}
          <div class="empty">Loading…</div>
        {:else if filteredBlocklist.length > 0}
          <div class="bl-table-wrap">
            <Table.Root class="bl-table">
              <Table.Header>
                <Table.Row>
                  <Table.Head class="sortable" onclick={() => { if (blockSortCol === 'reason') blockSortDir = blockSortDir === 'asc' ? 'desc' : 'asc'; else { blockSortCol = 'reason'; blockSortDir = 'asc'; } void loadBlocklist(); }}>
                    Reason {blockSortCol === 'reason' ? (blockSortDir === 'asc' ? '↑' : '↓') : ''}
                  </Table.Head>
                  <Table.Head class="sortable" onclick={() => { if (blockSortCol === 'key') blockSortDir = blockSortDir === 'asc' ? 'desc' : 'asc'; else { blockSortCol = 'key'; blockSortDir = 'asc'; } void loadBlocklist(); }}>
                    Runtime Key {blockSortCol === 'key' ? (blockSortDir === 'asc' ? '↑' : '↓') : ''}
                  </Table.Head>
                  <Table.Head>Matched Release</Table.Head>
                  <Table.Head class="sortable" onclick={() => { if (blockSortCol === 'createdAt') blockSortDir = blockSortDir === 'asc' ? 'desc' : 'asc'; else { blockSortCol = 'createdAt'; blockSortDir = 'desc'; } void loadBlocklist(); }}>
                    Added {blockSortCol === 'createdAt' ? (blockSortDir === 'asc' ? '↑' : '↓') : ''}
                  </Table.Head>
                  <Table.Head class="sortable" onclick={() => { if (blockSortCol === 'expires') blockSortDir = blockSortDir === 'asc' ? 'desc' : 'asc'; else { blockSortCol = 'expires'; blockSortDir = 'asc'; } void loadBlocklist(); }}>
                    Expires {blockSortCol === 'expires' ? (blockSortDir === 'asc' ? '↑' : '↓') : ''}
                  </Table.Head>
                  <Table.Head></Table.Head>
                </Table.Row>
              </Table.Header>
              <Table.Body>
                {#each filteredBlocklist as item (item.id)}
                  <Table.Row>
                    <Table.Cell>
                      <span class="reason-badge reason-{item.reason.split('_')[0]}">{item.reason}</span>
                    </Table.Cell>
                    <Table.Cell class="bl-key-cell">
                      <div class="bl-key-top">
                        {#if item.keyType === 'external_url' || item.keyType === 'release_signature'}
                          <span class="reason-badge neutral">{blocklistKeyLabel(item)}</span>
                        {/if}
                        <span class="bl-key mono" title={item.key}>{item.key}</span>
                        <button class="icon-btn" type="button" on:click={() => copyBlocklistKey(item.key)} title="Copy runtime key">
                          <Copy size={13} />
                        </button>
                      </div>
                    </Table.Cell>
                    <Table.Cell class="bl-context-cell">
                      {#if blocklistContext(item)}
                        <div class="bl-context-title">{item.releaseTitle || 'Matched release'}</div>
                        <div class="muted mono">{blocklistContext(item)}</div>
                        {#if item.libraryItemId || item.selectedReleaseId}
                          <div class="muted mono">
                            {#if item.libraryItemId}library #{item.libraryItemId}{/if}
                            {#if item.libraryItemId && item.selectedReleaseId} • {/if}
                            {#if item.selectedReleaseId}selected #{item.selectedReleaseId}{/if}
                          </div>
                        {/if}
                      {:else}
                        <span class="muted" title="No linked release metadata available.">—</span>
                      {/if}
                    </Table.Cell>
                    <Table.Cell class="muted mono">{item.createdAt ? new Date(item.createdAt).toLocaleDateString('en-GB') : '—'}</Table.Cell>
                    <Table.Cell class="muted mono">{item.expiresAt ? new Date(item.expiresAt).toLocaleDateString('en-GB') : 'Never'}</Table.Cell>
                    <Table.Cell class="bl-action">
                      <div class="bl-row-actions">
                        <button class="icon-btn" type="button" on:click={() => clearBlocklistByReason(item.reason)} disabled={isBusy(`clear-blocklist-reason-${item.reason}`)} title="Clear all with this reason">
                          <Trash2 size={13} />
                        </button>
                        <button class="icon-btn" type="button" on:click={() => startEditBlocklist(item)} title="Edit entry">
                          <Pencil size={13} />
                        </button>
                        <button class="clear-btn" on:click={() => clearBlocklist(item.id)} disabled={isBusy(`clear-blocklist-${item.id}`)} title="Clear this entry">
                        <X size={13} />
                        </button>
                      </div>
                    </Table.Cell>
                  </Table.Row>
                {/each}
              </Table.Body>
            </Table.Root>
          </div>
          <!-- Pagination -->
          <div class="bl-pagination">
            <Pagination page={blPage} totalPages={blTotalPages} on:change={(e) => { blPage = e.detail; void loadBlocklist(); }} />
          </div>
        {:else}
          <div class="empty">{blStats?.active === 0 ? 'No active blocklist entries.' : 'No entries match the current filter.'}</div>
        {/if}
      </Panel>

      <div class="grid-2" style="margin-top:16px">
        <Panel title="Ignored File Patterns" subtitle="Patterns skipped from imported NZBs and library processing.">
          {#if policySettings}
            <textarea class="pattern-box" value={policySettings.ignoredPatterns.join('\n')} on:change={(e) => {
              const t = e.currentTarget as HTMLTextAreaElement;
              const cur = policySettings;
              if (!cur) return;
              policySettings = { ...cur, ignoredPatterns: t.value.split('\n').map((l) => l.trim()).filter(Boolean) };
            }}></textarea>
            <div class="actions-row">
              <Button kind="secondary" on:click={savePolicies} disabled={loading || isBusy('save-policies')}>
                <Save size={16} />
                Save Patterns
              </Button>
            </div>
          {:else}
            <div class="empty">Unavailable.</div>
          {/if}
        </Panel>

        <Panel title="Maintenance" subtitle="Operator cleanup and cache controls.">
          <div class="maint-list">
            <Button kind="secondary" on:click={pruneCache} disabled={loading || isBusy('prune-cache')}>
              <Wrench size={16} />
              Prune Block Cache
            </Button>
          </div>
          <p class="maint-note">Orphaned VFS content, broken symlinks, and completed orphans are cleaned automatically by the <strong>Storage Maintenance</strong> scheduled task (every 6 h). Run it from the Tasks tab for an immediate pass.</p>
          {#if lastCachePrune}
            <div class="result-box">
              <strong>cache-prune</strong>
              <div class="result-grid mono">
                <span>limit: {bytes(lastCachePrune.limitBytes)}</span>
                <span>deleted: {lastCachePrune.deletedFiles} files</span>
                <span>before: {lastCachePrune.filesBefore}</span>
                <span>after: {lastCachePrune.filesAfter}</span>
              </div>
            </div>
          {/if}
        </Panel>
      </div>

    <!-- QUALITY -->
    {:else if activeTab === 'quality'}
      <div class="quality-sub-tabs">
        <button class="sub-tab-btn" class:active={qualitySubTab === 'profiles'} on:click={() => { qualitySubTab = 'profiles'; }} type="button">Profiles</button>
        <button class="sub-tab-btn" class:active={qualitySubTab === 'definitions'} on:click={() => { qualitySubTab = 'definitions'; }} type="button">Quality Definitions</button>
      </div>

      {#if qualitySubTab === 'definitions'}
        {@const movieDefs = qualityDefs.filter(d => d.mediaType === 'movie')}
        {@const episodeDefs = qualityDefs.filter(d => d.mediaType === 'episode')}
        <div class="qdef-shell">
          <Panel title="Movie Quality Definitions" subtitle="Per-tier size limits (MB/min) applied when ranking movie releases. Requires runtime metadata. Set 0 for no limit.">
            <Table.Root class="qdef-table">
              <Table.Header><Table.Row><Table.Head>Quality</Table.Head><Table.Head>Min (MB/min)</Table.Head><Table.Head>Max (MB/min)</Table.Head><Table.Head></Table.Head></Table.Row></Table.Header>
              <Table.Body>
                {#each movieDefs as d (d.id)}
                  <Table.Row>
                    <Table.Cell class="qdef-title">{d.title}</Table.Cell>
                    <Table.Cell><input type="number" min="0" class="qdef-input" bind:value={d.minMbPerMinute} on:input={() => { qualityDefsDirty = new Set([...qualityDefsDirty, d.id]); }} /></Table.Cell>
                    <Table.Cell><input type="number" min="0" class="qdef-input" bind:value={d.maxMbPerMinute} on:input={() => { qualityDefsDirty = new Set([...qualityDefsDirty, d.id]); }} /></Table.Cell>
                    <Table.Cell><Button kind="ghost" disabled={!qualityDefsDirty.has(d.id) || qualityDefsSaving.has(d.id)} on:click={() => saveQualityDef(d)} type="button">{qualityDefsSaving.has(d.id) ? '…' : 'Save'}</Button></Table.Cell>
                  </Table.Row>
                {/each}
              </Table.Body>
            </Table.Root>
          </Panel>
          <Panel title="TV / Episode Quality Definitions" subtitle="Per-tier size limits (MB/min) applied when ranking TV episode releases. Set 0 for no limit.">
            <Table.Root class="qdef-table">
              <Table.Header><Table.Row><Table.Head>Quality</Table.Head><Table.Head>Min (MB/min)</Table.Head><Table.Head>Max (MB/min)</Table.Head><Table.Head></Table.Head></Table.Row></Table.Header>
              <Table.Body>
                {#each episodeDefs as d (d.id)}
                  <Table.Row>
                    <Table.Cell class="qdef-title">{d.title}</Table.Cell>
                    <Table.Cell><input type="number" min="0" class="qdef-input" bind:value={d.minMbPerMinute} on:input={() => { qualityDefsDirty = new Set([...qualityDefsDirty, d.id]); }} /></Table.Cell>
                    <Table.Cell><input type="number" min="0" class="qdef-input" bind:value={d.maxMbPerMinute} on:input={() => { qualityDefsDirty = new Set([...qualityDefsDirty, d.id]); }} /></Table.Cell>
                    <Table.Cell><Button kind="ghost" disabled={!qualityDefsDirty.has(d.id) || qualityDefsSaving.has(d.id)} on:click={() => saveQualityDef(d)} type="button">{qualityDefsSaving.has(d.id) ? '…' : 'Save'}</Button></Table.Cell>
                  </Table.Row>
                {/each}
              </Table.Body>
            </Table.Root>
          </Panel>
        </div>
      {:else}
      <div class="settings-list-layout">
        <aside class="settings-list">
          {#each profiles as p (p.id ?? p.name)}
            <button class="settings-list-item" class:active={selectedProfile?.id === p.id} on:click={() => { selectedProfile = { ...p }; }} type="button">
              <div class="settings-list-item-name flex items-center gap-1.5">
                {#if p.isDefault}<Star size={12} class="text-primary" />{/if}
                {p.name}
              </div>
              <div class="settings-list-item-meta">{p.resolutions.slice(0,2).join(', ')}</div>
            </button>
          {/each}
          {#if profiles.length === 0 && !loading}<div class="empty">No profiles yet.</div>{/if}
          <Button kind="secondary" on:click={() => { selectedProfile = blankProfile(); }}>
            <Plus size={14} /> New Profile
          </Button>
        </aside>

        {#if selectedProfile}
          <div class="settings-editor">
            <Panel title={selectedProfile.id ? `Edit: ${selectedProfile.name}` : 'New Profile'} subtitle="Settings control how releases are ranked and filtered.">
              <div slot="actions">
                {#if selectedProfile.isDefault}<StatusPill tone="ok">Default</StatusPill>{/if}
              </div>

              <div class="field">
                <label class="field-label" for="pname">Profile Name</label>
                <input id="pname" class="field-input" bind:value={selectedProfile.name} placeholder="e.g. Movie HD" />
              </div>
              <div class="divider"></div>

              <!-- Resolutions ordered -->
              <div class="field">
                <div class="field-label">Resolutions <span class="field-hint">rank by priority</span></div>
                <div class="ordered-list">
                  {#each selectedProfile.resolutions as res, i}
                    <div class="ordered-row">
                      <span class="rank">{i+1}</span>
                      <span class="ordered-value">{res}</span>
                      <button type="button" class="rank-btn" aria-label={`Move ${res} up`} on:click={() => { selectedProfile = { ...selectedProfile!, resolutions: profileMoveUp(selectedProfile!.resolutions, i) }; }} disabled={i===0}><ChevronUp size={13}/></button>
                      <button type="button" class="rank-btn" aria-label={`Move ${res} down`} on:click={() => { selectedProfile = { ...selectedProfile!, resolutions: profileMoveDown(selectedProfile!.resolutions, i) }; }} disabled={i===selectedProfile.resolutions.length-1}><ChevronDown size={13}/></button>
                      <button type="button" class="rank-btn remove" aria-label={`Remove ${res}`} on:click={() => { selectedProfile = { ...selectedProfile!, resolutions: selectedProfile!.resolutions.filter(v=>v!==res) }; }}>✕</button>
                    </div>
                  {/each}
                  <div class="chip-row">
                    {#each ALL_RESOLUTIONS.filter(r => !selectedProfile!.resolutions.includes(r)) as r}
                      <button type="button" class="chip add" on:click={() => { selectedProfile = { ...selectedProfile!, resolutions: [...selectedProfile!.resolutions, r] }; }}>{r} +</button>
                    {/each}
                  </div>
                </div>
              </div>

              <!-- Sources ordered -->
              <div class="field">
                <div class="field-label">Sources <span class="field-hint">rank by priority</span></div>
                <div class="ordered-list">
                  {#each selectedProfile.sources as src, i}
                    <div class="ordered-row">
                      <span class="rank">{i+1}</span><span class="ordered-value">{src}</span>
                      <button type="button" class="rank-btn" aria-label={`Move ${src} up`} on:click={() => { selectedProfile = { ...selectedProfile!, sources: profileMoveUp(selectedProfile!.sources, i) }; }} disabled={i===0}><ChevronUp size={13}/></button>
                      <button type="button" class="rank-btn" aria-label={`Move ${src} down`} on:click={() => { selectedProfile = { ...selectedProfile!, sources: profileMoveDown(selectedProfile!.sources, i) }; }} disabled={i===selectedProfile.sources.length-1}><ChevronDown size={13}/></button>
                      <button type="button" class="rank-btn remove" aria-label={`Remove ${src}`} on:click={() => { selectedProfile = { ...selectedProfile!, sources: selectedProfile!.sources.filter(v=>v!==src) }; }}>✕</button>
                    </div>
                  {/each}
                  <div class="chip-row">
                    {#each ALL_SOURCES.filter(s => !selectedProfile!.sources.includes(s)) as s}
                      <button type="button" class="chip add" on:click={() => { selectedProfile = { ...selectedProfile!, sources: [...selectedProfile!.sources, s] }; }}>{s} +</button>
                    {/each}
                  </div>
                </div>
              </div>

              <!-- Codecs ordered -->
              <div class="field">
                <div class="field-label">Codecs <span class="field-hint">rank by priority</span></div>
                <div class="ordered-list">
                  {#each selectedProfile.codecs as c, i}
                    <div class="ordered-row">
                      <span class="rank">{i+1}</span><span class="ordered-value">{c}</span>
                      <button type="button" class="rank-btn" aria-label={`Move ${c} up`} on:click={() => { selectedProfile = { ...selectedProfile!, codecs: profileMoveUp(selectedProfile!.codecs, i) }; }} disabled={i===0}><ChevronUp size={13}/></button>
                      <button type="button" class="rank-btn" aria-label={`Move ${c} down`} on:click={() => { selectedProfile = { ...selectedProfile!, codecs: profileMoveDown(selectedProfile!.codecs, i) }; }} disabled={i===selectedProfile.codecs.length-1}><ChevronDown size={13}/></button>
                      <button type="button" class="rank-btn remove" aria-label={`Remove ${c}`} on:click={() => { selectedProfile = { ...selectedProfile!, codecs: selectedProfile!.codecs.filter(v=>v!==c) }; }}>✕</button>
                    </div>
                  {/each}
                  <div class="chip-row">
                    {#each ALL_CODECS.filter(c => !selectedProfile!.codecs.includes(c)) as c}
                      <button type="button" class="chip add" on:click={() => { selectedProfile = { ...selectedProfile!, codecs: [...selectedProfile!.codecs, c] }; }}>{c} +</button>
                    {/each}
                  </div>
                </div>
              </div>

              <div class="divider"></div>

              <!-- Audio ordered -->
              <div class="field">
                <div class="field-label">Audio Formats <span class="field-hint">rank by priority</span></div>
                <div class="ordered-list">
                  {#each selectedProfile.audioFormats as a, i}
                    <div class="ordered-row">
                      <span class="rank">{i+1}</span><span class="ordered-value">{a}</span>
                      <button type="button" class="rank-btn" aria-label={`Move ${a} up`} on:click={() => { selectedProfile = { ...selectedProfile!, audioFormats: profileMoveUp(selectedProfile!.audioFormats, i) }; }} disabled={i===0}><ChevronUp size={13}/></button>
                      <button type="button" class="rank-btn" aria-label={`Move ${a} down`} on:click={() => { selectedProfile = { ...selectedProfile!, audioFormats: profileMoveDown(selectedProfile!.audioFormats, i) }; }} disabled={i===selectedProfile.audioFormats.length-1}><ChevronDown size={13}/></button>
                      <button type="button" class="rank-btn remove" aria-label={`Remove ${a}`} on:click={() => { selectedProfile = { ...selectedProfile!, audioFormats: selectedProfile!.audioFormats.filter(v=>v!==a) }; }}>✕</button>
                    </div>
                  {/each}
                  <div class="chip-row">
                    {#each ALL_AUDIO.filter(a => !selectedProfile!.audioFormats.includes(a)) as a}
                      <button type="button" class="chip add" on:click={() => { selectedProfile = { ...selectedProfile!, audioFormats: [...selectedProfile!.audioFormats, a] }; }}>{a} +</button>
                    {/each}
                  </div>
                </div>
              </div>

              <!-- HDR ordered -->
              <div class="field">
                <div class="field-label">HDR Formats <span class="field-hint">rank by priority</span></div>
                <div class="ordered-list">
                  {#each selectedProfile.hdrFormats as h, i}
                    <div class="ordered-row">
                      <span class="rank">{i+1}</span><span class="ordered-value">{h}</span>
                      <button type="button" class="rank-btn" aria-label={`Move ${h} up`} on:click={() => { selectedProfile = { ...selectedProfile!, hdrFormats: profileMoveUp(selectedProfile!.hdrFormats, i) }; }} disabled={i===0}><ChevronUp size={13}/></button>
                      <button type="button" class="rank-btn" aria-label={`Move ${h} down`} on:click={() => { selectedProfile = { ...selectedProfile!, hdrFormats: profileMoveDown(selectedProfile!.hdrFormats, i) }; }} disabled={i===selectedProfile.hdrFormats.length-1}><ChevronDown size={13}/></button>
                      <button type="button" class="rank-btn remove" aria-label={`Remove ${h}`} on:click={() => { selectedProfile = { ...selectedProfile!, hdrFormats: selectedProfile!.hdrFormats.filter(v=>v!==h) }; }}>✕</button>
                    </div>
                  {/each}
                  <div class="chip-row">
                    {#each ALL_HDR.filter(h => !selectedProfile!.hdrFormats.includes(h)) as h}
                      <button type="button" class="chip add" on:click={() => { selectedProfile = { ...selectedProfile!, hdrFormats: [...selectedProfile!.hdrFormats, h] }; }}>{h} +</button>
                    {/each}
                  </div>
                </div>
              </div>

              <div class="divider"></div>

              <!-- Languages chips -->
              <div class="field">
                <div class="field-label">Languages</div>
                <div class="chip-row">
                  {#each ALL_LANGUAGES as lang}
                    <button type="button" class="chip" class:on={selectedProfile.languages.includes(lang)} on:click={() => { selectedProfile = { ...selectedProfile!, languages: profileToggle(selectedProfile!.languages, lang) }; }}>{lang}</button>
                  {/each}
                </div>
              </div>

              <div class="divider"></div>

              <!-- Flags -->
              <div class="field">
                <div class="field-label">Release Flags</div>
                <div class="flags-grid">
                  <label class="flag-row">
                    <input type="checkbox" bind:checked={selectedProfile.preferProper} />
                    <div><strong>Prefer Proper</strong><span>Boost score when release is marked PROPER</span></div>
                  </label>
                  <label class="flag-row">
                    <input type="checkbox" bind:checked={selectedProfile.preferRepack} />
                    <div><strong>Prefer Repack</strong><span>Boost score when release is marked REPACK</span></div>
                  </label>
                  <label class="flag-row">
                    <input type="checkbox" bind:checked={selectedProfile.rejectCam} />
                    <div><strong>Reject CAM / TS / Telecine</strong><span>Hard-reject low-quality cam captures and telesyncs</span></div>
                  </label>
                  <label class="flag-row">
                    <input type="checkbox" bind:checked={selectedProfile.allowUpgrade} />
                    <div><strong>Allow Quality Upgrade</strong><span>Periodically re-search available items for a higher-quality release</span></div>
                  </label>
                </div>
              </div>

              <div class="divider"></div>

              <!-- Cutoff + minimum age -->
              <div class="field">
                <div class="field-label">Upgrade Cutoff</div>
                <div class="size-row">
                  <label>
                    <span>Cutoff Resolution</span>
                    <Select.Root type="single" bind:value={selectedProfile.cutoffResolution}>
                      <Select.Trigger class="size-input w-full">{selectedProfile.cutoffResolution || 'No cutoff'}</Select.Trigger>
                      <Select.Content>
                        <Select.Item value="">No cutoff</Select.Item>
                        {#each ALL_RESOLUTIONS as r}
                          <Select.Item value={r}>{r}</Select.Item>
                        {/each}
                      </Select.Content>
                    </Select.Root>
                  </label>
                  <label>
                    <span>Minimum Age (hours)</span>
                    <input type="number" min="0" bind:value={selectedProfile.minimumAgeHours} class="size-input" placeholder="0 = no delay" />
                  </label>
                </div>
                <p class="field-hint" style="margin-top:4px">Stop upgrading once resolution reaches cutoff. Minimum age rejects releases posted within N hours.</p>
              </div>

              <div class="divider"></div>

              <div class="field">
                <div class="field-label">Upgrade Threshold</div>
                <div class="size-row">
                  <label>
                    <span>Minimum CF Upgrade</span>
                    <input type="number" min="0" bind:value={selectedProfile.minimumUpgradeCustomFormatScore} class="size-input" placeholder="0 = no minimum" />
                  </label>
                </div>
                <p class="field-hint" style="margin-top:4px">When upgrades are enabled, the candidate must improve the custom-format subtotal by at least this amount over the current release.</p>
              </div>

              <div class="divider"></div>

              <!-- Size limits -->
              <div class="field">
                <div class="field-label">Size Limits</div>
                <div class="size-row">
                  <label><span>Min (MB/min)</span><input type="number" min="0" bind:value={selectedProfile.minMbPerMinute} class="size-input" placeholder="0 = no limit" /></label>
                  <label><span>Max (MB/min)</span><input type="number" min="0" bind:value={selectedProfile.maxMbPerMinute} class="size-input" placeholder="0 = no limit" /></label>
                </div>
                <p class="field-hint" style="margin-top:4px">Applied per runtime minute. If runtime metadata is missing, size limits are skipped instead of hard-rejecting the release.</p>
              </div>

              <div class="divider"></div>

              <!-- Exclude patterns -->
              <div class="field">
                <div class="field-label">Exclude Patterns <span class="field-hint">(regex, one per line — titles matching any pattern are rejected)</span></div>
                <textarea
                  class="exclude-patterns-input"
                  rows="4"
                  placeholder="e.g. \.FRENCH\.\n\.GERMAN\.\nHardcoded"
                  value={(selectedProfile.excludePatterns ?? []).join('\n')}
                  on:input={(e) => { selectedProfile = { ...selectedProfile!, excludePatterns: (e.currentTarget as HTMLTextAreaElement).value.split('\n').map(s => s.trim()).filter(Boolean) }; }}
                ></textarea>
              </div>

              <div class="divider"></div>

              <div class="editor-actions">
                {#if selectedProfile.id && !selectedProfile.isDefault}
                  <Button kind="danger" on:click={() => selectedProfile && deleteSelectedProfile(selectedProfile)} disabled={profileSaving}>
                    <Trash2 size={15} /> Delete
                  </Button>
                {/if}
                <Button kind="primary" on:click={saveSelectedProfile} disabled={profileSaving}>
                  <Save size={15} /> {profileSaving ? 'Saving…' : 'Save Profile'}
                </Button>
              </div>
            </Panel>
          </div>
        {:else}
          <div class="empty-state">Select a profile to edit, or create a new one.</div>
        {/if}
      </div>
      {/if}

    <!-- CUSTOM FORMATS -->
    {:else if activeTab === 'formats'}
      <Panel title="Custom Formats" subtitle="User-defined scoring rules applied to release titles. Positive scores boost, negative scores penalise.">
        {#if cfImportOpen}
          <div class="settings-import-box">
            <div class="settings-import-header">
              <strong>Import Custom Formats</strong>
              <span class="field-hint">Paste a JSON array of custom format objects. Fields: name, pattern, score, enabled.</span>
            </div>
            <textarea class="settings-import-textarea" bind:value={cfImportJson} rows={8} placeholder={`[{"name":"BluRay","pattern":"(?i)bluray","score":50,"enabled":true}]`}></textarea>
            <div class="editor-actions" style="margin-top:10px">
              <Button kind="ghost" on:click={() => { cfImportOpen = false; cfImportJson = ''; }}>Cancel</Button>
              <Button kind="primary" on:click={importCustomFormats} disabled={cfImporting || !cfImportJson.trim()}>
                {cfImporting ? 'Importing…' : 'Import'}
              </Button>
            </div>
          </div>
        {/if}
        <div class="settings-list-layout">
          <div class="settings-list">
            <div class="settings-list-header">
              <span>Formats</span>
              <div class="flex gap-1.5">
                <Button kind="secondary" on:click={() => { cfImportOpen = !cfImportOpen; cfImportJson = ''; }}>
                  Import
                </Button>
                <Button kind="secondary" on:click={() => { editingFormat = blankFormat(); }}>
                  <Plus size={14} /> New
                </Button>
              </div>
            </div>
            {#if customFormats.length === 0}
              <div class="empty-state">No custom formats yet.</div>
            {/if}
            {#each customFormats as f (f.id)}
              <button class="settings-list-item" class:active={editingFormat?.id === f.id} on:click={() => { editingFormat = { ...f }; }}>
                <span class="settings-list-item-name">{f.name}</span>
                <div style="display:flex;align-items:center;gap:4px;flex-shrink:0">
                  {#if f.source && f.source !== 'custom'}<span class="settings-badge settings-badge-info">{f.source}</span>{/if}
                  <span class="settings-list-item-score" class:positive={f.score > 0} class:negative={f.score < 0}>{f.score > 0 ? '+' : ''}{f.score}</span>
                  {#if !f.enabled}<span class="settings-disabled-badge">off</span>{/if}
                </div>
              </button>
            {/each}
          </div>
          <div class="settings-editor">
            {#if editingFormat}
              <div class="field">
                <label class="field-label" for="cf-name">Name</label>
                <input id="cf-name" type="text" bind:value={editingFormat.name} placeholder="e.g. BluRay Boost" />
              </div>
              <div class="field">
                <label class="field-label" for="cf-pattern">Pattern <span class="field-hint">(regex matched against release title)</span></label>
                <input id="cf-pattern" type="text" bind:value={editingFormat.pattern} placeholder="(?i)bluray" />
              </div>
              <div class="field">
                <label class="field-label" for="cf-score">Score</label>
                <input id="cf-score" type="number" bind:value={editingFormat.score} placeholder="e.g. 50 or -100" />
              </div>
              <label class="flag-row">
                <input type="checkbox" bind:checked={editingFormat.enabled} />
                <div><strong>Enabled</strong><span>Apply this format when scoring releases</span></div>
              </label>
              <div class="editor-actions" style="margin-top:16px">
                {#if editingFormat.id}
                  <Button kind="danger" on:click={() => editingFormat?.id && deleteFormat(editingFormat.id)}>
                    <Trash2 size={15} /> Delete
                  </Button>
                {/if}
                <Button kind="ghost" on:click={() => { editingFormat = null; }}>Cancel</Button>
                <Button kind="primary" on:click={saveFormat} disabled={cfSaving}>
                  <Save size={15} /> {cfSaving ? 'Saving…' : 'Save'}
                </Button>
              </div>
            {:else}
              <div class="empty-state">Select a format to edit, or create a new one.</div>
            {/if}
          </div>
        </div>
      </Panel>

    <!-- RELEASE FILTERING -->
    {:else if activeTab === 'filtering'}
      <Panel title="Release Filtering" subtitle="Block or penalise known low-quality releases by group, title pattern, or regex. Default rules are from TRaSH Guides LQ lists.">
        <div class="settings-list-layout">
          <!-- Rule list -->
          <div class="settings-list">
            <div class="settings-list-header">
              <span>Rules ({blockRules.filter(r => r.enabled).length}/{blockRules.length} enabled)</span>
              <Button kind="secondary" on:click={() => { editingRule = blankRule(); }}>
                <Plus size={14} /> New
              </Button>
            </div>

            {#each [['release_group','Release Groups'], ['title_pattern','Title Patterns'], ['regex','Regex'], ['missing_release_group','Missing Group']] as [typeKey, typeLabel] (typeKey)}
              {@const group = blockRules.filter(r => r.type === typeKey)}
              {#if group.length > 0}
                <div class="settings-type-header">{typeLabel} <span class="settings-count-badge">{group.filter(r => r.enabled).length}/{group.length}</span></div>
                {#each group as rule (rule.id)}
                  <button class="settings-list-item" class:active={editingRule?.id === rule.id} class:disabled={!rule.enabled}
                    on:click={() => { editingRule = { ...rule }; testResult = null; }}>
                    <span class="settings-list-item-name">{rule.pattern || '(any)'}</span>
                    <span class="settings-badge-row">
                      {#if rule.mediaType !== 'both'}<span class="settings-badge settings-badge-media">{rule.mediaType}</span>{/if}
                      <span class="settings-badge {rule.action === 'block' ? 'settings-badge-block' : 'settings-badge-penalty'}">
                        {rule.action === 'block' ? 'block' : `-${rule.scorePenalty}`}
                      </span>
                      {#if rule.source !== 'custom'}<span class="settings-badge settings-badge-info">{rule.source}</span>{/if}
                      {#if !rule.enabled}<span class="settings-badge settings-disabled-badge">off</span>{/if}
                    </span>
                  </button>
                {/each}
              {/if}
            {/each}

            {#if blockRules.length === 0}
              <div class="empty-state">No rules yet.</div>
            {/if}
          </div>

          <!-- Editor + test tool -->
          <div class="settings-editor">
            {#if editingRule}
              <div class="field">
                <label class="field-label" for="rf-type">Type</label>
                <Select.Root type="single" value={editingRule.type} onValueChange={(v) => { if (editingRule) editingRule.type = v as ReleaseBlockRule['type']; }} disabled={editingRule.id !== undefined && editingRule.source !== 'custom'}>
                  <Select.Trigger id="rf-type" class="w-full">{rfTypeLabels[editingRule.type]}</Select.Trigger>
                  <Select.Content>
                    <Select.Item value="release_group">Release Group</Select.Item>
                    <Select.Item value="title_pattern">Title Pattern</Select.Item>
                    <Select.Item value="regex">Regex</Select.Item>
                    <Select.Item value="missing_release_group">Missing Release Group</Select.Item>
                  </Select.Content>
                </Select.Root>
              </div>
              {#if editingRule.type !== 'missing_release_group'}
                <div class="field">
                  <label class="field-label" for="rf-pattern-input">Pattern
                    {#if editingRule.type === 'regex'}<span class="field-hint">(regex, case-insensitive)</span>{/if}
                    {#if editingRule.type === 'title_pattern'}<span class="field-hint">(substring match, dots normalised)</span>{/if}
                    {#if editingRule.type === 'release_group'}<span class="field-hint">(parsed group after last "-")</span>{/if}
                  </label>
                  <input id="rf-pattern-input" type="text" bind:value={editingRule.pattern}
                    placeholder={editingRule.type === 'release_group' ? 'e.g. GalaxyRG' : editingRule.type === 'title_pattern' ? 'e.g. AI Upscale' : '(?i)upscal(e|ed)'}
                    disabled={editingRule.id !== undefined && editingRule.source !== 'custom'} />
                </div>
              {/if}
              <div class="field">
                <label class="field-label" for="rf-mediatype">Media type</label>
                <Select.Root type="single" value={editingRule.mediaType} onValueChange={(v) => { if (editingRule) editingRule.mediaType = v as ReleaseBlockRule['mediaType']; }} disabled={editingRule.id !== undefined && editingRule.source !== 'custom'}>
                  <Select.Trigger id="rf-mediatype" class="w-full">{rfMediaTypeLabels[editingRule.mediaType]}</Select.Trigger>
                  <Select.Content>
                    <Select.Item value="both">Both</Select.Item>
                    <Select.Item value="movie">Movie only</Select.Item>
                    <Select.Item value="tv">TV only</Select.Item>
                  </Select.Content>
                </Select.Root>
              </div>
              <div class="field">
                <label class="field-label" for="rf-action">Action</label>
                <Select.Root type="single" value={editingRule.action} onValueChange={(v) => { if (editingRule) editingRule.action = v as ReleaseBlockRule['action']; }} disabled={editingRule.id !== undefined && editingRule.source !== 'custom'}>
                  <Select.Trigger id="rf-action" class="w-full">{rfActionLabels[editingRule.action]}</Select.Trigger>
                  <Select.Content>
                    <Select.Item value="block">Block (reject release)</Select.Item>
                    <Select.Item value="penalty">Penalty (reduce score)</Select.Item>
                  </Select.Content>
                </Select.Root>
              </div>
              {#if editingRule.action === 'penalty'}
                <div class="field">
                  <label class="field-label" for="rf-penalty">Score penalty <span class="field-hint">(positive = points subtracted)</span></label>
                  <input id="rf-penalty" type="number" bind:value={editingRule.scorePenalty} min="0"
                    disabled={editingRule.id !== undefined && editingRule.source !== 'custom'} />
                </div>
              {/if}
              <div class="field">
                <label class="field-label" for="rf-note">Note <span class="field-hint">(optional)</span></label>
                <input id="rf-note" type="text" bind:value={editingRule.note} placeholder="Why this rule exists" />
              </div>
              <label class="flag-row">
                <input type="checkbox" bind:checked={editingRule.enabled} />
                <div><strong>Enabled</strong><span>Apply this rule when scoring releases</span></div>
              </label>
              <div class="editor-actions" style="margin-top:16px">
                {#if editingRule.id && editingRule.source === 'custom'}
                  <Button kind="danger" on:click={() => editingRule?.id && deleteBlockRule(editingRule.id)}>
                    <Trash2 size={15} /> Delete
                  </Button>
                {/if}
                <Button kind="ghost" on:click={() => { editingRule = null; testResult = null; }}>Cancel</Button>
                <Button kind="primary" on:click={saveBlockRule} disabled={bfSaving}>
                  <Save size={15} /> {bfSaving ? 'Saving…' : 'Save'}
                </Button>
              </div>
              {#if editingRule.source !== 'custom'}
                <p class="settings-readonly-note">Default and TRaSH rules: only <strong>enabled</strong> and <strong>note</strong> can be changed. To customise, add a new custom rule.</p>
              {/if}
            {:else}
              <div class="empty-state">Select a rule to edit, or add a new custom rule.</div>
            {/if}

            <!-- Test tool -->
            <div class="settings-test-panel">
              <div class="settings-test-header">Test a release title</div>
              <div class="settings-test-row">
                <input type="text" bind:value={testTitle} placeholder="Movie.Title.2025.1080p.WEB-DL-GalaxyRG"
                  style="flex:1" on:keydown={(e) => e.key === 'Enter' && runBlockTest()} />
                <Select.Root type="single" value={testMediaType} onValueChange={(v) => { testMediaType = v as 'movie' | 'tv' | 'both'; }}>
                  <Select.Trigger style="width:100px">{testMediaTypeLabels[testMediaType]}</Select.Trigger>
                  <Select.Content>
                    <Select.Item value="both">Both</Select.Item>
                    <Select.Item value="movie">Movie</Select.Item>
                    <Select.Item value="tv">TV</Select.Item>
                  </Select.Content>
                </Select.Root>
                <Button kind="secondary" on:click={runBlockTest} disabled={testRunning || !testTitle.trim()}>
                  {testRunning ? '…' : 'Test'}
                </Button>
              </div>
              {#if testResult}
                <div class="settings-test-result" class:blocked={testResult.blocked} class:allowed={testResult.allowed && testResult.scorePenalty === 0}>
                  <strong>{testResult.blocked ? '🚫 Blocked' : testResult.scorePenalty > 0 ? `⚠ Penalty −${testResult.scorePenalty}` : '✓ Allowed'}</strong>
                  {#if testResult.matchedRules.length > 0}
                    <ul class="settings-test-matches">
                      {#each testResult.matchedRules as m}
                        <li><span class="settings-badge settings-badge-info">{m.type}</span> {m.reason}</li>
                      {/each}
                    </ul>
                  {/if}
                </div>
              {/if}
            </div>
          </div>
        </div>
      </Panel>

    <!-- SUBTITLE PROFILES -->
    {:else if activeTab === 'subtitle-profiles'}
      <Panel title="Subtitle Profiles" subtitle="Named language preference sets for subtitle acquisition. Assign a profile per library item to override the global language settings.">
        <div class="settings-list-layout">
          <div class="settings-list">
            <div class="settings-list-header">
              <span>Profiles</span>
              <Button kind="secondary" on:click={() => { editingSubtitleProfile = { name: '', languages: [], preferHearingImpaired: false, requireExactLanguage: false, isDefault: false }; }}>
                <Plus size={14} /> New
              </Button>
            </div>
            {#if subtitleProfiles.length === 0}
              <div class="empty-state">No subtitle profiles yet.</div>
            {/if}
            {#each subtitleProfiles as p (p.id)}
              <button class="settings-list-item" class:active={editingSubtitleProfile?.id === p.id} on:click={() => { editingSubtitleProfile = { ...p }; }}>
                <span class="settings-list-item-name">{p.name}</span>
                <div style="display:flex;align-items:center;gap:4px;flex-shrink:0">
                  {#if p.isDefault}<span class="settings-badge settings-badge-info">default</span>{/if}
                  {#if p.languages.length > 0}<span class="settings-disabled-badge">{p.languages.slice(0,2).join(', ')}{p.languages.length > 2 ? '…' : ''}</span>{/if}
                </div>
              </button>
            {/each}
          </div>
          <div class="settings-editor">
            {#if editingSubtitleProfile}
              <div class="field">
                <label class="field-label" for="sp-name">Profile Name</label>
                <input id="sp-name" type="text" bind:value={editingSubtitleProfile.name} placeholder="e.g. Dutch Preferred" />
              </div>
              <div class="field">
                <label class="field-label" for="sp-languages">Languages <span class="field-hint">(comma-separated ISO codes, e.g. nl, en)</span></label>
                <input id="sp-languages" type="text"
                  value={editingSubtitleProfile.languages.join(', ')}
                  on:input={(e) => { editingSubtitleProfile = { ...editingSubtitleProfile!, languages: (e.currentTarget as HTMLInputElement).value.split(',').map(l => l.trim()).filter(Boolean) }; }}
                  placeholder="nl, en" />
              </div>
              <label class="flag-row">
                <input type="checkbox" bind:checked={editingSubtitleProfile.preferHearingImpaired} />
                <div><strong>Prefer Hearing Impaired</strong><span>Boost scores for SDH/HI subtitles</span></div>
              </label>
              <label class="flag-row">
                <input type="checkbox" bind:checked={editingSubtitleProfile.requireExactLanguage} />
                <div><strong>Require Exact Language</strong><span>Skip subtitles in a different language</span></div>
              </label>
              <label class="flag-row">
                <input type="checkbox" bind:checked={editingSubtitleProfile.isDefault} />
                <div><strong>Set as Default</strong><span>Use this profile when no per-item profile is assigned</span></div>
              </label>
              <div class="editor-actions" style="margin-top:16px">
                {#if editingSubtitleProfile.id}
                  <Button kind="danger" on:click={() => editingSubtitleProfile?.id && deleteSubtitleProfile(editingSubtitleProfile.id)}>
                    <Trash2 size={15} /> Delete
                  </Button>
                {/if}
                <Button kind="ghost" on:click={() => { editingSubtitleProfile = null; }}>Cancel</Button>
                <Button kind="primary" on:click={saveSubtitleProfile} disabled={spSaving}>
                  <Save size={15} /> {spSaving ? 'Saving…' : 'Save'}
                </Button>
              </div>
            {:else}
              <div class="empty-state">Select a profile to edit, or create a new one.</div>
            {/if}
          </div>
        </div>
      </Panel>

    <!-- NOTIFICATIONS -->
    {:else if activeTab === 'notifications'}
      {#if draft}
      <Panel title="Notifications" subtitle="Send event notifications to Discord or a generic webhook.">
        <div class="field">
          <label class="field-label" for="notif-discord">Discord Webhook URL</label>
          <input id="notif-discord" type="url" bind:value={draft.notifications.discordWebhookUrl} placeholder="https://discord.com/api/webhooks/…" />
          <p class="field-hint">Paste a Discord channel webhook URL. Drakkar will send an embed when triggered events fire.</p>
        </div>
        <div class="divider"></div>
        <div class="field">
          <label class="field-label" for="notif-webhook">Generic Webhook URL</label>
          <input id="notif-webhook" type="url" bind:value={draft.notifications.genericWebhookUrl} placeholder="https://your-server.com/hook" />
          <p class="field-hint">Receives a JSON POST body with <code>eventType</code>, <code>title</code>, <code>resolution</code>, and other fields.</p>
        </div>
        <div class="divider"></div>
        <div class="field">
          <div class="field-label">Trigger Events</div>
          <div class="flags-grid">
            <label class="flag-row">
              <input type="checkbox" bind:checked={draft.notifications.onGrab} />
              <div><strong>On Grab</strong><span>Fire when a release is selected for download</span></div>
            </label>
            <label class="flag-row">
              <input type="checkbox" bind:checked={draft.notifications.onAvailable} />
              <div><strong>On Available</strong><span>Fire when an item finishes importing</span></div>
            </label>
            <label class="flag-row">
              <input type="checkbox" bind:checked={draft.notifications.onFailed} />
              <div><strong>On Failed</strong><span>Fire when an item permanently fails</span></div>
            </label>
          </div>
        </div>
        <div class="divider"></div>
        <div class="editor-actions">
          <Button kind="primary" on:click={saveSettings} disabled={isBusy('save-settings')}>
            <Save size={15} /> {isBusy('save-settings') ? 'Saving…' : 'Save Notifications'}
          </Button>
        </div>
      </Panel>
      {/if}

    <!-- PRIVACY ROUTING -->
    {:else if activeTab === 'privacy'}
      {#if draft}
      <Panel title="Privacy Routing" subtitle="Routes only Usenet and NZB indexer traffic. Plex/Jellyfin/metadata/local traffic always stays direct.">
        <div class="field">
          <div class="field-label">Routing Mode</div>
          <div class="flags-grid">
            <label class="flag-row">
              <input type="radio" name="privacy-mode" value="direct" bind:group={draft.privacy.mode} />
              <div><strong>Direct</strong><span>Usenet and indexer traffic use the normal network connection. No configuration required.</span></div>
            </label>
            <label class="flag-row">
              <input type="radio" name="privacy-mode" value="socks5" bind:group={draft.privacy.mode} />
              <div><strong>SOCKS5 Proxy</strong><span>Route through a SOCKS5 proxy. Never falls back to Direct if the proxy is unreachable.</span></div>
            </label>
            <label class="flag-row">
              <input type="radio" name="privacy-mode" value="wireguard" bind:group={draft.privacy.mode} />
              <div><strong>WireGuard VPN</strong><span>In-process userspace WireGuard tunnel. No separate VPN container required.</span></div>
            </label>
          </div>
          <p class="field-hint">Applies immediately after Save — no restart required.</p>
        </div>

        {#if draft.privacy.mode === 'socks5'}
          <div class="divider"></div>
          <div class="form-grid">
            <label class="form-field">
              <span>Host</span>
              <input type="text" bind:value={draft.privacy.socks5.host} placeholder="127.0.0.1" />
            </label>
            <label class="form-field">
              <span>Port</span>
              <input type="number" min="1" max="65535" bind:value={draft.privacy.socks5.port} />
            </label>
            <label class="form-field">
              <span>Username <small class="field-hint-inline">(optional)</small></span>
              <input type="text" bind:value={draft.privacy.socks5.username} autocomplete="off" />
            </label>
            <label class="form-field">
              <span>Password <small class="field-hint-inline">(leave blank to keep existing)</small></span>
              <input type="password" bind:value={draft.privacy.socks5.password} placeholder="••••••••" autocomplete="off" />
            </label>
            <label class="form-field">
              <span>Timeout (seconds)</span>
              <input type="number" min="1" bind:value={draft.privacy.socks5.timeoutSeconds} />
            </label>
          </div>
          <div class="flags-grid" style="margin-top:12px">
            <label class="flag-row">
              <input type="checkbox" bind:checked={draft.privacy.syncNzbHydra2Proxy} />
              <div>
                <strong>Sync NZBHydra2 proxy settings</strong>
                <span>
                  Push this SOCKS5 config into NZBHydra2's own proxy settings on save, so its own
                  outbound indexer traffic is routed too — Drakkar can't route NZBHydra2's traffic
                  itself, since it's a separate process with its own networking. Unchecking this
                  clears NZBHydra2's proxy back to none.
                </span>
              </div>
            </label>
          </div>
        {/if}

        {#if draft.privacy.mode === 'wireguard'}
          <div class="divider"></div>
          {#if privacyStatus?.wireguard}
            <div class="field">
              <div class="field-label">Current Configuration</div>
              <div class="wg-summary-grid">
                {#if privacyStatus.wireguard.interfaceAddress?.length}<div><strong>Interface Address</strong><span class="mono">{privacyStatus.wireguard.interfaceAddress.join(', ')}</span></div>{/if}
                {#if privacyStatus.wireguard.endpoint}<div><strong>Endpoint</strong><span class="mono">{privacyStatus.wireguard.endpoint}</span></div>{/if}
                {#if privacyStatus.wireguard.allowedIps?.length}<div><strong>Allowed IPs</strong><span class="mono">{privacyStatus.wireguard.allowedIps.join(', ')}</span></div>{/if}
                {#if privacyStatus.wireguard.dns?.length}<div><strong>DNS</strong><span class="mono">{privacyStatus.wireguard.dns.join(', ')}</span></div>{/if}
                {#if privacyStatus.wireguard.persistentKeepalive}<div><strong>Keepalive</strong><span class="mono">{privacyStatus.wireguard.persistentKeepalive}s</span></div>{/if}
              </div>
              <p class="field-hint">PrivateKey/PresharedKey are never shown or sent back to the browser.</p>
            </div>
            <div class="divider"></div>
          {/if}
          <!--
            The imported .conf text is staged into `draft.privacy.wireguard.configText`
            only — it is never sent to the backend by this control. It becomes active
            only after the user hits the page-level Save button, consistent with the
            draft/fullSettings pattern documented at the top of this file.
          -->
          <div class="field">
            <label class="field-label" for="wg-import">Import Configuration</label>
            {#if !wireguardImportOpen}
              <div class="actions-row">
                <Button kind="ghost" on:click={() => { wireguardImportOpen = true; wireguardImportText = ''; }}>
                  <Upload size={14} /> Paste / Upload .conf
                </Button>
                {#if draft.privacy.wireguard.configText}<span class="field-hint-inline">A configuration is already saved. Import a new one to replace it.</span>{/if}
              </div>
            {:else}
              <textarea id="wg-import" class="settings-import-textarea" bind:value={wireguardImportText} rows={10}
                placeholder={"[Interface]\nPrivateKey = ...\nAddress = 10.x.x.x/32\nDNS = ...\n\n[Peer]\nPublicKey = ...\nAllowedIPs = 0.0.0.0/0, ::/0\nEndpoint = vpn.example.com:51820\nPersistentKeepalive = 25"}></textarea>
              <input type="file" accept=".conf,text/plain" on:change={async (e) => {
                const file = (e.currentTarget as HTMLInputElement).files?.[0];
                if (file) wireguardImportText = await file.text();
              }} />
              <div class="editor-actions" style="margin-top:10px">
                <Button kind="ghost" on:click={() => { wireguardImportOpen = false; wireguardImportText = ''; }}>Cancel</Button>
                <Button kind="primary" on:click={() => {
                  if (!draft || !wireguardImportText.trim()) return;
                  draft.privacy.wireguard.configText = wireguardImportText;
                  wireguardImportOpen = false;
                  wireguardImportText = '';
                  toastSuccess('WireGuard configuration staged — click Save to apply');
                }} disabled={!wireguardImportText.trim()}>Use This Configuration</Button>
              </div>
            {/if}
          </div>
          <div class="field">
            <label class="field-label" for="wg-timeout">Connection Timeout (seconds)</label>
            <input id="wg-timeout" type="number" min="1" bind:value={draft.privacy.wireguard.timeoutSeconds} style="max-width:120px" />
          </div>
        {/if}

        {#if draft.privacy.mode !== 'direct'}
          <div class="divider"></div>
          <div class="field">
            <div class="actions-row" style="gap:10px">
              <Button kind="secondary" on:click={async () => {
                setBusy('privacy-test', true);
                privacyTestResult = null;
                try {
                  const r = await api.testPrivacyConnection({
                    mode: draft!.privacy.mode,
                    socks5: draft!.privacy.socks5,
                    wireguard: { configText: draft!.privacy.wireguard.configText, timeoutSeconds: draft!.privacy.wireguard.timeoutSeconds }
                  });
                  privacyTestResult = r;
                  if (r.ok) toastSuccess('Privacy route reachable');
                  else toastError(r.error ?? 'Privacy route test failed');
                } catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
                finally { setBusy('privacy-test', false); }
              }} disabled={isBusy('privacy-test')}>
                <Wrench size={16} /> Test Connection
              </Button>
              {#if privacyTestResult}
                <StatusPill tone={privacyTestResult.ok ? 'ok' : 'danger'}>{privacyTestResult.ok ? 'Reachable' : 'Unreachable'}</StatusPill>
              {/if}
              {#if privacyStatus}
                <StatusPill tone={privacyStatus.mode === 'direct' ? 'neutral' : privacyStatus.status === 'error' ? 'danger' : 'ok'}>
                  {privacyStatus.mode} — {privacyStatus.status}
                </StatusPill>
              {/if}
            </div>
          </div>
        {/if}

        <div class="divider"></div>
        <!--
          Indexer names must match whatever string the search result reports
          for "indexer" (not a stable ID). Toggled from NZBHydra2's own
          enabled-indexer list when reachable; always also allows adding a
          name manually (NZBHydra2 unreachable, or a name that doesn't match
          exactly). Like every other field on this page, only takes effect
          once Save writes the draft back.
        -->
        <div class="field">
          <div class="field-label">Indexers Excluded From Privacy Routing</div>
          {#if knownIndexerNames.length > 0}
            <div class="flags-grid">
              {#each knownIndexerNames as name}
                <button type="button" class="chip" class:on={(draft.privacy.excludedIndexers ?? []).includes(name)}
                  on:click={() => { if (draft) draft.privacy.excludedIndexers = profileToggle(draft.privacy.excludedIndexers ?? [], name); }}>
                  {name}
                </button>
              {/each}
            </div>
          {:else}
            <p class="field-hint">NZBHydra2's indexer list isn't available (not configured or unreachable) — add names manually below.</p>
          {/if}
          <div class="actions-row" style="margin-top:10px">
            <input type="text" bind:value={manualExcludedIndexer} placeholder="Add an indexer name manually"
              on:keydown={(e) => {
                if (e.key !== 'Enter' || !draft) return;
                e.preventDefault();
                const name = manualExcludedIndexer.trim();
                if (!name) return;
                if (!(draft.privacy.excludedIndexers ?? []).includes(name)) {
                  draft.privacy.excludedIndexers = [...(draft.privacy.excludedIndexers ?? []), name];
                }
                manualExcludedIndexer = '';
              }} />
            <Button kind="ghost" on:click={() => {
              if (!draft) return;
              const name = manualExcludedIndexer.trim();
              if (!name) return;
              if (!(draft.privacy.excludedIndexers ?? []).includes(name)) {
                draft.privacy.excludedIndexers = [...(draft.privacy.excludedIndexers ?? []), name];
              }
              manualExcludedIndexer = '';
            }}>Add</Button>
          </div>
          {#if (draft.privacy.excludedIndexers ?? []).some(n => !knownIndexerNames.includes(n))}
            <div class="flags-grid" style="margin-top:8px">
              {#each draft.privacy.excludedIndexers.filter(n => !knownIndexerNames.includes(n)) as name}
                <button type="button" class="chip on"
                  on:click={() => { if (draft) draft.privacy.excludedIndexers = profileToggle(draft.privacy.excludedIndexers ?? [], name); }}>
                  {name} ✕
                </button>
              {/each}
            </div>
          {/if}
          <p class="field-hint">NZB downloads from these indexers always use a direct connection, regardless of the routing mode above — useful for a private/trusted indexer.</p>
        </div>

        <div class="divider"></div>
        <div class="editor-actions">
          <Button kind="primary" on:click={saveSettings} disabled={isBusy('save-settings')}>
            <Save size={15} /> {isBusy('save-settings') ? 'Saving…' : 'Save Privacy Settings'}
          </Button>
        </div>
      </Panel>
      {/if}

    <!-- LOGS -->
    {:else if activeTab === 'logs'}
      <Panel title="Logs" subtitle="Operational events assembled from backend runtime and job state.">
      <div class="log-toolbar">
        <div class="log-search-wrap">
          <Search size={14} />
          <input bind:value={logTerm} placeholder="Search logs, service names, request IDs…" class="log-search-input" />
        </div>
        <Select.Root type="single" value={logLevelFilter} onValueChange={(v) => { logLevelFilter = v; changeLogLevel(); }}>
          <Select.Trigger class="w-auto">{logLevelLabels[logLevelFilter] ?? logLevelFilter}</Select.Trigger>
          <Select.Content>
            <Select.Item value="all">All levels</Select.Item>
            <Select.Item value="info">Info</Select.Item>
            <Select.Item value="warn">Warn</Select.Item>
            <Select.Item value="error">Error</Select.Item>
            <Select.Item value="debug">Debug</Select.Item>
          </Select.Content>
        </Select.Root>
        <Button kind="secondary" on:click={loadLogs} disabled={logLoading}>
          <RefreshCw size={14} /> Refresh
        </Button>
        <a class="log-download-link" href="/api/logs?limit=2000" target="_blank" rel="noreferrer" download>
          <Button kind="secondary">Download</Button>
        </a>
      </div>
      {#if logError}<div class="log-error">Error: {logError}</div>{/if}
      <div class="pager-row">
        <span class="pager-total">{logTotal.toLocaleString()} matching entries</span>
        <Pagination page={logPage} totalPages={logTotalPages} on:change={changeLogPage} />
      </div>
      <div class="log-table-wrap">
        <Table.Root>
          <Table.Header>
            <Table.Row>
              <Table.Head class="log-col-time">Time</Table.Head>
              <Table.Head class="log-col-level">Level</Table.Head>
              <Table.Head class="log-col-service">Service</Table.Head>
              <Table.Head class="log-col-message">Message</Table.Head>
            </Table.Row>
          </Table.Header>
          <Table.Body>
            {#if logLoading && logEntries.length === 0}
              <Table.Row><Table.Cell colspan={4} class="log-empty">Loading…</Table.Cell></Table.Row>
            {:else if filteredLogs.length === 0}
              <Table.Row><Table.Cell colspan={4} class="log-empty">No log entries match the current filter.</Table.Cell></Table.Row>
            {:else}
              {#each filteredLogs as entry, i (i)}
                <Table.Row class="log-row-{entry.level === 'error' ? 'error' : entry.level === 'warn' ? 'warn' : 'default'}">
                  <Table.Cell class="log-col-time mono muted">{fmtLogDate(entry.time)}</Table.Cell>
                  <Table.Cell class="log-col-level">
                    <span class="log-badge log-badge-{entry.level || 'default'}">{(entry.level || '?').toUpperCase()}</span>
                  </Table.Cell>
                  <Table.Cell class="log-col-service mono muted">{entry.service || '—'}</Table.Cell>
                  <Table.Cell class="log-col-message">{entry.message}</Table.Cell>
                </Table.Row>
              {/each}
            {/if}
          </Table.Body>
        </Table.Root>
      </div>
      <div class="pager-row">
        <Pagination page={logPage} totalPages={logTotalPages} on:change={changeLogPage} />
      </div>
      </Panel>

    <!-- TASKS -->
    {:else if activeTab === 'tasks'}
      <div class="task-summary-grid">
        <div class="task-summary-card"><div class="task-summary-value">{taskDefs.length}</div><div class="task-summary-label">Scheduled tasks</div></div>
        <div class="task-summary-card"><div class="task-summary-value">{taskRunningCount}</div><div class="task-summary-label">Running now</div></div>
      </div>
      <Panel title="Scheduled Tasks" subtitle="Scheduled-job control plane for indexing, publishing, and maintenance work.">
        <div class="task-table-wrap">
          <Table.Root>
            <Table.Header>
              <Table.Row>
                <Table.Head>Name</Table.Head>
                <Table.Head>Interval</Table.Head>
                <Table.Head>Status</Table.Head>
                <Table.Head>Last Execution</Table.Head>
                <Table.Head>Action</Table.Head>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {#each taskGroups as group}
                {@const groupCollapsed = collapsedTaskGroups.has(group)}
                <Table.Row class="task-group-row" onclick={() => toggleTaskGroup(group)}>
                  <Table.Cell colspan={5}>
                    <span class="task-group-toggle">
                      <svelte:component this={groupCollapsed ? ChevronRight : ChevronDown} size={14} />
                      {group}
                      <span class="task-group-count">{taskDefs.filter(t => t.group === group).length}</span>
                    </span>
                  </Table.Cell>
                </Table.Row>
                {#if !groupCollapsed}
                {#each taskDefs.filter(t => t.group === group) as task}
                  {@const busy = taskRunning[task.id]}
                  {@const result = taskResults[task.id]}
                  {@const schedule = taskScheduleFor(task)}
                  <Table.Row>
                    <Table.Cell>
                      <div class="task-row-title">{task.label}</div>
                      <div class="task-row-sub">{task.description}</div>
                      {#if result}
                        <div class="task-result {result.ok ? 'ok' : 'fail'}">
                          <svelte:component this={result.ok ? CheckCircle2 : AlertTriangle} size={12} />
                          <span>{result.detail}</span>
                        </div>
                      {/if}
                    </Table.Cell>
                    <Table.Cell class="muted">{schedule?.interval ?? task.interval}</Table.Cell>
                    <Table.Cell>
                      {#if busy}
                        <StatusPill tone="warn">Running</StatusPill>
                      {:else if schedule?.automated}
                        <StatusPill tone="ok">Automated</StatusPill>
                      {:else if result?.ok}
                        <StatusPill tone="ok">Success</StatusPill>
                      {:else if result && !result.ok}
                        <StatusPill tone="danger">Failed</StatusPill>
                      {:else}
                        <StatusPill tone="neutral">Idle</StatusPill>
                      {/if}
                    </Table.Cell>
                    <Table.Cell class="muted">
                      {#if result}
                        <span class="time-cell"><Clock3 size={12} /> {fmtTaskTime(result.ranAt)}</span>
                      {:else if schedule?.lastRunAt}
                        <span class="time-cell"><Clock3 size={12} /> {fmtTaskTime(schedule.lastRunAt)}</span>
                      {:else if taskSchedulesLoading}
                        <span class="time-cell dim">—</span>
                      {:else}
                        <span class="time-cell dim">Never</span>
                      {/if}
                    </Table.Cell>
                    <Table.Cell>
                      <Button kind="secondary" on:click={() => runTask(task)} disabled={busy || !task.manual}>
                        {#if busy}<RefreshCw size={14} class="animate-spin" /> Running…{:else}<Play size={14} /> Run{/if}
                      </Button>
                    </Table.Cell>
                  </Table.Row>
                {/each}
                {/if}
              {/each}
            </Table.Body>
          </Table.Root>
        </div>
      </Panel>

    <!-- PLEX -->
    {:else if activeTab === 'media-players'}
      {#if draft}
        <Panel title="Plex Media Server" subtitle="Drakkar triggers a library scan automatically after publishing new media.">
          <div class="form-grid">
            <label class="form-field">
              <span>Server URL</span>
              <input type="url" bind:value={draft.plex.url} placeholder="http://your-plex-server:32400" />
            </label>
            <label class="form-field">
              <span>X-Plex-Token</span>
              <div class="plex-token-row">
                <input type="password" bind:value={draft.plex.token} placeholder="••••••••" autocomplete="off" />
                {#if !plexPin}
                  <Button kind="secondary" on:click={startPlexOAuth}>
                    <ExternalLink size={14} /> Get token with Plex
                  </Button>
                {:else}
                  <div class="plex-oauth-status">
                    <a href={plexPin.authUrl} target="_blank" rel="noopener noreferrer" class="plex-open-link">
                      Open PIN {plexPin.code}
                    </a>
                    <span class="plex-oauth-hint">Waiting for approval…</span>
                    <button type="button" class="plex-cancel-btn" on:click={cancelPlexOAuth}>Cancel</button>
                  </div>
                {/if}
              </div>
            </label>
            <label class="form-field">
              <span>Section Key <small class="field-hint-inline">(leave empty to refresh all libraries)</small></span>
              <input type="text" bind:value={draft.plex.sectionKey} placeholder="1" />
            </label>
          </div>
          <div class="actions-row" style="margin-top:16px; gap:10px; justify-content:space-between">
            <div style="display:flex;gap:10px">
              <Button kind="secondary" on:click={async () => {
                setBusy('plex-test', true);
                try {
                  const r = await api.plexTest();
                  if (r.ok) toastSuccess(`Plex connected: ${r.serverName} (${r.libraries?.length ?? 0} libraries)`);
                  else toastError(r.error ?? 'Plex connection failed');
                } catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
                finally { setBusy('plex-test', false); }
              }} disabled={isBusy('plex-test')}>
                <Wrench size={16} /> Test Connection
              </Button>
              <Button kind="secondary" on:click={async () => {
                setBusy('plex-refresh', true);
                try { await api.plexRefresh(); toastSuccess('Plex library scan triggered'); }
                catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
                finally { setBusy('plex-refresh', false); }
              }} disabled={isBusy('plex-refresh')}>
                <RefreshCw size={16} /> Refresh Libraries
              </Button>
            </div>
            <Button kind="primary" on:click={saveSettings} disabled={isBusy('save-settings')}>
              <Save size={16} /> Save Plex Settings
            </Button>
          </div>
        </Panel>

        <Panel title="Jellyfin" subtitle="Drakkar triggers a library scan automatically after publishing new media.">
          <div class="form-grid">
            <label class="form-field">
              <span>Server URL</span>
              <input type="url" bind:value={draft.jellyfin.url} placeholder="http://your-jellyfin-server:8096" />
            </label>
            <label class="form-field">
              <span>API Key</span>
              <input type="password" bind:value={draft.jellyfin.apiKey} placeholder="••••••••" autocomplete="off" />
            </label>
          </div>
          <div class="actions-row" style="margin-top:16px; gap:10px; justify-content:space-between">
            <div style="display:flex;gap:10px">
              <Button kind="secondary" on:click={async () => {
                setBusy('jellyfin-test', true);
                try {
                  const r = await api.jellyfinTest();
                  if (r.ok) toastSuccess(`Jellyfin connected: ${r.serverName} v${r.version}`);
                  else toastError(r.error ?? 'Jellyfin connection failed');
                } catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
                finally { setBusy('jellyfin-test', false); }
              }} disabled={isBusy('jellyfin-test')}>
                <Wrench size={16} /> Test Connection
              </Button>
              <Button kind="secondary" on:click={async () => {
                setBusy('jellyfin-refresh', true);
                try { await api.jellyfinRefresh(); toastSuccess('Jellyfin library scan triggered'); }
                catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
                finally { setBusy('jellyfin-refresh', false); }
              }} disabled={isBusy('jellyfin-refresh')}>
                <RefreshCw size={16} /> Refresh Libraries
              </Button>
            </div>
            <Button kind="primary" on:click={saveSettings} disabled={isBusy('save-settings')}>
              <Save size={16} /> Save Jellyfin Settings
            </Button>
          </div>
        </Panel>
      {:else}
        <div class="empty">Loading settings…</div>
      {/if}

    <!-- SPEED TEST -->
    {:else if activeTab === 'speed-test'}
      <div class="grid-2">
        <Panel title="Internal Streaming Speed Test" subtitle="Streams the largest already-downloaded file through the same read path playback uses, for ~8 seconds, and reports throughput.">
          <p class="muted">
            Use this to find the best <strong>Max Download Connections</strong> value (Providers tab): run the test, adjust the setting and Save, and run it again — stop once the reported Mbps plateaus.
          </p>
          <Button kind="primary" on:click={runSpeedTest} disabled={isBusy('speedtest')}>
            <Gauge size={16} /> {isBusy('speedtest') ? 'Testing… (~8s)' : 'Run Speed Test'}
          </Button>
          {#if draft}
            <div class="kv-list" style="margin-top: 14px;">
              <div><span>Current Max Download Connections</span><strong>{draft.usenet.maxDownloadConnections}</strong></div>
            </div>
          {/if}
        </Panel>

        <Panel title="Results" subtitle="Most recent runs, newest first.">
          {#if speedTestResults.length === 0}
            <div class="empty">No speed tests run yet.</div>
          {:else}
            <div class="kv-list">
              {#each speedTestResults as r, i}
                <div>
                  <span>{r.fileName}{#if i === 0} <StatusPill tone="ok">latest</StatusPill>{/if}</span>
                  <strong>{r.throughputMbps.toFixed(0)} Mbps · {r.cpuPercent.toFixed(0)}% CPU · {bytes(r.bytesRead)} in {r.durationSeconds.toFixed(1)}s</strong>
                </div>
              {/each}
            </div>
          {/if}
        </Panel>
      </div>

    <!-- SYSTEM -->
    {:else if activeTab === 'system'}
      <div class="grid-2">
        <Panel title="Runtime" subtitle="Service configuration from the backend.">
          {#if status}
            <div class="kv-list">
              <div><span>Started</span><strong>{dateTime(status.startedAt)}</strong></div>
              <div><span>FUSE mount</span><strong>{status.fuseMountPath}</strong></div>
              <div><span>Disk cache limit</span><strong>{bytes(status.diskCacheLimitBytes)}</strong></div>
              <div><span>Read-ahead limit</span><strong>{bytes(status.readAheadLimitBytes)}</strong></div>
              <div><span>Hot cache</span><strong>{bytes(status.memoryHotCacheBytes)}</strong></div>
              <div><span>Queue depth</span><strong>{status.backgroundQueueDepth}</strong></div>
              <div>
                <span>Privacy routing</span>
                <strong>
                  {#if privacyStatus}
                    {privacyStatus.mode}{privacyStatus.mode !== 'direct' ? ` · ${privacyStatus.status}` : ''}
                  {:else}
                    —
                  {/if}
                </strong>
              </div>
            </div>
          {:else}
            <div class="empty">Loading runtime…</div>
          {/if}
        </Panel>

        <Panel title="Integration Status" subtitle="Config-derived readiness for external services.">
          {#if status}
            <div class="int-list">
              {#each integrationEntries as [name, value]}
                <div class="int-row">
                  <div class="int-info">
                    <strong>{name}</strong>
                    <span>{value.detail || 'no detail'}</span>
                  </div>
                  <StatusPill tone={value.configured ? 'ok' : value.enabled ? 'warn' : 'neutral'}>
                    {value.configured ? 'configured' : value.enabled ? 'incomplete' : 'disabled'}
                  </StatusPill>
                </div>
              {/each}
              {#each subtitleProviderEntries as [name, value]}
                <div class="int-row nested">
                  <div class="int-info">
                    <strong>subtitle: {name}</strong>
                    <span>{value.detail || 'no detail'}</span>
                  </div>
                  <StatusPill tone={value.configured ? 'ok' : value.enabled ? 'warn' : 'neutral'}>
                    {value.configured ? 'configured' : value.enabled ? 'incomplete' : 'disabled'}
                  </StatusPill>
                </div>
              {/each}
            </div>
          {:else}
            <div class="empty">Loading…</div>
          {/if}
        </Panel>

        <BackupRestorePanel />
      </div>
    {/if}

  </div>
</div>

<style>

  /* shell */
  .settings-shell {
    display: grid;
    grid-template-columns: 220px minmax(0, 1fr);
    gap: 18px;
    align-items: start;
  }

  /* sidebar */
  .tab-rail {
    display: grid;
    gap: 2px;
    position: sticky;
    top: 88px;
  }

  /* subtitle provider */
  .sub-provider {
    margin-top: 14px;
    padding: 12px 14px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    border-radius: 12px;
    background: hsl(0 0% 100% / 0.02);
  }
  .sub-provider-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 4px;
  }
  .sub-provider-head strong { font-size: 13px; }

  /* toggle label */
  .toggle-label {
    display: flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    font-size: 13px;
    color: hsl(var(--muted-foreground));
  }
  .toggle-label input[type="checkbox"] { accent-color: hsl(var(--primary)); width: 15px; height: 15px; cursor: pointer; }

  /* provider edit cards */
  .provider-forms { display: grid; gap: 16px; }
  .provider-edit-card {
    padding: 16px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 14px;
    background: hsl(0 0% 100% / 0.03);
  }
  .provider-edit-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 14px;
  }
  .pec-title { display: flex; align-items: center; gap: 10px; }
  .pec-title strong { font-size: 14px; }
  .provider-edit-footer {
    display: flex;
    gap: 20px;
    margin-top: 12px;
    padding-top: 12px;
    border-top: 1px solid hsl(0 0% 100% / 0.06);
  }

  /* icon buttons */
  .icon-btn {
    display: inline-grid;
    place-items: center;
    width: 30px;
    height: 30px;
    border-radius: 8px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: transparent;
    color: hsl(var(--muted-foreground));
    cursor: pointer;
    transition: background 0.1s, color 0.1s;
  }
  .icon-btn.danger:hover { background: hsl(0 72% 51% / 0.15); color: hsl(0 96% 82%); border-color: hsl(0 72% 51% / 0.3); }

  /* add button */
  .add-btn {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 12px;
    padding: 10px 16px;
    border-radius: 10px;
    border: 1px dashed hsl(0 0% 100% / 0.15);
    background: transparent;
    color: hsl(var(--muted-foreground));
    font-size: 13px;
    cursor: pointer;
    width: 100%;
    justify-content: center;
    transition: border-color 0.15s, color 0.15s;
  }
  .add-btn:hover { border-color: hsl(var(--primary) / 0.5); color: hsl(var(--primary)); }

  /* shared */
  .kv-list { display: grid; gap: 12px; }
  .kv-list div { display: flex; justify-content: space-between; align-items: baseline; gap: 12px; padding: 10px 0; border-bottom: 1px solid hsl(0 0% 100% / 0.04); }
  .kv-list div:last-child { border-bottom: none; }
  .kv-list span { color: hsl(var(--muted-foreground)); font-size: 13px; }
  .kv-list strong { font-size: 13px; }

  .int-list { display: grid; gap: 10px; }
  .int-row {
    display: flex;
    justify-content: space-between;
    align-items: start;
    gap: 12px;
    padding: 12px 14px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    border-radius: 14px;
    background: hsl(0 0% 100% / 0.03);
  }
  .int-row.nested { margin-left: 12px; }
  .int-info strong { display: block; font-size: 13px; }
  .int-info span   { display: block; margin-top: 3px; color: hsl(var(--muted-foreground)); font-size: 12px; overflow-wrap: anywhere; }

  /* blocklist */
  .bl-editor {
    display: grid;
    gap: 12px;
    margin-bottom: 18px;
    padding: 14px;
    border-radius: 16px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.03);
  }

  .bl-editor-head {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    align-items: flex-start;
  }

  .bl-editor-head p {
    margin: 4px 0 0;
    color: hsl(var(--muted-foreground));
    font-size: 12px;
  }

  .bl-editor-actions {
    display: flex;
    justify-content: flex-end;
  }

  .bl-stats-row {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin-bottom: 14px;
    align-items: center;
  }

  .bl-stat-chip {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    border-radius: 10px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    font-size: 12px;
  }

  .bl-stat-chip.warn .bl-stat-num { color: hsl(47 100% 77%); }
  .bl-stat-num { font-weight: 700; }
  .bl-stat-lbl { color: hsl(var(--muted-foreground)); }

  .bl-reason-chip {
    display: flex;
    align-items: center;
    gap: 6px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    padding: 4px 8px;
    cursor: pointer;
    font-size: 12px;
    transition: border-color 0.15s, background 0.15s;
  }

  .bl-reason-chip:hover { background: hsl(0 0% 100% / 0.08); }

  .bl-reason-chip.active {
    border-color: hsl(var(--primary) / 0.5);
    background: hsl(var(--primary) / 0.08);
  }

  .bl-reason-count {
    font-weight: 700;
    color: hsl(var(--foreground));
  }

  .bl-pagination {
    display: flex;
    align-items: center;
    gap: 6px;
    justify-content: center;
    padding: 14px 0 4px;
  }

  .bl-stats-text { color: hsl(var(--muted-foreground)); font-size: 12px; white-space: nowrap; }

  .bl-show-more {
    padding: 4px 10px;
    border-radius: 12px;
    border: 1px dashed hsl(0 0% 100% / 0.15);
    background: none;
    color: hsl(var(--muted-foreground));
    font-size: 12px;
    cursor: pointer;
    white-space: nowrap;
  }
  .bl-show-more:hover { color: hsl(var(--foreground)); border-color: hsl(0 0% 100% / 0.3); }

  .bl-filter-active {
    display: flex;
    align-items: center;
    gap: 5px;
    height: 32px;
    padding: 0 10px;
    border-radius: 10px;
    border: 1px solid hsl(var(--primary) / 0.4);
    background: hsl(var(--primary) / 0.1);
    color: hsl(var(--primary));
    font-size: 12px;
    font-family: 'JetBrains Mono', monospace;
    cursor: pointer;
  }

  .bl-filter-active:hover { background: hsl(var(--primary) / 0.15); }

  .bl-toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
    margin-bottom: 14px;
  }

  .bl-search {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: 1;
    min-width: 200px;
    height: 40px;
    padding: 0 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 12px;
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--muted-foreground));
  }

  .bl-search input {
    flex: 1;
    background: transparent;
    border: none;
    outline: none;
    color: hsl(var(--foreground));
    font-size: 13px;
  }

  .bl-reason-select {
    height: 40px;
    padding: 0 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 12px;
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    font-size: 13px;
    cursor: pointer;
  }


  .bl-table-wrap {
    overflow-x: auto;
    border: 1px solid hsl(0 0% 100% / 0.06);
    border-radius: 14px;
  }

  .bl-table { width: 100%; min-width: 560px; }

  :global(.bl-table th) {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.12em;
  }

  :global(.bl-table th.sortable) { cursor: pointer; user-select: none; }
  :global(.bl-table th.sortable:hover) { color: hsl(var(--foreground)); }

  :global(.bl-table td) {
    font-size: 13px;
    vertical-align: middle;
  }

  :global(.bl-key-cell), :global(.bl-context-cell) { min-width: 220px; }
  .bl-key-top, .bl-row-actions {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .bl-key-top { min-width: 0; }
  .bl-key {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: hsl(var(--muted-foreground));
    font-size: 11px;
    cursor: default;
  }
  .bl-context-title {
    font-size: 13px;
    font-weight: 600;
    margin-bottom: 3px;
  }
  :global(.bl-action) { width: 84px; text-align: right; }

  .clear-btn {
    display: inline-grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border-radius: 8px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: transparent;
    color: hsl(var(--muted-foreground));
    cursor: pointer;
  }
  .clear-btn:hover { background: hsl(0 72% 51% / 0.15); color: hsl(0 96% 82%); border-color: hsl(0 72% 51% / 0.3); }

  .reason-badge.neutral {
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--muted-foreground));
  }

  .reason-badge {
    display: inline-block;
    padding: 2px 8px;
    border-radius: 8px;
    font-size: 11px;
    font-family: 'JetBrains Mono', monospace;
    background: hsl(0 0% 100% / 0.06);
    color: hsl(var(--muted-foreground));
  }
  .reason-badge.reason-manual   { background: hsl(271 75% 65% / 0.15); color: hsl(271 75% 82%); }
  .reason-badge.reason-missing  { background: hsl(0 72% 51% / 0.15);  color: hsl(0 96% 82%); }
  .reason-badge.reason-archive  { background: hsl(38 96% 55% / 0.15); color: hsl(38 100% 72%); }

  /* queue rules */
  .queue-rules { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin-bottom: 14px; }
  @media (max-width: 900px) { .queue-rules { grid-template-columns: 1fr; } }
  .rule-row { display: grid; gap: 6px; }
  .rule-label { color: hsl(var(--muted-foreground)); font-size: 13px; }
  .pattern-box {
    width: 100%;
    min-height: 160px; resize: vertical;
    font-family: 'JetBrains Mono', monospace; font-size: 12px;
  }

  .integration-pair { align-items: stretch; }
  .sab-api-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }
  .sab-api-head strong { font-size: 13px; }
  .sab-copy-fields { display: grid; gap: 12px; }
  .sab-copy-field { min-width: 0; }
  .sab-copy-field .webhook-token-label { display: block; margin-bottom: 6px; }
  .sab-copy-input { cursor: text; }
  .sab-path-note {
    margin: 10px 0 0;
    color: hsl(var(--muted-foreground));
    font-size: 12px;
    line-height: 1.55;
  }

  /* seerr webhook */
  .webhook-setup {
    margin-top: 18px;
    padding: 14px 16px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 12px;
    background: hsl(0 0% 100% / 0.03);
  }
  .webhook-setup__header {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: 13px;
    font-weight: 600;
    color: hsl(var(--foreground));
    margin-bottom: 8px;
  }
  .webhook-setup__desc {
    font-size: 12px;
    color: hsl(var(--muted-foreground));
    margin: 0 0 10px;
    line-height: 1.55;
  }
  .webhook-setup__steps {
    font-size: 12px;
    color: hsl(var(--muted-foreground));
    margin: 0 0 12px;
    padding-left: 18px;
    line-height: 1.8;
  }
  .webhook-setup__steps strong { color: hsl(var(--foreground)); }
  .webhook-setup__steps code {
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    background: hsl(0 0% 100% / 0.06);
    border-radius: 4px;
    padding: 1px 5px;
  }
  .webhook-url-row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .webhook-url {
    flex: 1;
    font-family: 'JetBrains Mono', monospace;
    font-size: 12px;
    padding: 8px 12px;
    border-radius: 8px;
    border: 1px solid hsl(0 0% 100% / 0.12);
    background: hsl(0 0% 100% / 0.05);
    color: hsl(var(--foreground));
    user-select: all;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: block;
  }
  .copy-btn {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    border-radius: 8px;
    border: 1px solid hsl(0 0% 100% / 0.12);
    background: hsl(0 0% 100% / 0.06);
    color: hsl(var(--muted-foreground));
    cursor: pointer;
    transition: background 0.15s, color 0.15s;
  }
  .copy-btn:hover { background: hsl(0 0% 100% / 0.1); color: hsl(var(--foreground)); }

  .webhook-token-section {
    margin-top: 14px;
    padding-top: 14px;
    border-top: 1px solid hsl(0 0% 100% / 0.06);
  }

  .webhook-token-label {
    font-size: 12px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.1em;
    color: hsl(var(--muted-foreground));
    margin-bottom: 8px;
  }

  .webhook-token-hint {
    font-size: 12px;
    color: hsl(47 100% 77%);
    margin: 0 0 8px;
  }

  .copy-btn--generate {
    padding: 6px 14px;
    font-size: 13px;
    width: auto;
    height: auto;
    border-radius: 8px;
    cursor: pointer;
  }

  .copy-btn--generate:disabled {
    opacity: 0.5;
    cursor: default;
  }

  /* maintenance */
  .maint-list { display: grid; gap: 10px; margin-bottom: 14px; }
  .maint-note { font-size: 13px; color: hsl(var(--muted-foreground)); margin: 8px 0 0; line-height: 1.5; }
  .result-box {
    margin-top: 14px;
    padding: 12px 14px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    border-radius: 12px;
    background: hsl(0 0% 100% / 0.03);
  }
  .result-box strong { display: block; margin-bottom: 8px; font-size: 13px; }
  .result-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; }
  .result-grid span { color: hsl(var(--muted-foreground)); font-size: 12px; }

  /* profiles */
  .profile-list { display: grid; gap: 10px; }
  .profile-card {
    padding: 12px 14px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    border-radius: 12px;
    background: hsl(0 0% 100% / 0.03);
  }
  .profile-head { display: flex; justify-content: space-between; align-items: start; gap: 12px; margin-bottom: 8px; }
  .profile-meta { display: grid; grid-template-columns: repeat(3, 1fr); gap: 6px; font-size: 11px; color: hsl(var(--muted-foreground)); }

  /* actions */
  .actions-row { display: flex; justify-content: flex-end; margin-top: 14px; }
  .action-link { font-size: 13px; font-weight: 600; color: hsl(var(--primary)); text-decoration: none; }
  .action-link:hover { text-decoration: underline; }

  /* redirect panels */
  .logs-redirect, .tasks-redirect {
    padding: 32px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 16px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    border-radius: 18px;
    background: hsl(0 0% 100% / 0.03);
  }

  /* utils */
  .mono { font-family: 'JetBrains Mono', monospace; }
  .muted { color: hsl(var(--muted-foreground)); }
  .empty { color: hsl(var(--muted-foreground)); padding: 12px 0; }

  /* root folders */
  .root-folders { display: grid; gap: 10px; }
  .root-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 14px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.03);
  }
  .root-path { font-size: 12px; color: hsl(var(--foreground)); }

  /* hardlink/symlink box */
  .hardlink-box {
    display: flex;
    gap: 14px;
    padding: 14px;
    border-radius: 14px;
    border: 1px solid hsl(var(--primary) / 0.2);
    background: hsl(var(--primary) / 0.06);
  }
  .hardlink-icon { font-size: 1.8rem; flex-shrink: 0; }
  .hardlink-box strong { display: block; font-size: 14px; margin-bottom: 6px; }
  .hardlink-box p { margin: 0 0 8px; font-size: 13px; color: hsl(var(--muted-foreground)); line-height: 1.6; }
  .hardlink-box em { color: hsl(var(--primary)); font-style: normal; font-weight: 600; }

  /* naming patterns */
  .naming-section { display: grid; gap: 10px; }
  .naming-row { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; }
  .naming-label { font-size: 12px; color: hsl(var(--muted-foreground)); min-width: 120px; flex-shrink: 0; }
  .naming-token {
    padding: 3px 8px;
    border-radius: 7px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.05);
    font-size: 12px;
    font-family: 'JetBrains Mono', monospace;
    color: hsl(var(--primary));
  }
  .naming-example {
    font-size: 12px;
    color: hsl(var(--muted-foreground));
    padding-left: 130px;
  }

  /* config hint */
  .config-hint {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 14px;
    padding: 10px 14px;
    border-radius: 12px;
    border: 1px solid hsl(38 96% 55% / 0.2);
    background: hsl(38 96% 55% / 0.08);
    color: hsl(38 100% 72%);
    font-size: 12px;
  }

  /* responsive */
  @media (max-width: 1200px) {
    .settings-shell { grid-template-columns: 1fr; }
    .tab-rail { position: static; grid-template-columns: repeat(3, minmax(0, 1fr)); }
  }

  @media (max-width: 900px) {
    .profile-meta, .result-grid { grid-template-columns: 1fr; }
  }

  @media (max-width: 600px) {
    .tab-rail { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .size-row { grid-template-columns: 1fr; }
  }

  /* ── Logs tab ──────────────────────────────────────────────── */
  .log-toolbar {
    display: flex; flex-wrap: wrap; align-items: center; gap: 10px; margin-bottom: 12px;
  }
  .log-search-wrap {
    display: flex; align-items: center; gap: 8px; flex: 1; min-width: 200px;
    height: 40px; padding: 0 14px;
    border: 1px solid hsl(0 0% 100% / 0.08); border-radius: 14px;
    background: hsl(0 0% 100% / 0.04); color: hsl(var(--muted-foreground));
  }
  .log-search-input {
    flex: 1; background: transparent; border: none; outline: none;
    color: hsl(var(--foreground)); font-size: 13px;
  }
  .log-search-input::placeholder { color: hsl(var(--muted-foreground)); }
  .log-download-link { display: contents; }
  .log-error { margin-bottom: 10px; padding: 10px 14px; border-radius: 12px; background: hsl(0 72% 51% / 0.15); color: hsl(0 96% 82%); font-size: 13px; }
  .pager-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin: 10px 0; }
  .pager-total { font-size: 13px; color: hsl(var(--muted-foreground)); }
  .log-table-wrap { overflow-x: auto; border: 1px solid hsl(0 0% 100% / 0.08); border-radius: 18px; background: hsl(var(--background) / 0.6); }
  :global(.log-table-wrap th) { white-space: nowrap; }
  :global(.log-table-wrap td) { vertical-align: top; font-size: 13px; }
  :global(.log-col-time) { width: 140px; } :global(.log-col-level) { width: 72px; } :global(.log-col-service) { width: 160px; } :global(.log-col-message) { min-width: 200px; }
  .log-empty { padding: 32px; text-align: center; color: hsl(var(--muted-foreground)); }
  :global(.log-row-error td) { background: hsl(0 72% 51% / 0.06); }
  :global(.log-row-warn td) { background: hsl(38 96% 55% / 0.06); }
  .log-badge { display: inline-block; padding: 2px 8px; border-radius: 8px; font-size: 10px; font-weight: 700; font-family: 'JetBrains Mono', monospace; letter-spacing: 0.06em; }
  .log-badge-error   { background: hsl(0 72% 51% / 0.2);   color: hsl(0 96% 82%); }
  .log-badge-warn    { background: hsl(38 96% 55% / 0.2);  color: hsl(38 100% 72%); }
  .log-badge-info    { background: hsl(171 82% 55% / 0.15); color: hsl(171 82% 72%); }
  .log-badge-debug   { background: hsl(var(--muted-foreground) / 0.15); color: hsl(var(--muted-foreground)); }
  .log-badge-default { background: hsl(var(--muted-foreground) / 0.15); color: hsl(var(--muted-foreground)); }

  /* ── Tasks tab ─────────────────────────────────────────────── */
  .task-summary-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; margin-bottom: 16px; }
  .task-summary-card { padding: 14px 16px; border: 1px solid hsl(0 0% 100% / 0.06); border-radius: 18px; background: hsl(0 0% 100% / 0.03); }
  .task-summary-value { font-size: 1.8rem; font-weight: 700; line-height: 1; }
  .task-summary-label { margin-top: 6px; color: hsl(var(--muted-foreground)); font-size: 12px; }
  .task-table-wrap { overflow-x: auto; }
  :global(.task-table-wrap td) { vertical-align: top; }
  :global(.task-table-wrap th) { font-size: 11px; text-transform: uppercase; letter-spacing: 0.18em; }
  :global(.task-group-row) { cursor: pointer; user-select: none; }
  :global(.task-group-row td) { padding-top: 20px; font-size: 12px; font-weight: 700; letter-spacing: 0.12em; text-transform: uppercase; color: hsl(var(--primary)); }
  .task-group-toggle { display: inline-flex; align-items: center; gap: 6px; }
  .task-group-count { font-weight: 500; color: hsl(var(--muted-foreground)); letter-spacing: normal; text-transform: none; }
  .task-row-title { font-weight: 600; }
  .task-row-sub { margin-top: 4px; color: hsl(var(--muted-foreground)); font-size: 12px; }
  .task-result { display: inline-flex; align-items: center; gap: 6px; margin-top: 8px; font-size: 12px; font-family: 'JetBrains Mono', monospace; }
  .task-result.ok { color: hsl(141 80% 68%); }
  .task-result.fail { color: hsl(0 96% 82%); }
  .time-cell { display: inline-flex; align-items: center; gap: 6px; color: hsl(var(--muted-foreground)); font-size: 12px; }
  .time-cell.dim { opacity: 0.4; }
  @media (max-width: 900px) { .task-summary-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
  .ordered-list { display: grid; gap: 6px; }
  .ordered-row { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border-radius: 10px; border: 1px solid hsl(0 0% 100% / 0.06); background: hsl(0 0% 100% / 0.03); }
  .rank { min-width: 22px; font-size: 11px; font-weight: 700; font-family: 'JetBrains Mono', monospace; color: hsl(var(--primary)); }
  .ordered-value { flex: 1; font-size: 13px; font-family: 'JetBrains Mono', monospace; }
  .rank-btn { display: grid; place-items: center; width: 26px; height: 26px; border-radius: 7px; border: 1px solid hsl(0 0% 100% / 0.06); background: transparent; color: hsl(var(--muted-foreground)); cursor: pointer; font-size: 12px; }
  .rank-btn:hover { background: hsl(0 0% 100% / 0.08); color: hsl(var(--foreground)); }
  .rank-btn:disabled { opacity: 0.3; cursor: default; }
  .rank-btn.remove:hover { background: hsl(0 72% 51% / 0.15); color: hsl(0 96% 82%); }
  .chip-row { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
  .chip { padding: 5px 12px; border-radius: 10px; border: 1px solid hsl(0 0% 100% / 0.08); background: hsl(0 0% 100% / 0.04); color: hsl(var(--muted-foreground)); font-size: 12px; font-family: 'JetBrains Mono', monospace; cursor: pointer; transition: all 0.12s; }
  .chip.on { background: var(--primary); border-color: var(--primary); color: var(--primary-foreground); }
  .chip.add { border-style: dashed; font-size: 11px; }
  .chip.add:hover, .chip:not(.on):hover { background: hsl(0 0% 100% / 0.08); color: hsl(var(--foreground)); }
  .flags-grid { display: grid; gap: 10px; }
  .flag-row { display: flex; align-items: flex-start; gap: 12px; padding: 12px 14px; border-radius: 12px; border: 1px solid hsl(0 0% 100% / 0.06); background: hsl(0 0% 100% / 0.03); cursor: pointer; }
  .flag-row input[type=checkbox] { width: 16px; height: 16px; flex-shrink: 0; margin-top: 2px; accent-color: hsl(var(--primary)); cursor: pointer; }
  .flag-row strong { display: block; font-size: 13px; margin-bottom: 2px; }
  .flag-row span { display: block; font-size: 12px; color: hsl(var(--muted-foreground)); }
  .wg-summary-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  .wg-summary-grid > div { padding: 10px 14px; border-radius: 12px; border: 1px solid hsl(0 0% 100% / 0.06); background: hsl(0 0% 100% / 0.03); min-width: 0; }
  .wg-summary-grid > div strong { display: block; font-size: 11px; margin-bottom: 4px; color: hsl(var(--muted-foreground)); text-transform: uppercase; letter-spacing: 0.04em; }
  .wg-summary-grid > div span { display: block; font-size: 13px; word-break: break-all; }
  .size-row { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
  .size-row label { display: grid; gap: 6px; }
  .size-row span { font-size: 12px; color: hsl(var(--muted-foreground)); }
  .size-input { width: 100%; font-family: 'JetBrains Mono', monospace; }
  .editor-actions { display: flex; justify-content: flex-end; gap: 10px; margin-top: 6px; }
  .exclude-patterns-input { width: 100%; font-family: 'JetBrains Mono', monospace; resize: vertical; }

  /* ── Quality sub-tabs ───────────────────────────────────────── */
  .quality-sub-tabs { display: flex; gap: 4px; margin-bottom: 16px; padding: 4px; border-radius: 12px; background: hsl(0 0% 100% / 0.04); border: 1px solid hsl(0 0% 100% / 0.06); width: fit-content; }
  .qdef-shell { display: grid; gap: 20px; }
  :global(.qdef-table td) { padding: 6px 12px; }
  .qdef-title { font-size: 13px; min-width: 180px; }
  .qdef-input { width: 90px; font-size: 12px; font-family: 'JetBrains Mono', monospace; }

  /* ── Plex OAuth ─────────────────────────────────────────────── */
  .plex-token-row { display: flex; gap: 10px; align-items: center; }
  .plex-token-row input { flex: 1; }
  .plex-oauth-status { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .plex-open-link { display: inline-flex; align-items: center; gap: 6px; height: 36px; padding: 0 14px; border-radius: 12px; border: 1px solid var(--primary); background: var(--primary); color: var(--primary-foreground); font-size: 13px; font-weight: 600; text-decoration: none; }
  .plex-oauth-hint { font-size: 12px; color: hsl(var(--muted-foreground)); }
  .plex-cancel-btn { height: 36px; padding: 0 12px; border-radius: 12px; border: 1px solid hsl(0 0% 100% / 0.08); background: transparent; color: hsl(var(--muted-foreground)); font-size: 12px; cursor: pointer; }
  .plex-cancel-btn:hover { background: hsl(0 0% 100% / 0.08); }


</style>
