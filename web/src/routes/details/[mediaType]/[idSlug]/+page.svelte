<script lang="ts">
  /**
   * Displays the combined TMDB "discover" info and local library/queue state
   * for a single movie or TV show: season/episode browser, the manual-scrape
   * release picker modal (auto search, manual title search, or direct NZB
   * upload), per-episode subtitle management, quality-profile/monitoring
   * overrides, and grab history.
   *
   * Refetches when the route params or `?title=`/`?year=` query change (see
   * the `activeKey` reactive block below) and otherwise stays live via SSE
   * events rather than polling.
   */
  import { page } from '$app/state';
  import { afterNavigate, goto } from '$app/navigation';
  import Search from '@lucide/svelte/icons/search';
  import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
  import Languages from '@lucide/svelte/icons/languages';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Tv from '@lucide/svelte/icons/tv';
  import Download from '@lucide/svelte/icons/download';
  import X from '@lucide/svelte/icons/x';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import Upload from '@lucide/svelte/icons/upload';
  import Info from '@lucide/svelte/icons/info';
  import Button from '$lib/components/Button.svelte';
  import PosterCard from '$lib/components/PosterCard.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import SubtitlePanel from '$lib/components/SubtitlePanel.svelte';
  import * as Select from '$lib/components/ui/select/index.js';
  import * as Tabs from '$lib/components/ui/tabs/index.js';
  import { Input } from '$lib/components/ui/input/index.js';
  import { api, subscribeEvents } from '$lib/api';
  import { idFromSlug } from '$lib/detailsHref';
  import { toastError, toastSuccess } from '$lib/toast';
  import { bytes as fmtBytes } from '$lib/format';
  import { runAction, confirmed } from '$lib/actions';
  import { onMount } from 'svelte';
  import type { CurrentFile, DiscoverDetails, LibraryDetail, LibraryItem, ManualSearchItem, QualityProfile, ReleaseItem } from '$lib/types';

  let detail: DiscoverDetails | null = null;
  let libraryMatch: LibraryItem | null = null;
  let localDetail: LibraryDetail | null = null;
  let releaseCandidates: ReleaseItem[] = [];
  let profiles: QualityProfile[] = [];
  let activeProfileId: number | null = null;
  // Native <select> elements only respect Svelte's two-way bind:value for
  // keeping the selected <option> in sync -- a plain value={...} attribute
  // (no bind:) sets the DOM attribute, not the select's actual selection
  // state, so the box renders with nothing visibly selected even when
  // activeProfileId correctly holds a real profile id. Confirmed live: the
  // select's selectedIndex was -1 despite a matching <option> existing.
  // These derived string vars are the bind:value targets; on:change still
  // drives the real update (updateQualityProfile / setTVShowMonitoring).
  $: profileSelectValue = activeProfileId == null ? '' : String(activeProfileId);
  $: monitoringSelectValue = localDetail?.monitoringMode ?? 'all';
  // Select.Trigger's children is exactly what's rendered -- there's no
  // automatic "show selected option's text" like a native <select> -- so
  // these derive the visible label from the current bound value.
  $: currentProfileLabel = profileSelectValue
    ? (() => {
        const p = profiles.find((profile) => String(profile.id) === profileSelectValue);
        return p ? `${p.name}${p.isDefault ? ' · default' : ''}` : 'Default profile';
      })()
    : 'Default profile';
  const MONITORING_LABELS: Record<string, string> = {
    all: 'All episodes',
    future: 'Future only',
    missing: 'Missing only',
    recent: 'Recent (30d)',
    pilot: 'Pilot only',
    none: 'None (paused)'
  };
  $: currentMonitoringLabel = MONITORING_LABELS[monitoringSelectValue] ?? 'All episodes';
  let showReleasePicker = false;
  let pickerLabel = '';
  let pickerLibraryItemID: number | null = null;
  let pickerSearching = false;
  let pickerSearchTimeout: ReturnType<typeof setTimeout> | null = null;
  let pickerSearchStartedAt = 0;
  /** Floor so the "Searching…" state/spinner is actually perceivable even
   *  when NZBHydra2 answers in well under a second (confirmed live: some
   *  searches resolved in ~200ms, too fast to notice the indicator at all). */
  const MIN_PICKER_SEARCH_VISIBLE_MS = 700;

  /** Applies `apply` (refresh candidates, clear pickerSearching) no earlier
   *  than MIN_PICKER_SEARCH_VISIBLE_MS after the search started. */
  function finishPickerSearch(apply: () => void) {
    const elapsed = Date.now() - pickerSearchStartedAt;
    const remaining = MIN_PICKER_SEARCH_VISIBLE_MS - elapsed;
    if (remaining > 0) {
      setTimeout(apply, remaining);
    } else {
      apply();
    }
  }
  let manualQuery = '';
  let manualResults: ManualSearchItem[] = [];
  let manualSearching = false;
  let manualImporting = false;
  let pickerTab: 'search' | 'auto' = 'search';
  let manualFilterText = '';
  let autoFilterText = '';
  let uploadingNzb = false;
  $: filteredManualResults = manualFilterText.trim()
    ? manualResults.filter((item) => item.title.toLowerCase().includes(manualFilterText.trim().toLowerCase()))
    : manualResults;
  $: filteredAutoCandidates = autoFilterText.trim()
    ? releaseCandidates.filter((c) => c.title.toLowerCase().includes(autoFilterText.trim().toLowerCase()))
    : releaseCandidates;
  let loading = true;
  // Per-action busy tracking, keyed by what the action targets (e.g.
  // `reset-42`, `ep-subtitle-download-7-13`). Replaces a single shared
  // `working` boolean that used to gate every button on the page — with
  // that flag, clicking "Search" on one episode disabled the "Get subtitle"
  // button on a completely different episode (or the season/top-level
  // actions) for the duration of the unrelated request.
  let busy: Record<string, boolean> = {};
  function isBusy(key: string): boolean {
    return !!busy[key];
  }
  function setBusy(key: string, value: boolean) {
    busy = { ...busy, [key]: value };
  }
  let activeKey = '';

  let episodeInfoOpen = false;
  let episodeInfoLoading = false;
  let episodeInfoLabel = '';
  let episodeInfoData: CurrentFile | null = null;
  let subtitleModalOpen = false;
  let subtitleModalEpisodeId: number | null = null;
  let subtitleModalLabel = '';
  // Guards against a slower earlier navigation's response landing after a
  // faster later one's and overwriting the newer page's data (e.g. quickly
  // clicking through several poster cards in a row).
  let loadToken = 0;


  /** Extracts display tags (resolution, source, codec, HDR/DV) from a release title via pattern matching, in a fixed priority order with duplicates dropped. */
  function qualityTags(title: string): string[] {
    const rules: [RegExp, string][] = [
      [/\b2160p\b/i, '2160p'], [/\b4k\b/i, '4K'], [/\b1080p\b/i, '1080p'],
      [/\b720p\b/i, '720p'], [/\b480p\b/i, '480p'],
      [/bluray|bdremux|bdrip/i, 'BluRay'], [/\bweb[- ]?dl\b/i, 'WEB-DL'],
      [/\bwebrip\b/i, 'WEBRip'], [/hevc|x265|h\.265/i, 'x265'],
      [/\bx264\b|h\.264/i, 'x264'], [/\bhdr\b/i, 'HDR'],
      [/dolby.?vision|\bDV\b/, 'DV'],
    ];
    const seen = new Set<string>();
    const out: string[] = [];
    for (const [re, label] of rules) {
      if (re.test(title) && !seen.has(label)) { seen.add(label); out.push(label); }
    }
    return out;
  }

  function badgeTone(tag: string): 'res-2160' | 'res-1080' | 'res-720' | 'default' {
    const t = tag.toLowerCase();
    if (t.includes('2160') || t.includes('4k')) return 'res-2160';
    if (t.includes('1080')) return 'res-1080';
    if (t.includes('720')) return 'res-720';
    return 'default';
  }
  // Exact riven-frontend "darkmatter" theme tokens (its default theme —
  // src/routes/(protected)/+layout.svelte: ModeWatcher defaultTheme="darkmatter")
  // reproduced verbatim so the manual-scrape modal matches pixel-for-pixel.

  /** Lowercases, strips apostrophes/punctuation, and collapses whitespace so titles can be compared regardless of formatting. */
  function normalizeTitle(value: string) {
    return value.toLowerCase().replace(/[''']/g, '').replace(/[^a-z0-9]+/g, ' ').trim();
  }

  type ParsedExplanation = { text: string; delta: number | null; isReject: boolean };
  /** Splits a scoring-explanation line (e.g. "Preferred word (+10)") into its text, trailing signed score delta, and whether it represents an outright rejection. */
  function parseExplanation(line: string): ParsedExplanation {
    const isReject = line.startsWith('Rejected:') || line.startsWith('Rejected by');
    const m = line.match(/\(([+-]\d+)\)$/);
    const delta = m ? parseInt(m[1], 10) : null;
    return { text: line, delta, isReject };
  }

  /** Matches a library item to this page's TMDB subject: prefers exact tmdbId/imdbId match, falling back to normalized-title (+ year, if known) comparison. */
  function sameIdentity(item: LibraryItem, mediaType: string, title: string, year?: number, tmdbId?: number, imdbId?: string) {
    const mapped = item.mediaType === 'episode' ? 'tv' : item.mediaType;
    if (mapped !== mediaType) return false;
    if (tmdbId && item.tmdbId === tmdbId) return true;
    if (imdbId && item.imdbId === imdbId) return true;
    return normalizeTitle(item.title) === normalizeTitle(title) && (!!year ? item.year === year : true);
  }

  /**
   * Loads the TMDB discover details for the current route, finds the matching
   * local library item (if any), and — only when a match exists — loads the
   * library-specific data (local season/episode state, subtitles, release/grab
   * history, quality profiles). Guarded by `loadToken` (see above).
   */
  async function loadDetail() {
    const token = ++loadToken;
    loading = true;
    try {
      const mediaType = page.params.mediaType === 'tv' ? 'tv' : 'movie';
      const tmdbSlug = idFromSlug(page.params.idSlug);
      const tmdbId = tmdbSlug && /^\d+$/.test(tmdbSlug) ? Number(tmdbSlug) : undefined;
      const imdbId = tmdbSlug && /^tt/i.test(tmdbSlug) ? tmdbSlug : undefined;
      const title = page.url.searchParams.get('title') ?? undefined;
      const year = page.url.searchParams.get('year') ? Number(page.url.searchParams.get('year')) : undefined;
      const [discover, library] = await Promise.all([
        api.discoverDetails(mediaType, { title, year, tmdbId, imdbId }),
        api.librarySearch(title ?? imdbId ?? tmdbSlug ?? '')
      ]);
      if (token !== loadToken) return;
      detail = discover;
      libraryMatch = library.items.find((item) => sameIdentity(item, mediaType, discover.title, discover.year, discover.tmdbId, discover.imdbId)) ?? null;
      if (libraryMatch) {
        const [detailResult, profilesResult, activeProfileResult] = await Promise.all([
          api.libraryDetail(libraryMatch.id),
          api.listProfiles().catch(() => ({ profiles: [] })),
          api.getLibraryProfile(libraryMatch.id).catch(() => ({ profile: null }))
        ]);
        if (token !== loadToken) return;
        localDetail = detailResult;
        profiles = profilesResult.profiles ?? [];
        activeProfileId = activeProfileResult.profile?.id ?? null;
      } else {
        localDetail = null;
        profiles = [];
        activeProfileId = null;
      }
    } catch (error) {
      if (token !== loadToken) return;
      toastError(error instanceof Error ? error.message : String(error));
      detail = null;
      libraryMatch = null;
      localDetail = null;
      profiles = [];
      activeProfileId = null;
    } finally {
      if (token === loadToken) loading = false;
    }
  }

  // SvelteKit reuses this component across navigations to sibling routes
  // (e.g. clicking a Recommendations/Similar poster on this same page), so
  // route params/query alone won't rerun on:mount logic. Reading
  // page.params/page.url from a $: block does not reliably re-run on
  // client-side navigation in this app -- confirmed root cause elsewhere
  // this session (+layout.svelte, AppShell.svelte): the URL bar updates
  // (SvelteKit's own router did navigate) but the page's own reactive
  // block reading `page` never re-fires, so the content silently never
  // changes. afterNavigate + a plain reassignment, plus an eager
  // synchronous read for the very first load (afterNavigate does not fire
  // for that one), is the reliable alternative used here and in those
  // other two files.
  activeKey = `${page.params.mediaType}:${page.params.idSlug}:${page.url.search}`;
  void loadDetail();
  afterNavigate((nav) => {
    const params = nav.to?.params;
    const url = nav.to?.url;
    if (!params?.mediaType || !params?.idSlug || !url) return;
    const nextKey = `${params.mediaType}:${params.idSlug}:${url.search}`;
    if (nextKey !== activeKey) {
      activeKey = nextKey;
      void loadDetail();
    }
  });

  onMount(() => {
    // Keeps the page's data live via targeted SSE events instead of polling.
    return subscribeEvents((event) => {
      if (!event) return;
      if (event.kind === 'library.replacements' && event.libraryItemId === pickerLibraryItemID) {
        // Background search completed — refresh candidates and clear searching indicator.
        if (pickerSearchTimeout) {
          clearTimeout(pickerSearchTimeout);
          pickerSearchTimeout = null;
        }
        api.releases(pickerLibraryItemID as number).then((r) => {
          const sorted = (r.items ?? []).sort((a, b) => b.score - a.score);
          finishPickerSearch(() => {
            releaseCandidates = sorted;
            pickerSearching = false;
          });
        }).catch(() => finishPickerSearch(() => { pickerSearching = false; }));
      }
      // Subtitle refresh (both show-level and per-episode) is handled by
      // each SubtitlePanel instance's own SSE subscription now.
      if (event.kind === 'tv.prioritize_missing' && event.tvShowId === localDetail?.tvShowId) {
        toastSuccess(`Prioritized show — queued ${event.queued ?? 0}, created ${event.itemsCreated ?? 0}`);
        void loadDetail();
      }
    });
  });

  function openReleasePicker(libraryItemID: number, label: string) {
    // Defaults to the Search tab and does NOT eagerly kick off the automatic
    // background search — that search replaces (deletes+reinserts) every
    // release_candidates row for this item, which was racing with manual
    // imports and causing "release candidate no longer available" errors.
    // Auto Scrape now always fetches live on demand (see selectPickerTab).
    releaseCandidates = [];
    manualQuery = '';
    manualResults = [];
    manualFilterText = '';
    autoFilterText = '';
    pickerTab = 'search';
    pickerLabel = label;
    pickerLibraryItemID = libraryItemID;
    pickerSearching = false;
    showReleasePicker = true;
  }

  async function selectPickerTab(tab: 'search' | 'auto') {
    pickerTab = tab;
    if (tab !== 'auto' || !pickerLibraryItemID) return;
    // Read-only fetch of whatever candidates already exist — do NOT trigger
    // a new search here. api.replacementCandidates (POST) deletes and
    // reinserts every candidate row with fresh IDs; calling it just to
    // *view* the tab meant the list you were looking at could be replaced
    // out from under you before you clicked anything, causing "release
    // candidate no longer available" on every single click. Searching again
    // is now an explicit action (see searchAgain below).
    pickerSearching = true;
    try {
      const result = await api.releases(pickerLibraryItemID);
      releaseCandidates = (result.items ?? []).sort((a, b) => b.score - a.score);
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      pickerSearching = false;
    }
  }

  /**
   * Explicitly triggers a fresh indexer search for the Auto Scrape tab
   * (unlike selectPickerTab, which only reads existing candidates).
   *
   * The POST here returns immediately with whatever candidates existed
   * *before* this search (see the backend handler's own comment — it
   * starts the real search in a background goroutine and returns the old
   * snapshot right away so the modal doesn't block on a full NZB search
   * round-trip). The real, fresh candidates only arrive later via the
   * 'library.replacements' SSE event (handled in onMount above).
   *
   * pickerSearching is deliberately kept true — and Search Again / Download
   * disabled — until that event actually arrives, not just until this POST
   * resolves. Clearing it early (the previous behavior, via runAction's
   * automatic setWorking) let a user click Search Again again, or click
   * Download on a candidate, while the first search was still replacing
   * (deleting + reinserting) every release_candidates row underneath them —
   * confirmed live as the direct cause of "release candidate no longer
   * available" toasts on Download shortly after Search Again.
   *
   * A safety timeout clears the stuck state if the SSE event never arrives
   * (e.g. a dropped connection), rather than leaving the modal disabled
   * forever.
   */
  async function searchAgain() {
    if (!pickerLibraryItemID) return;
    pickerSearching = true;
    pickerSearchStartedAt = Date.now();
    if (pickerSearchTimeout) clearTimeout(pickerSearchTimeout);
    pickerSearchTimeout = setTimeout(() => {
      pickerSearching = false;
      pickerSearchTimeout = null;
      toastError('Search is taking longer than expected — try again in a moment.');
    }, 25000);
    try {
      await api.replacementCandidates(pickerLibraryItemID);
      toastSuccess('Search queued — results will update shortly');
    } catch (error) {
      if (pickerSearchTimeout) {
        clearTimeout(pickerSearchTimeout);
        pickerSearchTimeout = null;
      }
      pickerSearching = false;
      toastError(error instanceof Error ? error.message : String(error));
    }
  }

  async function runLocalSearch() {
    if (!libraryMatch) return;
    const action = libraryMatch.available ? 'replacement' : 'search';
    const label = `${detail?.title ?? 'this item'} · ${action}`;
    return openReleasePicker(libraryMatch.id, label);
  }

  async function runEpisodeSearch(epLibraryItemId: number, season: number, episode: number, title: string) {
    const label = `S${String(season).padStart(2, '0')}E${String(episode).padStart(2, '0')}${title ? ` · ${title}` : ''}`;
    return openReleasePicker(epLibraryItemId, label);
  }

  async function pickRelease(candidate: ReleaseItem, isRetry = false) {
    if (!isRetry) setBusy('pick-release', true);
    try {
      await api.selectRelease(candidate.releaseCandidateId);
      showReleasePicker = false;
      await loadDetail();
      toastSuccess('Release selected');
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      // A concurrent background search can replace candidate rows (new IDs)
      // between when this list was fetched and when the click landed. If
      // that happens, retry once against the fresh list by matching title —
      // most of the time the same release is still there under a new ID.
      if (!isRetry && message.includes('no longer available') && pickerLibraryItemID) {
        try {
          const fresh = await api.releases(pickerLibraryItemID);
          const match = (fresh.items ?? []).find((item) => item.title === candidate.title);
          if (match) {
            releaseCandidates = fresh.items ?? [];
            await pickRelease(match, true);
            return;
          }
          releaseCandidates = fresh.items ?? [];
        } catch {
          // fall through to showing the original error
        }
      }
      toastError(message);
    } finally {
      if (!isRetry) setBusy('pick-release', false);
    }
  }

  async function runManualSearch() {
    if (!manualQuery.trim() || manualSearching) return;
    manualSearching = true;
    manualResults = [];
    try {
      const result = await api.manualSearch(manualQuery.trim());
      manualResults = (result.items ?? []).sort((a, b) => b.score - a.score);
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      manualSearching = false;
    }
  }

  async function importManualResult(item: ManualSearchItem) {
    if (!pickerLibraryItemID) return;
    await runAction(() => api.manualImportRelease(pickerLibraryItemID!, item), {
      setWorking: (v) => (manualImporting = v),
      successMessage: () => 'Manual release imported',
      afterSuccess: async () => {
        showReleasePicker = false;
        manualQuery = '';
        manualResults = [];
        await loadDetail();
      }
    });
  }

  async function uploadNzbFile(file: File) {
    if (!pickerLibraryItemID) return;
    await runAction(() => api.manualImportUpload(pickerLibraryItemID!, file), {
      setWorking: (v) => (uploadingNzb = v),
      successMessage: () => 'NZB file imported',
      afterSuccess: async () => {
        showReleasePicker = false;
        await loadDetail();
      }
    });
  }

  async function resetItem(targetLibraryItemId: number, label: string) {
    if (!confirm(`Reset "${label}"?\n\nThis removes the symlink and re-queues the item from scratch.`)) return;
    await runAction(() => api.resetLibraryItem(targetLibraryItemId), {
      setWorking: (v) => setBusy(`reset-${targetLibraryItemId}`, v),
      successMessage: () => 'Item reset — re-queued',
      afterSuccess: loadDetail
    });
  }

  async function deleteMedia() {
    if (!libraryMatch) return;
    const isShow = !!localDetail?.tvShowId;
    const scope = isShow ? 'the show, every episode, request, symlink, subtitle, and release record' : 'the movie, request, symlink, subtitle, and release record';
    if (!confirm(`Delete "${detail?.title ?? 'this media'}"?\n\nThis permanently removes ${scope} from Drakkar and cleans matching Seerr/Plex watchlist data.`)) return;
    await runAction(() => api.deleteMedia(libraryMatch!.id), {
      setWorking: (value) => setBusy('delete-media', value),
      successMessage: (result) => result.cleanupPending ? 'Media deleted; external cleanup will retry automatically' : 'Media and related requests deleted',
      afterSuccess: async () => { await goto('/library'); }
    });
  }

  async function runRepublish() {
    if (!libraryMatch) return;
    await runAction(() => api.republishLibrary(libraryMatch!.id), {
      setWorking: (v) => setBusy('republish', v),
      successMessage: () => 'republished library item',
      afterSuccess: loadDetail
    });
  }

  /** Quick shortcut in the header action row -- equivalent to clicking "Search Subtitles" inside the Subtitles panel below. */
  async function runSubtitleSearch() {
    if (!libraryMatch) return;
    await runAction(() => api.searchSubtitles(libraryMatch!.id, []), {
      setWorking: (v) => setBusy(`subtitle-search-${libraryMatch!.id}`, v),
      successMessage: () => 'Searching subtitles in background...'
    });
  }

  async function requestSeason(seasonNumber: number, seasonLabel: string) {
    const tmdbId = detail?.tmdbId;
    if (!tmdbId) return;
    await runAction(() => api.requestMedia(tmdbId, 'tv', [seasonNumber]), {
      setWorking: (v) => setBusy(`season-request-${seasonNumber}`, v),
      successMessage: (result) => (result.queued ? `Requested ${seasonLabel} — finishing up in background` : `Requested ${seasonLabel} — ${result.created} item(s) added`),
      afterSuccess: loadDetail
    });
  }

  async function prioritizeMissingForShow() {
    const tvShowId = localDetail?.tvShowId;
    if (!tvShowId) return;
    await runAction(() => api.prioritizeTVShowMissing(tvShowId), {
      setWorking: (v) => setBusy('prioritize-missing', v),
      successMessage: () => 'Prioritizing missing episodes in background'
    });
  }

  /** Opens the subtitle-management modal for one episode; SubtitlePanel loads its own data on mount. */
  function openSubtitleModal(epLibraryItemId: number, label: string) {
    subtitleModalEpisodeId = epLibraryItemId;
    subtitleModalLabel = label;
    subtitleModalOpen = true;
  }

  function closeSubtitleModal() {
    subtitleModalOpen = false;
    subtitleModalEpisodeId = null;
  }

  /** Formats an episode's air date (YYYY-MM-DD) for display, or '' if absent/invalid. */
  function fmtAirDate(airDate?: string): string {
    if (!airDate) return '';
    const date = new Date(airDate);
    if (Number.isNaN(date.getTime())) return '';
    return date.toLocaleDateString('en-GB', { year: 'numeric', month: 'short', day: '2-digit' });
  }

  /** Fetches and shows the current-file info modal for one episode (or the movie/show item). */
  async function openEpisodeInfo(libraryItemId: number, label: string) {
    episodeInfoOpen = true;
    episodeInfoLoading = true;
    episodeInfoLabel = label;
    episodeInfoData = null;
    try {
      const result = await api.currentFile(libraryItemId);
      episodeInfoData = result.currentFile;
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      episodeInfoLoading = false;
    }
  }

  function closeEpisodeInfo() {
    episodeInfoOpen = false;
    episodeInfoData = null;
  }

  /** Applies a quality-profile override optimistically; on failure, reloads from the server to discard the change and restore the true state. */
  async function updateQualityProfile(nextValue: string) {
    if (!libraryMatch) return;
    const parsedProfileId = nextValue ? Number(nextValue) : null;
    const nextProfileId = parsedProfileId != null && Number.isFinite(parsedProfileId) ? parsedProfileId : null;
    setBusy('quality-profile', true);
    try {
      await api.setLibraryProfile(libraryMatch.id, nextProfileId);
      activeProfileId = nextProfileId;
      toastSuccess(activeProfileId ? 'quality profile updated' : 'quality profile override cleared');
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
      await loadDetail();
    } finally {
      setBusy('quality-profile', false);
    }
  }
</script>

<svelte:head><title>{detail?.title ?? 'Details'} — Drakkar</title></svelte:head>

{#if loading}
  <div class="rounded-[20px] border border-white/[0.06] bg-white/[0.02] p-7 text-center text-muted-foreground">Loading details…</div>
{:else if detail}
  <div class="grid gap-5.5">
    <section class="hero relative overflow-hidden rounded-[28px] border border-white/[0.08]">
      {#if detail.backdropUrl}<img class="hero-backdrop" src={detail.backdropUrl} alt="" />{/if}
      <div class="hero-shade"></div>
      <div class="relative z-10 grid min-h-[420px] grid-cols-[220px_minmax(0,1fr)] items-end gap-6 p-6 max-[980px]:grid-cols-1">
        <div class="aspect-[2/3] overflow-hidden rounded-[20px] border border-white/10 bg-muted max-[980px]:max-w-[220px]">
          {#if detail.posterUrl}
            <img class="h-full w-full object-cover" src={detail.posterUrl} alt="" />
          {:else}
            <div class="grid h-full w-full place-items-center text-muted-foreground"><Tv size={28} /></div>
          {/if}
        </div>
        <div class="grid min-w-0 content-end gap-3 max-[980px]:content-start">
          <div class="flex flex-wrap gap-2.5">
            <StatusPill tone="neutral">{detail.mediaType}</StatusPill>
            {#if detail.year}<StatusPill tone="neutral">{detail.year}</StatusPill>{/if}
            {#if detail.originalLanguage}<StatusPill tone="neutral">{detail.originalLanguage.toUpperCase()}</StatusPill>{/if}
            {#if libraryMatch}
              <StatusPill tone={libraryMatch.available ? 'ok' : libraryMatch.queueState === 'failed' ? 'danger' : 'neutral'}>
                {libraryMatch.available ? 'in library' : libraryMatch.queueState}
              </StatusPill>
            {:else}
              <StatusPill tone="neutral">not in library</StatusPill>
            {/if}
          </div>
          <h1 class="mt-2 text-[clamp(2rem,5vw,3.7rem)] leading-[1.04]">{detail.title}</h1>
          {#if detail.tagline}<div class="mt-2.5 font-bold" style="color: color-mix(in oklch, var(--foreground) 82%, transparent)">{detail.tagline}</div>{/if}
          {#if detail.overview}<p class="max-w-[900px] leading-[1.65]" style="color: color-mix(in oklch, var(--foreground) 80%, transparent)">{detail.overview}</p>{/if}
          <div class="flex flex-wrap items-center gap-2.5 max-[700px]:items-stretch">
            {#if libraryMatch}
              <Button kind="secondary" on:click={runLocalSearch}>
                <Search size={15} />
                {libraryMatch.available ? 'Find Upgrade' : localDetail?.tvShowId ? 'Search Show' : 'Search'}
              </Button>
              {#if localDetail?.tvShowId && (libraryMatch.missingCount ?? 0) > 0}
                <Button kind="secondary" on:click={prioritizeMissingForShow} disabled={isBusy('prioritize-missing')}>
                  <Download size={15} />
                  Prioritize Missing
                </Button>
              {/if}
              <Button kind="secondary" on:click={runSubtitleSearch} disabled={isBusy(`subtitle-search-${libraryMatch.id}`)}>
                <Languages size={15} />
                Subs
              </Button>
              <Button kind="secondary" on:click={runRepublish} disabled={isBusy('republish')}>
                <RotateCcw size={15} />
                Republish
              </Button>
              <Button kind="ghost" on:click={() => resetItem(libraryMatch!.id, detail?.title ?? 'this item')} disabled={isBusy(`reset-${libraryMatch.id}`)}>
                <RotateCcw size={15} />
                Reset
              </Button>
              <Button kind="danger" on:click={deleteMedia} disabled={isBusy('delete-media')}>
                <Trash2 size={15} />
                Delete
              </Button>
            {/if}
            <a class="inline-flex h-8 items-center justify-center rounded-lg bg-secondary px-2.5 text-sm font-medium text-secondary-foreground no-underline transition-colors hover:bg-secondary/80" href="/search">Back To Search</a>
            <Button kind="ghost" on:click={loadDetail} disabled={loading}>
              <RefreshCw size={15} />
              Refresh
            </Button>
          </div>
        </div>
      </div>
    </section>

    <section class="grid grid-cols-[minmax(0,1.7fr)_minmax(300px,0.8fr)] items-start gap-5 max-[980px]:grid-cols-1">
      <div class="grid gap-4.5">
        <section class="rounded-2xl border border-white/[0.08] bg-card/[0.82] p-4.5 min-w-0">
          <h2 class="mb-3.5 text-lg">Details</h2>
          <div class="grid grid-cols-2 gap-3 max-[700px]:grid-cols-1">
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Rating</span><strong>{detail.voteAverage ? detail.voteAverage.toFixed(1) : '—'}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Votes</span><strong>{detail.voteCount || '—'}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Runtime</span><strong>{detail.runtimeMinutes ? `${detail.runtimeMinutes}m` : '—'}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Status</span><strong>{detail.status || '—'}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Language</span><strong>{detail.originalLanguage?.toUpperCase() || '—'}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Companies</span><strong>{detail.productionCompanies?.length || '—'}</strong></div>
          </div>
          {#if detail.genres?.length}
            <div class="mt-4.5 flex flex-wrap gap-2.5">{#each detail.genres as genre}<StatusPill tone="neutral">{genre}</StatusPill>{/each}</div>
          {/if}
        </section>

        {#if localDetail?.mediaType !== 'movie' && localDetail?.seasons?.length}
          <section class="rounded-2xl border border-white/[0.08] bg-card/[0.82] p-4.5 min-w-0">
            <h2 class="mb-3.5 text-lg">Local Seasons</h2>
            <div class="grid gap-3">
              {#each localDetail.seasons as season}
                <details class="overflow-hidden rounded-[18px] border border-white/[0.06] bg-white/[0.02]" open={season.missingCount > 0}>
                  <summary class="grid list-none gap-1.5 p-3.5 cursor-pointer [&::-webkit-details-marker]:hidden">
                    <strong>{season.name}</strong>
                    <div class="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                      {season.availableCount}/{season.episodeCount} available · {season.missingCount} missing
                      {#if season.missingCount > 0 && detail?.tmdbId}
                        <!-- role="button" span, not a real <button>: a <summary> is itself
                             interactive content (toggles the parent <details>), and nesting
                             another interactive element inside it is invalid HTML — some
                             browsers/extensions can mis-time the stopPropagation and trigger
                             both actions. A span can't be interactive content, so this keeps
                             the exact same click behavior/position without the invalid nesting. -->
                        <span
                          class="action-btn"
                          class:opacity-50={isBusy(`season-request-${season.seasonNumber}`)}
                          class:pointer-events-none={isBusy(`season-request-${season.seasonNumber}`)}
                          role="button"
                          tabindex="0"
                          aria-disabled={isBusy(`season-request-${season.seasonNumber}`)}
                          aria-label={`Request ${season.name} in Seerr`}
                          title={`Request ${season.name} in Seerr`}
                          on:click|preventDefault|stopPropagation={() => !isBusy(`season-request-${season.seasonNumber}`) && requestSeason(season.seasonNumber, season.name)}
                          on:keydown|preventDefault|stopPropagation={(e) => { if (!isBusy(`season-request-${season.seasonNumber}`) && (e.key === 'Enter' || e.key === ' ')) requestSeason(season.seasonNumber, season.name); }}
                        >
                          <Download size={11} />
                          Request
                        </span>
                      {/if}
                    </div>
                  </summary>
                  <div class="grid gap-3">
                    {#each season.episodes as episode}
                      <div class="flex flex-wrap items-center justify-between gap-2 border-t border-white/[0.05] px-3.5 py-2.5">
                        <div class="grid min-w-0 flex-1 gap-0.5">
                          <span class="mono text-xs font-bold text-foreground">E{String(episode.episodeNumber).padStart(2, '0')}</span>
                          {#if episode.title}
                            <span class="truncate text-xs text-muted-foreground">{episode.title}</span>
                          {/if}
                          {#if episode.status !== 'available' && fmtAirDate(episode.airDate)}
                            <span class="whitespace-nowrap text-[11px] text-muted-foreground" title="Expected release date">{fmtAirDate(episode.airDate)}</span>
                          {/if}
                        </div>
                        <div class="flex flex-wrap shrink-0 items-center gap-2.5 max-[480px]:basis-full max-[480px]:justify-end">
                          <div class="flex flex-wrap items-center gap-1.5">
                            <StatusPill tone={episode.status === 'available' ? 'ok' : 'neutral'}>{episode.status}</StatusPill>
                            {#if episode.status === 'available'}
                              <StatusPill tone={episode.subtitleLanguages?.length ? 'ok' : 'neutral'}>
                                {episode.subtitleLanguages?.length ? episode.subtitleLanguages.map((l) => l.toUpperCase()).join(', ') : 'No subs'}
                              </StatusPill>
                            {/if}
                          </div>
                          {#if episode.libraryItemId}
                            {@const epId = episode.libraryItemId}
                            {@const epLabel = `S${String(episode.seasonNumber).padStart(2,'0')}E${String(episode.episodeNumber).padStart(2,'0')} ${episode.title}`}
                            <div class="h-4 w-px shrink-0 bg-white/10"></div>
                            <div class="flex items-center gap-1">
                              <button
                                class="action-btn"
                                title="Search releases for this episode (includes season packs)"
                                on:click={() => runEpisodeSearch(epId, episode.seasonNumber, episode.episodeNumber, episode.title)}
                              ><Search size={11} /> Search</button>
                              {#if episode.status === 'available'}
                                <button
                                  class="action-btn"
                                  title="Manage subtitles for this episode"
                                  on:click={() => openSubtitleModal(epId, epLabel)}
                                ><Languages size={11} /> Subs</button>
                                <button
                                  class="action-btn action-btn-icon"
                                  title="Episode file info"
                                  on:click={() => openEpisodeInfo(epId, epLabel)}
                                ><Info size={11} /></button>
                                <button
                                  class="action-btn action-btn-icon action-btn-danger"
                                  title="Reset this episode"
                                  disabled={isBusy(`reset-${epId}`)}
                                  on:click={() => resetItem(epId, epLabel)}
                                ><Trash2 size={11} /></button>
                              {/if}
                            </div>
                          {/if}
                        </div>
                      </div>
                    {/each}
                  </div>
                </details>
              {/each}
            </div>
          </section>
        {/if}

        {#if detail.cast?.length}
          <section class="rounded-2xl border border-white/[0.08] bg-card/[0.82] p-4.5 min-w-0">
            <h2 class="mb-3.5 text-lg">Cast</h2>
            <div class="drag-scroll flex gap-2 overflow-x-auto pb-1">
              {#each detail.cast.slice(0, 8) as person}
                <div class="w-[104px] shrink-0 min-[820px]:w-[calc((100%-3.5rem)/8)]">
                  <div class="grid min-h-full gap-1.5 rounded-xl border border-white/[0.06] bg-white/[0.03] p-2">
                    <div class="aspect-[2/3] overflow-hidden rounded-xl bg-muted">{#if person.profileUrl}<img class="h-full w-full object-cover" src={person.profileUrl} alt="" loading="lazy" />{/if}</div>
                    <strong class="text-sm">{person.name}</strong>
                    <span class="text-xs text-muted-foreground">{person.character || 'cast'}</span>
                  </div>
                </div>
              {/each}
            </div>
          </section>
        {/if}

        {#if detail.recommendations?.length}
          <section class="rounded-2xl border border-white/[0.08] bg-card/[0.82] p-4.5 min-w-0">
            <h2 class="mb-3.5 text-lg">Recommendations</h2>
            <div class="drag-scroll flex gap-2 overflow-x-auto pb-1">
              {#each detail.recommendations.slice(0, 8) as item}
                <div class="w-[104px] shrink-0 min-[820px]:w-[calc((100%-3.5rem)/8)]">
                  <PosterCard item={{ id:0, mediaType:item.mediaType, title:item.title, year:item.year, overview:item.overview, posterUrl:item.posterUrl, backdropUrl:item.backdropUrl, available:false, requestedAt:'', queueState:'requested', failureReason:'', tmdbId:item.tmdbId, imdbId:item.imdbId }} showStatus={false} href={`/details/${item.mediaType === 'tv' ? 'tv' : 'movie'}/${item.tmdbId}-${item.title.toLowerCase().replace(/[^a-z0-9]+/g,'-')}`} compact />
                </div>
              {/each}
            </div>
          </section>
        {/if}

        {#if detail.similar?.length}
          <section class="rounded-2xl border border-white/[0.08] bg-card/[0.82] p-4.5 min-w-0">
            <h2 class="mb-3.5 text-lg">Similar</h2>
            <div class="drag-scroll flex gap-2 overflow-x-auto pb-1">
              {#each detail.similar.slice(0, 8) as item}
                <div class="w-[104px] shrink-0 min-[820px]:w-[calc((100%-3.5rem)/8)]">
                  <PosterCard item={{ id:0, mediaType:item.mediaType, title:item.title, year:item.year, overview:item.overview, posterUrl:item.posterUrl, backdropUrl:item.backdropUrl, available:false, requestedAt:'', queueState:'requested', failureReason:'', tmdbId:item.tmdbId, imdbId:item.imdbId }} showStatus={false} href={`/details/${item.mediaType === 'tv' ? 'tv' : 'movie'}/${item.tmdbId}-${item.title.toLowerCase().replace(/[^a-z0-9]+/g,'-')}`} compact />
                </div>
              {/each}
            </div>
          </section>
        {/if}
      </div>

      <aside class="grid gap-4.5">
        <section class="rounded-2xl border border-white/[0.08] bg-card/[0.82] p-4.5 min-w-0">
          <h2 class="mb-3.5 text-lg">Library State</h2>
          {#if libraryMatch}
            <div class="grid grid-cols-2 gap-3">
              <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Presence</span><strong>{libraryMatch.available ? 'Available' : 'Tracked'}</strong></div>
              <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Queue</span><strong>{libraryMatch.queueState || '—'}</strong></div>
              <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Available</span><strong>{libraryMatch.availableCount ?? 0}</strong></div>
              <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Missing</span><strong>{libraryMatch.missingCount ?? 0}</strong></div>
            </div>
            <div class="mt-3 flex items-center justify-between gap-2.5 border-t border-white/[0.06] pt-3">
              <label for="profile-select" class="whitespace-nowrap text-xs font-semibold text-muted-foreground">Quality Profile</label>
              <Select.Root
                type="single"
                bind:value={profileSelectValue}
                onValueChange={(v) => updateQualityProfile(v)}
              >
                <Select.Trigger id="profile-select" class="flex-1 min-w-0" disabled={isBusy('quality-profile') || profiles.length === 0}>
                  {currentProfileLabel}
                </Select.Trigger>
                <Select.Content>
                  <Select.Item value="">Default profile</Select.Item>
                  {#each profiles as profile}
                    <Select.Item value={String(profile.id)}>{profile.name}{profile.isDefault ? ' · default' : ''}</Select.Item>
                  {/each}
                </Select.Content>
              </Select.Root>
            </div>
            {#if libraryMatch.failureReason}
              <div class="mt-3.5 rounded-[14px] border border-white/[0.06] bg-white/[0.03] px-3.5 py-3 text-[13px] text-muted-foreground">{libraryMatch.failureReason.replaceAll('_', ' ')}</div>
            {/if}
            {#if localDetail?.tvShowId}
              <div class="mt-3 flex items-center justify-between gap-2.5 border-t border-white/[0.06] pt-3">
                <label for="monitoring-select" class="whitespace-nowrap text-xs font-semibold text-muted-foreground">Monitoring</label>
                <Select.Root
                  type="single"
                  bind:value={monitoringSelectValue}
                  onValueChange={async (mode) => {
                    if (!localDetail?.tvShowId) return;
                    try {
                      await api.setTVShowMonitoring(localDetail.tvShowId, mode);
                      localDetail = { ...localDetail, monitoringMode: mode };
                    } catch (err) { toastError(err instanceof Error ? err.message : String(err)); }
                  }}
                >
                  <Select.Trigger id="monitoring-select" class="flex-1 min-w-0">
                    {currentMonitoringLabel}
                  </Select.Trigger>
                  <Select.Content>
                    <Select.Item value="all">All episodes</Select.Item>
                    <Select.Item value="future">Future only</Select.Item>
                    <Select.Item value="missing">Missing only</Select.Item>
                    <Select.Item value="recent">Recent (30d)</Select.Item>
                    <Select.Item value="pilot">Pilot only</Select.Item>
                    <Select.Item value="none">None (paused)</Select.Item>
                  </Select.Content>
                </Select.Root>
              </div>
            {/if}
          {:else}
            <div class="rounded-[14px] border border-white/[0.06] bg-white/[0.03] px-3.5 py-3 text-[13px] text-muted-foreground">No local library item linked yet.</div>
          {/if}
        </section>

        <section class="rounded-2xl border border-white/[0.08] bg-card/[0.82] p-4.5 min-w-0">
          <h2 class="mb-3.5 text-lg">Source</h2>
          <div class="grid grid-cols-2 gap-3">
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">TMDB</span><strong>{detail.tmdbId || '—'}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">IMDb</span><strong>{detail.imdbId || '—'}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Network</span><strong>{detail.network || '—'}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Seasons</span><strong>{detail.numberOfSeasons || '—'}</strong></div>
          </div>
        </section>

        {#if libraryMatch && localDetail?.mediaType === 'movie'}
          {@const lm = libraryMatch}
          <section class="rounded-2xl border border-white/[0.08] bg-card/[0.82] p-4.5 min-w-0">
            <h2 class="mb-3.5 text-lg">Current File</h2>
            {#if localDetail?.currentFile}
              <div class="grid gap-2.5">
                <div class="truncate text-sm font-bold" title={localDetail.currentFile.fileName}>{localDetail.currentFile.fileName}</div>
                <div class="truncate text-xs text-muted-foreground" title={localDetail.currentFile.releaseTitle}>{localDetail.currentFile.releaseTitle}</div>
                <div class="grid grid-cols-2 gap-3">
                  <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Size</span><strong>{fmtBytes(localDetail.currentFile.fileSizeBytes)}</strong></div>
                  <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Indexer</span><strong>{localDetail.currentFile.indexerName || '—'}</strong></div>
                  <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Resolution</span><strong>{localDetail.currentFile.resolution || '—'}</strong></div>
                  <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Score</span><strong>{localDetail.currentFile.score}</strong></div>
                </div>
                <div class="flex items-center gap-2">
                  <div
                    class="inline-flex h-6 items-center rounded-lg px-2.5 text-[11px] font-semibold {localDetail.currentFile.subtitleLanguages?.length ? 'border border-[hsl(142_60%_45%/0.25)] bg-[hsl(142_60%_45%/0.1)] text-[hsl(142_60%_55%)]' : 'border border-white/[0.08] bg-white/[0.04] text-muted-foreground'}"
                  >
                    {localDetail.currentFile.subtitleLanguages?.length ? `Subtitles: ${localDetail.currentFile.subtitleLanguages.map((l) => l.toUpperCase()).join(', ')}` : 'No subtitles'}
                  </div>
                  <button
                    class="action-btn"
                    title="Manage subtitles for this movie"
                    on:click={() => openSubtitleModal(lm.id, detail?.title ?? 'this movie')}
                  ><Languages size={11} /> Subs</button>
                </div>
              </div>
            {:else}
              <div class="rounded-[14px] border border-white/[0.06] bg-white/[0.03] px-3.5 py-3 text-[13px] text-muted-foreground">No file currently selected.</div>
            {/if}
          </section>
        {/if}
      </aside>
    </section>
  </div>
{:else}
  <div class="rounded-[20px] border border-white/[0.06] bg-white/[0.02] p-7 text-center text-muted-foreground">No details found.</div>
{/if}

{#if showReleasePicker}
  <div
    class="fixed inset-0 z-[900] flex items-center justify-center bg-black/50 p-4"
    on:click={(e) => e.target === e.currentTarget && (showReleasePicker = false)}
    on:keydown={(e) => e.key === 'Escape' && (showReleasePicker = false)}
    role="button"
    tabindex="0"
    aria-label="Close release picker"
  >
    <div
      class="flex w-full max-h-[80vh] max-w-[min(calc(100%-2rem),896px)] flex-col gap-4 overflow-hidden rounded-2xl border border-white/10 bg-card p-6 text-foreground shadow-[0_10px_15px_-3px_hsl(0_0%_0%/0.3),0_4px_6px_-4px_hsl(0_0%_0%/0.3)]"
      role="dialog" aria-modal="true" aria-label="Manual scrape" tabindex="-1"
    >
      <div class="flex shrink-0 flex-col gap-2">
        <div class="flex items-center justify-between">
          <h2 class="m-0 text-lg font-semibold leading-none">Manual Scrape</h2>
          <button class="flex size-7 items-center justify-center rounded-md text-foreground opacity-70 transition-opacity hover:opacity-100" on:click={() => (showReleasePicker = false)} aria-label="Close">
            <X size={16} />
          </button>
        </div>
        <div class="text-sm text-muted-foreground">
          {#if pickerTab === 'search'}
            Choose how to find streams for "{pickerLabel || detail?.title || 'this item'}"
          {:else}
            {releaseCandidates.length} candidate{releaseCandidates.length === 1 ? '' : 's'} found
          {/if}
        </div>
      </div>

      <Tabs.Root bind:value={pickerTab} onValueChange={(v) => selectPickerTab(v as 'search' | 'auto')} class="shrink-0">
        <Tabs.List class="w-full grid-cols-2">
          <Tabs.Trigger value="search" class="flex-1">Search</Tabs.Trigger>
          <Tabs.Trigger value="auto" class="flex-1">Auto Scrape</Tabs.Trigger>
        </Tabs.List>
      </Tabs.Root>

      <div class="min-h-0 flex-1 overflow-y-auto">
        {#if pickerTab === 'search'}
          <div class="flex flex-col gap-3.5">
            <form class="flex flex-col gap-1.5" on:submit|preventDefault={runManualSearch}>
              <div class="relative flex items-center">
                <Search size={15} class="pointer-events-none absolute left-2.5 text-muted-foreground" />
                <Input
                  type="text"
                  placeholder="Search (e.g. show name S01 complete)"
                  bind:value={manualQuery}
                  disabled={manualSearching || manualImporting}
                  class="!h-9 pl-8"
                  onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); runManualSearch(); } }}
                />
              </div>
              <Button kind="secondary" type="submit" disabled={manualSearching || manualImporting || !manualQuery.trim()}>
                <Search size={14} />
                {manualSearching ? 'Searching…' : 'Search Streams'}
              </Button>
            </form>

            <label class="flex h-9 items-center gap-2 rounded-md border border-dashed border-white/15 bg-white/[0.02] px-3 text-[13px] text-muted-foreground transition-colors {uploadingNzb ? 'cursor-default opacity-60' : 'cursor-pointer hover:bg-white/5 hover:border-primary/40'}">
              <Upload size={14} />
              {uploadingNzb ? 'Uploading…' : 'Or upload an NZB file directly'}
              <input
                type="file"
                accept=".nzb,application/x-nzb,application/xml,text/xml"
                class="hidden"
                disabled={uploadingNzb}
                on:change={(e) => {
                  const file = (e.currentTarget as HTMLInputElement).files?.[0];
                  (e.currentTarget as HTMLInputElement).value = '';
                  if (file) void uploadNzbFile(file);
                }}
              />
            </label>

            {#if manualResults.length > 0}
              <Input type="text" placeholder="Filter results…" bind:value={manualFilterText} class="!h-8 text-[13px]" />
              <div class="flex flex-col gap-2">
                {#each filteredManualResults as item}
                  {@const tags = [item.resolution, item.source, item.codec, item.audio, item.hdr].filter(Boolean) as string[]}
                  <div class="flex flex-col gap-2 rounded-xl border border-white/[0.08] bg-white/[0.03] p-3 px-4 cursor-pointer transition-colors hover:border-primary/50" on:click={() => importManualResult(item)} role="button" tabindex="0" on:keydown={(e) => e.key === 'Enter' && importManualResult(item)}>
                    <div class="flex items-start justify-between gap-2">
                      <p class="m-0 min-w-0 flex-1 break-all text-[13px] font-semibold leading-[1.4] text-foreground">{item.title}</p>
                      <span class="shrink-0 whitespace-nowrap rounded-full px-2.5 py-0.5 text-[11px] font-bold leading-none {item.score <= 0 ? 'bg-destructive/15 text-destructive' : 'bg-primary/[0.18] text-primary'}">Rank: {item.score}</span>
                    </div>
                    <div class="flex flex-wrap items-center gap-1.5">
                      {#each tags as tag}
                        {@const toned = badgeTone(tag) !== 'default'}
                        <span class="rounded-full px-2 py-0.5 text-[11px] font-semibold whitespace-nowrap {toned ? 'border border-transparent bg-primary/[0.16] text-primary' : 'border border-white/[0.12] font-normal text-muted-foreground'}">{tag}</span>
                      {/each}
                      {#if item.indexer}<span class="rounded-full border border-white/[0.12] px-2 py-0.5 text-[11px] text-muted-foreground whitespace-nowrap">{item.indexer}</span>{/if}
                      <span class="mono rounded-full border border-white/[0.12] px-2 py-0.5 text-[11px] text-muted-foreground whitespace-nowrap">{fmtBytes(item.sizeBytes)}</span>
                    </div>
                    <Button kind="secondary" class="self-start" on:click={(e) => { e.stopPropagation(); importManualResult(item); }} disabled={manualImporting}>
                      <Download size={14} />
                      Import
                    </Button>
                  </div>
                {/each}
              </div>
            {:else if manualSearching}
              <div class="p-9 text-center text-sm text-muted-foreground">Searching streams…</div>
            {/if}
          </div>
        {:else}
          <Button kind="secondary" on:click={searchAgain} disabled={pickerSearching}>
            {#if pickerSearching}<RefreshCw size={14} class="animate-spin" />{:else}<Search size={14} />{/if}
            {pickerSearching ? 'Searching…' : 'Search Again'}
          </Button>
          {#if pickerSearching}
            <div class="mt-2.5 flex items-center gap-2 rounded-[10px] bg-primary/[0.08] px-3.5 py-2.5 text-[13px] text-muted-foreground">
              <RefreshCw size={14} class="animate-spin" />
              Searching indexers — results will refresh automatically…
            </div>
          {/if}
          {#if releaseCandidates.length === 0 && !pickerSearching}
            <div class="p-9 text-center text-sm text-muted-foreground">No candidates yet — click "Search Again" to run an indexer search.</div>
          {:else if releaseCandidates.length > 0}
            <Input type="text" placeholder="Filter results…" bind:value={autoFilterText} class="!h-8 mt-2.5 text-[13px]" />
            <div class="mt-2.5 flex flex-col gap-2">
              {#each filteredAutoCandidates as c}
                {@const tags = qualityTags(c.title)}
                <div class="flex flex-col gap-2 rounded-xl border border-white/[0.08] bg-white/[0.03] p-3 px-4 transition-colors {c.selected ? 'border-primary/40 bg-primary/[0.06]' : ''} {c.rejected && !c.selected ? 'opacity-50' : ''}">
                  <div class="flex items-start justify-between gap-2">
                    <p class="m-0 min-w-0 flex-1 break-all text-[13px] font-semibold leading-[1.4] text-foreground">{c.title}</p>
                    <span class="shrink-0 whitespace-nowrap rounded-full px-2.5 py-0.5 text-[11px] font-bold leading-none {c.rejected && !c.selected ? 'bg-destructive/15 text-destructive' : 'bg-primary/[0.18] text-primary'}">Score: {c.score}</span>
                  </div>
                  <div class="flex flex-wrap items-center gap-1.5">
                    {#each tags as tag}
                      {@const toned = badgeTone(tag) !== 'default'}
                      <span class="rounded-full px-2 py-0.5 text-[11px] font-semibold whitespace-nowrap {toned ? 'border border-transparent bg-primary/[0.16] text-primary' : 'border border-white/[0.12] font-normal text-muted-foreground'}">{tag}</span>
                    {/each}
                    {#if c.indexerName}<span class="rounded-full border border-white/[0.12] px-2 py-0.5 text-[11px] text-muted-foreground whitespace-nowrap">{c.indexerName}</span>{/if}
                    <span class="mono rounded-full border border-white/[0.12] px-2 py-0.5 text-[11px] text-muted-foreground whitespace-nowrap">{fmtBytes(c.sizeBytes)}</span>
                    <span class="mono rounded-full border border-white/[0.12] px-2 py-0.5 text-[11px] text-muted-foreground whitespace-nowrap">cf {c.customFormatScore}</span>
                    {#if c.selected}<span class="rounded-full border border-[hsl(142_70%_45%/0.3)] bg-[hsl(142_70%_45%/0.15)] px-2 py-0.5 text-[11px] font-semibold text-[hsl(142_60%_55%)] whitespace-nowrap">selected</span>{/if}
                    {#if c.rejected && !c.selected}<span class="rounded-full border border-destructive/25 bg-destructive/15 px-2 py-0.5 text-[11px] text-destructive whitespace-nowrap">{c.rejectReason || 'rejected'}</span>{/if}
                    {#if c.failureCount > 0}<span class="rounded-full border border-[hsl(40_90%_50%/0.25)] bg-[hsl(40_90%_50%/0.15)] px-2 py-0.5 text-[11px] font-semibold text-[hsl(40_80%_60%)] whitespace-nowrap">{c.failureCount}× failed</span>{/if}
                  </div>
                  {#if c.compatibilityWarnings && c.compatibilityWarnings.length > 0}
                    <div class="mt-1 flex flex-wrap gap-1">
                      {#each c.compatibilityWarnings as w}
                        <span class="cursor-default rounded-lg border border-[hsl(38_92%_50%/0.3)] bg-[hsl(38_92%_50%/0.15)] px-1.75 py-0.5 text-[10px] font-semibold text-[hsl(38_92%_70%)]" title={w}>⚠ {w.split('—')[0].trim()}</span>
                      {/each}
                    </div>
                  {/if}
                  {#if c.explanations && c.explanations.length > 0}
                    <details class="disclosure-caret mt-1">
                      <summary class="inline-flex select-none items-center gap-1 py-0.5 text-[11px] text-muted-foreground cursor-pointer">Why? ({c.explanations.length} factors)</summary>
                      <div class="grid gap-0.5 pt-1.5">
                        {#each c.explanations as line}
                          {@const ex = parseExplanation(line)}
                          <div class="flex items-baseline gap-1.5 text-[11px] leading-[1.5] {ex.isReject ? 'font-medium text-[hsl(0_72%_62%)]' : ex.delta !== null && ex.delta > 0 ? 'text-[hsl(142_71%_55%/0.9)]' : ex.delta !== null && ex.delta < 0 ? 'text-[hsl(0_72%_62%/0.85)]' : 'text-muted-foreground'}">
                            {#if ex.delta !== null}
                              <span class="mono min-w-9 shrink-0 text-right text-[10px] opacity-90">{ex.delta > 0 ? '+' : ''}{ex.delta}</span>
                            {/if}
                            <span>{ex.text}</span>
                          </div>
                        {/each}
                      </div>
                    </details>
                  {/if}
                  <Button kind={c.selected ? 'primary' : 'secondary'} class="self-start" on:click={() => pickRelease(c)} disabled={isBusy('pick-release') || pickerSearching}>
                    <Download size={14} />
                    {c.selected ? 'Re-grab' : 'Download'}
                  </Button>
                </div>
              {/each}
            </div>
          {/if}
        {/if}
      </div>
    </div>
  </div>
{/if}

{#if episodeInfoOpen}
  <div
    class="fixed inset-0 z-[900] flex items-center justify-center bg-black/50 p-4"
    on:click={(e) => e.target === e.currentTarget && closeEpisodeInfo()}
    on:keydown={(e) => e.key === 'Escape' && closeEpisodeInfo()}
    role="button"
    tabindex="0"
    aria-label="Close file info"
  >
    <div class="grid w-full max-h-[80vh] max-w-[440px] gap-3.5 overflow-y-auto rounded-[20px] border border-white/10 bg-card p-5" role="dialog" aria-modal="true" aria-label="File info" tabindex="-1">
      <div class="flex items-center justify-between">
        <h2 class="m-0 text-base font-semibold">{episodeInfoLabel || 'File Info'}</h2>
        <button class="flex size-7 items-center justify-center rounded-md text-foreground opacity-70 transition-opacity hover:opacity-100" on:click={closeEpisodeInfo} aria-label="Close">
          <X size={16} />
        </button>
      </div>
      {#if episodeInfoLoading}
        <div class="rounded-[14px] border border-white/[0.06] bg-white/[0.03] px-3.5 py-3 text-[13px] text-muted-foreground">Loading…</div>
      {:else if episodeInfoData}
        <div class="grid gap-2.5">
          <div class="truncate text-sm font-bold" title={episodeInfoData.fileName}>{episodeInfoData.fileName}</div>
          <div class="truncate text-xs text-muted-foreground" title={episodeInfoData.releaseTitle}>{episodeInfoData.releaseTitle}</div>
          <div class="grid grid-cols-2 gap-3">
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Size</span><strong>{fmtBytes(episodeInfoData.fileSizeBytes)}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Indexer</span><strong>{episodeInfoData.indexerName || '—'}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Resolution</span><strong>{episodeInfoData.resolution || '—'}</strong></div>
            <div class="grid gap-1 rounded-[14px] border border-white/[0.06] bg-white/[0.03] p-3"><span class="text-xs text-muted-foreground">Score</span><strong>{episodeInfoData.score}</strong></div>
          </div>
          <div class="inline-flex h-6 w-fit items-center rounded-lg px-2.5 text-[11px] font-semibold {episodeInfoData.subtitleLanguages?.length ? 'border border-[hsl(142_60%_45%/0.25)] bg-[hsl(142_60%_45%/0.1)] text-[hsl(142_60%_55%)]' : 'border border-white/[0.08] bg-white/[0.04] text-muted-foreground'}">
            {episodeInfoData.subtitleLanguages?.length ? `Subtitles: ${episodeInfoData.subtitleLanguages.map((l) => l.toUpperCase()).join(', ')}` : 'No subtitles'}
          </div>
        </div>
      {:else}
        <div class="rounded-[14px] border border-white/[0.06] bg-white/[0.03] px-3.5 py-3 text-[13px] text-muted-foreground">No file currently selected.</div>
      {/if}
    </div>
  </div>
{/if}

{#if subtitleModalOpen && subtitleModalEpisodeId}
  <div
    class="fixed inset-0 z-[900] flex items-center justify-center bg-black/50 p-4"
    on:click={(e) => e.target === e.currentTarget && closeSubtitleModal()}
    on:keydown={(e) => e.key === 'Escape' && closeSubtitleModal()}
    role="button"
    tabindex="0"
    aria-label="Close subtitle manager"
  >
    <div class="grid w-full max-h-[80vh] max-w-[440px] gap-3.5 overflow-y-auto rounded-[20px] border border-white/10 bg-card p-5" role="dialog" aria-modal="true" aria-label="Subtitles" tabindex="-1">
      <div class="flex items-center justify-between">
        <h2 class="m-0 text-base font-semibold">Subtitles — {subtitleModalLabel}</h2>
        <button class="flex size-7 items-center justify-center rounded-md text-foreground opacity-70 transition-opacity hover:opacity-100" on:click={closeSubtitleModal} aria-label="Close">
          <X size={16} />
        </button>
      </div>
      <SubtitlePanel libraryItemId={subtitleModalEpisodeId} compact />
    </div>
  </div>
{/if}
