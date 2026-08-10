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

  // SvelteKit reuses this component across navigations to sibling routes, so
  // route params/query alone won't rerun on:mount logic — this reactive block
  // re-triggers loadDetail() whenever the subject (mediaType/idSlug/query)
  // actually changes.
  $: {
    const nextKey = `${page.params.mediaType}:${page.params.idSlug}:${page.url.search}`;
    if (nextKey !== activeKey) {
      activeKey = nextKey;
      void loadDetail();
    }
  }

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
  <div class="empty">Loading details…</div>
{:else if detail}
  <div class="page">
    <section class="hero">
      {#if detail.backdropUrl}<img class="hero-bg" src={detail.backdropUrl} alt="" />{/if}
      <div class="hero-shade"></div>
      <div class="hero-grid">
        <div class="poster">
          {#if detail.posterUrl}
            <img src={detail.posterUrl} alt="" />
          {:else}
            <div class="poster-fallback"><Tv size={28} /></div>
          {/if}
        </div>
        <div class="copy">
          <div class="badge-row">
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
          <h1>{detail.title}</h1>
          {#if detail.tagline}<div class="tagline">{detail.tagline}</div>{/if}
          {#if detail.overview}<p>{detail.overview}</p>{/if}
          <div class="action-row">
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
                <Trash2 size={15} />
                Reset
              </Button>
            {/if}
            <a class="link-btn secondary" href="/search">Back To Search</a>
            <Button kind="ghost" on:click={loadDetail} disabled={loading}>
              <RefreshCw size={15} />
              Refresh
            </Button>
          </div>
        </div>
      </div>
    </section>

    <section class="grid">
      <div class="main">
        <section class="panel stats">
          <h2>Details</h2>
          <div class="stat-grid">
            <div><span>Rating</span><strong>{detail.voteAverage ? detail.voteAverage.toFixed(1) : '—'}</strong></div>
            <div><span>Votes</span><strong>{detail.voteCount || '—'}</strong></div>
            <div><span>Runtime</span><strong>{detail.runtimeMinutes ? `${detail.runtimeMinutes}m` : '—'}</strong></div>
            <div><span>Status</span><strong>{detail.status || '—'}</strong></div>
            <div><span>Language</span><strong>{detail.originalLanguage?.toUpperCase() || '—'}</strong></div>
            <div><span>Companies</span><strong>{detail.productionCompanies?.length || '—'}</strong></div>
          </div>
          {#if detail.genres?.length}
            <div class="chips genre-chips">{#each detail.genres as genre}<StatusPill tone="neutral">{genre}</StatusPill>{/each}</div>
          {/if}
        </section>

        {#if localDetail?.mediaType !== 'movie' && localDetail?.seasons?.length}
          <section class="panel">
            <h2>Local Seasons</h2>
            <div class="season-stack">
              {#each localDetail.seasons as season}
                <details class="season-panel" open={season.missingCount > 0}>
                  <summary>
                    <strong>{season.name}</strong>
                    <div class="summary-meta">
                      {season.availableCount}/{season.episodeCount} available · {season.missingCount} missing
                      {#if season.missingCount > 0 && detail?.tmdbId}
                        <!-- role="button" span, not a real <button>: a <summary> is itself
                             interactive content (toggles the parent <details>), and nesting
                             another interactive element inside it is invalid HTML — some
                             browsers/extensions can mis-time the stopPropagation and trigger
                             both actions. A span can't be interactive content, so this keeps
                             the exact same click behavior/position without the invalid nesting. -->
                        <span
                          class="ep-sub-btn"
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
                  <div class="episode-list">
                    {#each season.episodes as episode}
                      <div class="episode-row">
                        <div class="ep-info">
                          <span class="ep-code">E{String(episode.episodeNumber).padStart(2, '0')}</span>
                          {#if episode.title}
                            <span class="ep-title">{episode.title}</span>
                          {/if}
                          {#if episode.status !== 'available' && fmtAirDate(episode.airDate)}
                            <span class="ep-airdate" title="Expected release date">{fmtAirDate(episode.airDate)}</span>
                          {/if}
                        </div>
                        <div class="ep-right">
                          <StatusPill tone={episode.status === 'available' ? 'ok' : 'neutral'}>{episode.status}</StatusPill>
                          {#if episode.status === 'available'}
                            <span class="ep-subs" class:missing={!episode.subtitleLanguages?.length} title="Subtitle languages available for this episode">
                              {episode.subtitleLanguages?.length ? episode.subtitleLanguages.map((l) => l.toUpperCase()).join(', ') : 'No subs'}
                            </span>
                          {/if}
                          {#if episode.libraryItemId}
                            {@const epId = episode.libraryItemId}
                            {@const epLabel = `S${String(episode.seasonNumber).padStart(2,'0')}E${String(episode.episodeNumber).padStart(2,'0')} ${episode.title}`}
                            <button
                              class="ep-sub-btn"
                              title="Search releases for this episode (includes season packs)"
                              on:click={() => runEpisodeSearch(epId, episode.seasonNumber, episode.episodeNumber, episode.title)}
                            ><Search size={11} /> Search</button>
                            {#if episode.status === 'available'}
                              <button
                                class="ep-sub-btn"
                                title="Manage subtitles for this episode"
                                on:click={() => openSubtitleModal(epId, epLabel)}
                              ><Languages size={11} /> Subs</button>
                              <button
                                class="ep-sub-btn icon-only"
                                title="Episode file info"
                                on:click={() => openEpisodeInfo(epId, epLabel)}
                              ><Info size={11} /></button>
                              <button
                                class="ep-sub-btn ep-reset-btn icon-only"
                                title="Reset this episode"
                                disabled={isBusy(`reset-${epId}`)}
                                on:click={() => resetItem(epId, epLabel)}
                              ><Trash2 size={11} /></button>
                            {/if}
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
          <section class="panel">
            <h2>Cast</h2>
            <div class="drag-scroll media-strip">
              {#each detail.cast.slice(0, 12) as person}
                <div class="person-slot">
                  <div class="person-card">
                    <div class="person-photo">{#if person.profileUrl}<img src={person.profileUrl} alt="" />{/if}</div>
                    <strong>{person.name}</strong>
                    <span>{person.character || 'cast'}</span>
                  </div>
                </div>
              {/each}
            </div>
          </section>
        {/if}

        {#if detail.recommendations?.length}
          <section class="panel">
            <h2>Recommendations</h2>
            <div class="drag-scroll media-strip">
              {#each detail.recommendations as item}
                <div class="poster-slot">
                  <PosterCard item={{ id:0, mediaType:item.mediaType, title:item.title, year:item.year, overview:item.overview, posterUrl:item.posterUrl, backdropUrl:item.backdropUrl, available:false, requestedAt:'', queueState:'requested', failureReason:'', tmdbId:item.tmdbId, imdbId:item.imdbId }} showStatus={false} href={`/details/${item.mediaType === 'tv' ? 'tv' : 'movie'}/${item.tmdbId}-${item.title.toLowerCase().replace(/[^a-z0-9]+/g,'-')}`} compact />
                </div>
              {/each}
            </div>
          </section>
        {/if}

        {#if detail.similar?.length}
          <section class="panel">
            <h2>Similar</h2>
            <div class="drag-scroll media-strip">
              {#each detail.similar as item}
                <div class="poster-slot">
                  <PosterCard item={{ id:0, mediaType:item.mediaType, title:item.title, year:item.year, overview:item.overview, posterUrl:item.posterUrl, backdropUrl:item.backdropUrl, available:false, requestedAt:'', queueState:'requested', failureReason:'', tmdbId:item.tmdbId, imdbId:item.imdbId }} showStatus={false} href={`/details/${item.mediaType === 'tv' ? 'tv' : 'movie'}/${item.tmdbId}-${item.title.toLowerCase().replace(/[^a-z0-9]+/g,'-')}`} compact />
                </div>
              {/each}
            </div>
          </section>
        {/if}
      </div>

      <aside class="side">
        <section class="panel">
          <h2>Library State</h2>
          {#if libraryMatch}
            <div class="kv">
              <div><span>Presence</span><strong>{libraryMatch.available ? 'Available' : 'Tracked'}</strong></div>
              <div><span>Queue</span><strong>{libraryMatch.queueState || '—'}</strong></div>
              <div><span>Available</span><strong>{libraryMatch.availableCount ?? 0}</strong></div>
              <div><span>Missing</span><strong>{libraryMatch.missingCount ?? 0}</strong></div>
            </div>
            <div class="monitoring-row">
              <label for="profile-select">Quality Profile</label>
              <select
                id="profile-select"
                bind:value={profileSelectValue}
                disabled={isBusy('quality-profile') || profiles.length === 0}
                on:change={(e) => updateQualityProfile((e.currentTarget as HTMLSelectElement).value)}
              >
                <option value="">Default profile</option>
                {#each profiles as profile}
                  <option value={String(profile.id)}>{profile.name}{profile.isDefault ? ' · default' : ''}</option>
                {/each}
              </select>
            </div>
            {#if libraryMatch.failureReason}
              <div class="failure-box">{libraryMatch.failureReason.replaceAll('_', ' ')}</div>
            {/if}
            {#if localDetail?.tvShowId}
              <div class="monitoring-row">
                <label for="monitoring-select">Monitoring</label>
                <select
                  id="monitoring-select"
                  bind:value={monitoringSelectValue}
                  on:change={async (e) => {
                    if (!localDetail?.tvShowId) return;
                    const mode = (e.currentTarget as HTMLSelectElement).value;
                    try {
                      await api.setTVShowMonitoring(localDetail.tvShowId, mode);
                      localDetail = { ...localDetail, monitoringMode: mode };
                    } catch (err) { toastError(err instanceof Error ? err.message : String(err)); }
                  }}
                >
                  <option value="all">All episodes</option>
                  <option value="future">Future only</option>
                  <option value="missing">Missing only</option>
                  <option value="recent">Recent (30d)</option>
                  <option value="pilot">Pilot only</option>
                  <option value="none">None (paused)</option>
                </select>
              </div>
            {/if}
          {:else}
            <div class="empty-side">No local library item linked yet.</div>
          {/if}
        </section>

        <section class="panel">
          <h2>Source</h2>
          <div class="kv">
            <div><span>TMDB</span><strong>{detail.tmdbId || '—'}</strong></div>
            <div><span>IMDb</span><strong>{detail.imdbId || '—'}</strong></div>
            <div><span>Network</span><strong>{detail.network || '—'}</strong></div>
            <div><span>Seasons</span><strong>{detail.numberOfSeasons || '—'}</strong></div>
          </div>
        </section>

        {#if libraryMatch && localDetail?.mediaType === 'movie'}
          {@const lm = libraryMatch}
          <section class="panel">
            <h2>Current File</h2>
            {#if localDetail?.currentFile}
              <div class="current-file">
                <div class="cf-name" title={localDetail.currentFile.fileName}>{localDetail.currentFile.fileName}</div>
                <div class="cf-release" title={localDetail.currentFile.releaseTitle}>{localDetail.currentFile.releaseTitle}</div>
                <div class="kv">
                  <div><span>Size</span><strong>{fmtBytes(localDetail.currentFile.fileSizeBytes)}</strong></div>
                  <div><span>Indexer</span><strong>{localDetail.currentFile.indexerName || '—'}</strong></div>
                  <div><span>Resolution</span><strong>{localDetail.currentFile.resolution || '—'}</strong></div>
                  <div><span>Score</span><strong>{localDetail.currentFile.score}</strong></div>
                </div>
                <div class="cf-subs-row">
                  <div class="cf-subs" class:missing={!localDetail.currentFile.subtitleLanguages?.length}>
                    {localDetail.currentFile.subtitleLanguages?.length ? `Subtitles: ${localDetail.currentFile.subtitleLanguages.map((l) => l.toUpperCase()).join(', ')}` : 'No subtitles'}
                  </div>
                  <button
                    class="ep-sub-btn"
                    title="Manage subtitles for this movie"
                    on:click={() => openSubtitleModal(lm.id, detail?.title ?? 'this movie')}
                  ><Languages size={11} /> Subs</button>
                </div>
              </div>
            {:else}
              <div class="empty-side">No file currently selected.</div>
            {/if}
          </section>
        {/if}
      </aside>
    </section>
  </div>
{:else}
  <div class="empty">No details found.</div>
{/if}

{#if showReleasePicker}
  <div
    class="modal-backdrop"
    on:click={(e) => e.target === e.currentTarget && (showReleasePicker = false)}
    on:keydown={(e) => e.key === 'Escape' && (showReleasePicker = false)}
    role="button"
    tabindex="0"
    aria-label="Close release picker"
  >
    <div class="rel-modal" role="dialog" aria-modal="true" aria-label="Manual scrape" tabindex="-1">
      <div class="rel-header">
        <div class="rel-header-top">
          <h2>Manual Scrape</h2>
          <button class="close-btn" on:click={() => (showReleasePicker = false)} aria-label="Close">
            <X size={16} />
          </button>
        </div>
        <div class="rel-header-desc">
          {#if pickerTab === 'search'}
            Choose how to find streams for "{pickerLabel || detail?.title || 'this item'}"
          {:else}
            {releaseCandidates.length} candidate{releaseCandidates.length === 1 ? '' : 's'} found
          {/if}
        </div>
      </div>

      <div class="rel-tabs">
        <button type="button" class:active={pickerTab === 'search'} on:click={() => selectPickerTab('search')}>Search</button>
        <button type="button" class:active={pickerTab === 'auto'} on:click={() => selectPickerTab('auto')}>Auto Scrape</button>
      </div>

      <div class="rel-tab-body">
        {#if pickerTab === 'search'}
          <div class="manual-search-block">
            <form class="manual-search-form" on:submit|preventDefault={runManualSearch}>
              <div class="manual-search-input-wrap">
                <Search size={15} />
                <input
                  class="manual-search-input"
                  type="text"
                  placeholder="Search (e.g. show name S01 complete)"
                  bind:value={manualQuery}
                  disabled={manualSearching || manualImporting}
                  on:keydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); runManualSearch(); } }}
                />
              </div>
              <Button kind="secondary" type="submit" disabled={manualSearching || manualImporting || !manualQuery.trim()}>
                <Search size={14} />
                {manualSearching ? 'Searching…' : 'Search Streams'}
              </Button>
            </form>

            <label class="upload-row" class:disabled={uploadingNzb}>
              <Upload size={14} />
              {uploadingNzb ? 'Uploading…' : 'Or upload an NZB file directly'}
              <input
                type="file"
                accept=".nzb,application/x-nzb,application/xml,text/xml"
                class="upload-input"
                disabled={uploadingNzb}
                on:change={(e) => {
                  const file = (e.currentTarget as HTMLInputElement).files?.[0];
                  (e.currentTarget as HTMLInputElement).value = '';
                  if (file) void uploadNzbFile(file);
                }}
              />
            </label>

            {#if manualResults.length > 0}
              <input
                class="rel-filter-input"
                type="text"
                placeholder="Filter results…"
                bind:value={manualFilterText}
              />
              <div class="rel-list">
                {#each filteredManualResults as item}
                  {@const tags = [item.resolution, item.source, item.codec, item.audio, item.hdr].filter(Boolean) as string[]}
                  <div class="rel-card" on:click={() => importManualResult(item)} role="button" tabindex="0" on:keydown={(e) => e.key === 'Enter' && importManualResult(item)}>
                    <div class="rel-card-top">
                      <p class="rel-card-title">{item.title}</p>
                      <span class="rel-badge" class:rel-badge-neg={item.score <= 0}>Rank: {item.score}</span>
                    </div>
                    <div class="rel-card-badges">
                      {#each tags as tag}
                        <span class={`rel-badge-outline tone-${badgeTone(tag)}`}>{tag}</span>
                      {/each}
                      {#if item.indexer}<span class="rel-badge-outline">{item.indexer}</span>{/if}
                      <span class="rel-badge-outline mono">{fmtBytes(item.sizeBytes)}</span>
                    </div>
                    <Button kind="secondary" on:click={(e) => { e.stopPropagation(); importManualResult(item); }} disabled={manualImporting}>
                      <Download size={14} />
                      Import
                    </Button>
                  </div>
                {/each}
              </div>
            {:else if manualSearching}
              <div class="rel-empty">Searching streams…</div>
            {/if}
          </div>
        {:else}
          <Button kind="secondary" on:click={searchAgain} disabled={pickerSearching}>
            {#if pickerSearching}<RefreshCw size={14} class="spin" />{:else}<Search size={14} />{/if}
            {pickerSearching ? 'Searching…' : 'Search Again'}
          </Button>
          {#if pickerSearching}
            <div class="rel-searching-banner">
              <RefreshCw size={14} class="spin" />
              Searching indexers — results will refresh automatically…
            </div>
          {/if}
          {#if releaseCandidates.length === 0 && !pickerSearching}
            <div class="rel-empty">No candidates yet — click "Search Again" to run an indexer search.</div>
          {:else if releaseCandidates.length > 0}
            <input
              class="rel-filter-input"
              type="text"
              placeholder="Filter results…"
              bind:value={autoFilterText}
            />
            <div class="rel-list">
              {#each filteredAutoCandidates as c}
                {@const tags = qualityTags(c.title)}
                <div class="rel-card" class:rel-selected={c.selected} class:rel-rejected={c.rejected && !c.selected}>
                  <div class="rel-card-top">
                    <p class="rel-card-title">{c.title}</p>
                    <span class="rel-badge" class:rel-badge-neg={c.rejected && !c.selected}>Score: {c.score}</span>
                  </div>
                  <div class="rel-card-badges">
                    {#each tags as tag}
                      <span class={`rel-badge-outline tone-${badgeTone(tag)}`}>{tag}</span>
                    {/each}
                    {#if c.indexerName}<span class="rel-badge-outline">{c.indexerName}</span>{/if}
                    <span class="rel-badge-outline mono">{fmtBytes(c.sizeBytes)}</span>
                    <span class="rel-badge-outline mono">cf {c.customFormatScore}</span>
                    {#if c.selected}<span class="rel-badge-outline rel-pill-ok">selected</span>{/if}
                    {#if c.rejected && !c.selected}<span class="rel-badge-outline rel-pill-danger">{c.rejectReason || 'rejected'}</span>{/if}
                    {#if c.failureCount > 0}<span class="rel-badge-outline rel-pill-warn">{c.failureCount}× failed</span>{/if}
                  </div>
                  {#if c.compatibilityWarnings && c.compatibilityWarnings.length > 0}
                    <div class="compat-warnings">
                      {#each c.compatibilityWarnings as w}
                        <span class="compat-badge" title={w}>⚠ {w.split('—')[0].trim()}</span>
                      {/each}
                    </div>
                  {/if}
                  {#if c.explanations && c.explanations.length > 0}
                    <details class="rel-why">
                      <summary class="rel-why-toggle">Why? ({c.explanations.length} factors)</summary>
                      <div class="rel-explanations">
                        {#each c.explanations as line}
                          {@const ex = parseExplanation(line)}
                          <div class="rel-explanation" class:rel-exp-reject={ex.isReject} class:rel-exp-pos={!ex.isReject && ex.delta !== null && ex.delta > 0} class:rel-exp-neg={!ex.isReject && ex.delta !== null && ex.delta < 0}>
                            {#if ex.delta !== null}
                              <span class="rel-exp-delta">{ex.delta > 0 ? '+' : ''}{ex.delta}</span>
                            {/if}
                            <span>{ex.text}</span>
                          </div>
                        {/each}
                      </div>
                    </details>
                  {/if}
                  <Button kind={c.selected ? 'primary' : 'secondary'} on:click={() => pickRelease(c)} disabled={isBusy('pick-release') || pickerSearching}>
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
    class="modal-backdrop"
    on:click={(e) => e.target === e.currentTarget && closeEpisodeInfo()}
    on:keydown={(e) => e.key === 'Escape' && closeEpisodeInfo()}
    role="button"
    tabindex="0"
    aria-label="Close file info"
  >
    <div class="info-modal" role="dialog" aria-modal="true" aria-label="File info" tabindex="-1">
      <div class="rel-header-top">
        <h2>{episodeInfoLabel || 'File Info'}</h2>
        <button class="close-btn" on:click={closeEpisodeInfo} aria-label="Close">
          <X size={16} />
        </button>
      </div>
      {#if episodeInfoLoading}
        <div class="empty-side">Loading…</div>
      {:else if episodeInfoData}
        <div class="current-file">
          <div class="cf-name" title={episodeInfoData.fileName}>{episodeInfoData.fileName}</div>
          <div class="cf-release" title={episodeInfoData.releaseTitle}>{episodeInfoData.releaseTitle}</div>
          <div class="kv">
            <div><span>Size</span><strong>{fmtBytes(episodeInfoData.fileSizeBytes)}</strong></div>
            <div><span>Indexer</span><strong>{episodeInfoData.indexerName || '—'}</strong></div>
            <div><span>Resolution</span><strong>{episodeInfoData.resolution || '—'}</strong></div>
            <div><span>Score</span><strong>{episodeInfoData.score}</strong></div>
          </div>
          <div class="cf-subs" class:missing={!episodeInfoData.subtitleLanguages?.length}>
            {episodeInfoData.subtitleLanguages?.length ? `Subtitles: ${episodeInfoData.subtitleLanguages.map((l) => l.toUpperCase()).join(', ')}` : 'No subtitles'}
          </div>
        </div>
      {:else}
        <div class="empty-side">No file currently selected.</div>
      {/if}
    </div>
  </div>
{/if}

{#if subtitleModalOpen && subtitleModalEpisodeId}
  <div
    class="modal-backdrop"
    on:click={(e) => e.target === e.currentTarget && closeSubtitleModal()}
    on:keydown={(e) => e.key === 'Escape' && closeSubtitleModal()}
    role="button"
    tabindex="0"
    aria-label="Close subtitle manager"
  >
    <div class="info-modal" role="dialog" aria-modal="true" aria-label="Subtitles" tabindex="-1">
      <div class="rel-header-top">
        <h2>Subtitles — {subtitleModalLabel}</h2>
        <button class="close-btn" on:click={closeSubtitleModal} aria-label="Close">
          <X size={16} />
        </button>
      </div>
      <SubtitlePanel libraryItemId={subtitleModalEpisodeId} compact />
    </div>
  </div>
{/if}

<style>
  .page { display: grid; gap: 22px; }
  .hero {
    position: relative; overflow: hidden; border-radius: 28px;
    border: 1px solid hsl(0 0% 100% / 0.08);
  }
  .hero-bg, .hero-shade { position: absolute; inset: 0; }
  .hero-bg { width: 100%; height: 100%; object-fit: cover; }
  .hero-shade { background: linear-gradient(180deg, hsl(0 0% 0% / 0.2), hsl(0 0% 0% / 0.86)); }
  .hero-grid {
    position: relative; z-index: 1; min-height: 420px;
    display: grid; grid-template-columns: 220px minmax(0,1fr);
    gap: 24px; align-items: end; padding: 24px;
  }
  .poster { aspect-ratio: 2 / 3; overflow: hidden; border-radius: 20px; border: 1px solid hsl(0 0% 100% / 0.1); background: hsl(var(--muted)); }
  .poster img, .person-photo img { width: 100%; height: 100%; object-fit: cover; }
  .poster-fallback, .person-photo { display: grid; place-items: center; width: 100%; height: 100%; color: hsl(var(--muted-foreground)); }
  .copy { min-width: 0; display: grid; gap: 12px; align-content: end; }
  .copy h1 { margin: 8px 0 0; font-size: clamp(2rem, 5vw, 3.7rem); line-height: 1.04; }
  .copy p { max-width: 900px; color: hsl(var(--foreground) / 0.8); line-height: 1.65; }
  .tagline { margin-top: 10px; color: hsl(var(--foreground) / 0.82); font-weight: 700; }
  .badge-row, .action-row, .chips { display: flex; flex-wrap: wrap; gap: 10px; }
  .genre-chips { margin-top: 18px; }
  .action-row { align-items: center; }
  .action-row :global(button) { min-height: 42px; }
  .action-row :global(button),
  .action-row .link-btn { flex: 0 0 auto; }
  .link-btn {
    display: inline-flex; align-items: center; justify-content: center;
    min-height: 42px; padding: 0 14px; border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.08); text-decoration: none;
  }
  .link-btn.secondary {
    background: hsl(0 0% 100% / 0.05); color: hsl(var(--foreground));
  }
  .link-btn.ghost {
    min-height: 28px; padding: 0 10px; font-size: 12px;
    color: hsl(var(--muted-foreground)); border-color: transparent;
  }
  .grid { display: grid; grid-template-columns: minmax(0,1.7fr) minmax(300px,0.8fr); gap: 20px; align-items: start; }
  .main, .side { display: grid; gap: 18px; }
  .panel {
    border-radius: 24px; border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(var(--card) / 0.82); padding: 18px;
    min-width: 0;
  }
  .panel h2 { margin: 0 0 14px; font-size: 18px; }
  .current-file { display: grid; gap: 10px; }
  .cf-name {
    font-weight: 700; font-size: 14px;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .cf-release {
    color: hsl(var(--muted-foreground)); font-size: 12px;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .cf-subs-row { display: flex; align-items: center; gap: 8px; }
  .cf-subs {
    display: inline-flex; align-items: center; box-sizing: border-box; min-height: 24px;
    padding: 0 9px; border-radius: 8px;
    font-size: 11px; font-weight: 600;
    border: 1px solid hsl(142 60% 45% / 0.25); color: hsl(142 60% 55%);
    background: hsl(142 60% 45% / 0.1);
  }
  .cf-subs.missing {
    border-color: hsl(0 0% 100% / 0.08); color: hsl(var(--muted-foreground));
    background: hsl(0 0% 100% / 0.04);
  }
  .stat-grid, .kv { display: grid; grid-template-columns: repeat(2, minmax(0,1fr)); gap: 12px; }
  .stat-grid div, .kv div {
    display: grid; gap: 4px; padding: 12px; border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.06); background: hsl(0 0% 100% / 0.03);
  }
  .failure-box, .empty-side {
    margin-top: 14px;
    padding: 12px 14px;
    border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.03);
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }
  .stat-grid span, .kv span, .summary-meta, .person-card span { color: hsl(var(--muted-foreground)); font-size: 12px; }
  .season-stack, .episode-list { display: grid; gap: 12px; }
  .season-panel { border-radius: 18px; border: 1px solid hsl(0 0% 100% / 0.06); background: hsl(0 0% 100% / 0.02); overflow: hidden; }
  .season-panel summary { list-style: none; cursor: pointer; padding: 14px 16px; display: grid; gap: 6px; }
  .episode-row {
    display: flex; align-items: center; justify-content: space-between; gap: 12px;
    padding: 10px 16px; border-top: 1px solid hsl(0 0% 100% / 0.05);
  }
  .ep-info { flex: 1; min-width: 0; display: grid; gap: 2px; }
  .ep-code {
    font-family: 'JetBrains Mono', monospace; font-size: 12px; font-weight: 700;
    color: hsl(var(--foreground));
  }
  .ep-title {
    font-size: 12px; color: hsl(var(--muted-foreground));
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .ep-right { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
  .ep-airdate {
    font-size: 11px; color: hsl(var(--muted-foreground)); white-space: nowrap;
  }
  .ep-subs {
    display: inline-flex; align-items: center; justify-content: center;
    box-sizing: border-box; min-height: 24px; padding: 0 9px;
    border-radius: 8px; font-size: 11px; font-weight: 600;
    border: 1px solid hsl(142 60% 45% / 0.25); color: hsl(142 60% 55%);
    background: hsl(142 60% 45% / 0.1); white-space: nowrap;
  }
  .ep-subs.missing {
    border-color: hsl(0 0% 100% / 0.08); color: hsl(var(--muted-foreground));
    background: hsl(0 0% 100% / 0.04);
  }
  .ep-sub-btn {
    display: inline-flex; align-items: center; justify-content: center; gap: 4px;
    box-sizing: border-box; min-height: 24px; min-width: 24px; padding: 0 9px;
    border-radius: 8px; border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04); color: hsl(var(--muted-foreground));
    font-size: 11px; cursor: pointer; flex-shrink: 0;
  }
  .ep-sub-btn.icon-only { padding: 0; width: 24px; }
  .ep-sub-btn:hover { background: hsl(var(--primary) / 0.15); color: hsl(var(--primary)); border-color: hsl(var(--primary) / 0.3); }
  /* Only relevant to the summary "Request" span (role="button"), which uses
     aria-disabled since a <span> has no native disabled attribute/styling. */
  .ep-sub-btn[aria-disabled='true'] { opacity: 0.5; pointer-events: none; }
  .ep-reset-btn:hover { background: hsl(0 70% 50% / 0.15); color: hsl(0 70% 60%); border-color: hsl(0 70% 50% / 0.3); }
  .media-strip { padding-bottom: 4px; }
  .person-slot { width: 146px; flex: 0 0 auto; }
  .poster-slot { width: 146px; flex: 0 0 auto; }
  .person-card {
    display: grid; gap: 8px; padding: 10px; border-radius: 16px;
    border: 1px solid hsl(0 0% 100% / 0.06); background: hsl(0 0% 100% / 0.03);
    min-height: 100%;
  }
  .person-photo { aspect-ratio: 2 / 3; overflow: hidden; border-radius: 12px; background: hsl(var(--muted)); }
  .empty {
    padding: 28px; text-align: center; color: hsl(var(--muted-foreground));
    border-radius: 20px; border: 1px solid hsl(0 0% 100% / 0.06); background: hsl(0 0% 100% / 0.02);
  }
  @media (max-width: 1200px) {
    .grid { grid-template-columns: 1fr; }
    .stat-grid, .kv { grid-template-columns: repeat(2, minmax(0,1fr)); }
  }

  @media (max-width: 980px) {
    .hero-grid, .grid { grid-template-columns: 1fr; }
    .poster { max-width: 220px; }
    .copy { align-content: start; }
  }

  @media (max-width: 700px) {
    .stat-grid, .kv { grid-template-columns: 1fr; }
    .hero-grid { padding: 18px; gap: 18px; }
    .action-row { align-items: stretch; }
  }
  .monitoring-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-top: 12px; padding-top: 12px; border-top: 1px solid hsl(0 0% 100% / 0.06); }
  .monitoring-row label { font-size: 12px; font-weight: 600; color: hsl(var(--muted-foreground)); white-space: nowrap; }
  .monitoring-row select { flex: 1; min-width: 0; height: 32px; border-radius: 8px; border: 1px solid hsl(0 0% 100% / 0.1); background: hsl(0 0% 100% / 0.05); color: inherit; font-size: 12px; padding: 0 8px; cursor: pointer; }

  /* Release picker modal */
  .modal-backdrop {
    position: fixed; inset: 0; z-index: 900;
    background: rgba(0, 0, 0, 0.5); /* bg-black/50, Dialog.Overlay */
    display: flex; align-items: center; justify-content: center; padding: 16px;
  }
  /* Structure (tabs + card list) borrowed from riven-frontend's manual-scrape
     dialog, but every color comes from Drakkar's own theme tokens (app.css)
     so it matches the rest of the app, not riven's palette. */
  .rel-modal {
    background: hsl(var(--card));
    border: 1px solid hsl(0 0% 100% / 0.1);
    border-radius: var(--radius-2xl);
    box-shadow: 0 10px 15px -3px hsl(0 0% 0% / 0.3), 0 4px 6px -4px hsl(0 0% 0% / 0.3);
    padding: 24px;
    display: flex; flex-direction: column; gap: 16px;
    width: 100%; max-width: min(calc(100% - 2rem), 896px);
    max-height: 80vh; overflow: hidden;
    color: hsl(var(--foreground));
  }
  .rel-header { flex-shrink: 0; display: flex; flex-direction: column; gap: 8px; }
  .info-modal {
    background: hsl(var(--card)); border: 1px solid hsl(0 0% 100% / 0.1);
    border-radius: 20px; padding: 20px; width: 100%; max-width: 440px;
    max-height: 80vh; overflow-y: auto; display: grid; gap: 14px;
  }
  .info-modal h2 { margin: 0; font-size: 16px; font-weight: 600; }
  .rel-header-top { display: flex; align-items: center; justify-content: space-between; }
  .rel-header h2 { margin: 0; font-size: 18px; font-weight: 600; line-height: 1; }
  .rel-header-desc { font-size: 14px; color: hsl(var(--muted-foreground)); }
  .close-btn {
    display: flex; align-items: center; justify-content: center;
    width: 28px; height: 28px; border-radius: var(--radius-sm);
    border: none; background: transparent;
    color: hsl(var(--foreground)); opacity: 0.7; cursor: pointer;
  }
  .close-btn:hover { opacity: 1; }
  .rel-tabs {
    display: grid; grid-template-columns: 1fr 1fr; gap: 0;
    height: 36px; padding: 3px; border-radius: var(--radius-lg);
    background: hsl(var(--muted)); color: hsl(var(--muted-foreground)); flex-shrink: 0;
  }
  .rel-tabs button {
    border-radius: var(--radius-md); border: 1px solid transparent; background: transparent;
    color: hsl(var(--muted-foreground)); font-size: 14px; font-weight: 500; cursor: pointer;
  }
  .rel-tabs button.active {
    background: hsl(var(--background)); color: hsl(var(--foreground));
    box-shadow: 0px 1px 4px 0px hsl(0 0% 0% / 0.05), 0px 1px 2px -1px hsl(0 0% 0% / 0.05);
  }
  .rel-tab-body { flex: 1; min-height: 0; overflow-y: auto; }
  .rel-list { display: flex; flex-direction: column; gap: 8px; }
  .manual-search-block { display: flex; flex-direction: column; gap: 14px; }
  .manual-search-form { display: flex; flex-direction: column; gap: 6px; }
  .manual-search-input-wrap { position: relative; display: flex; align-items: center; }
  .manual-search-input-wrap :global(svg) {
    position: absolute; left: 10px; color: hsl(var(--muted-foreground)); pointer-events: none;
  }
  .manual-search-input {
    height: 36px; width: 100%; padding: 0 12px 0 34px;
    border-radius: var(--radius-md); border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground)); font-size: 14px; outline: none;
  }
  .manual-search-input::placeholder { color: hsl(var(--muted-foreground)); }
  .upload-row {
    display: flex; align-items: center; gap: 8px; height: 36px; padding: 0 12px;
    border-radius: var(--radius-md); border: 1px dashed hsl(0 0% 100% / 0.15);
    background: hsl(0 0% 100% / 0.02); color: hsl(var(--muted-foreground));
    font-size: 13px; cursor: pointer;
  }
  .upload-row:hover:not(.disabled) { background: hsl(0 0% 100% / 0.05); border-color: hsl(var(--primary) / 0.4); }
  .upload-row.disabled { opacity: 0.6; cursor: default; }
  .upload-input { display: none; }
  .rel-filter-input {
    height: 32px; padding: 0 12px; border-radius: var(--radius-md);
    border: 1px solid hsl(0 0% 100% / 0.08); background: hsl(0 0% 100% / 0.03);
    color: hsl(var(--foreground)); font-size: 13px; outline: none;
  }
  .rel-filter-input::placeholder { color: hsl(var(--muted-foreground)); }
  .rel-empty { padding: 36px; text-align: center; color: hsl(var(--muted-foreground)); font-size: 14px; }
  .rel-searching-banner { display: flex; align-items: center; gap: 8px; padding: 10px 14px; border-radius: 10px; background: hsl(var(--primary) / 0.08); color: hsl(var(--muted-foreground)); font-size: 13px; }
  :global(.spin) { animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .rel-card {
    display: flex; flex-direction: column; gap: 8px; padding: 12px 16px;
    border-radius: var(--radius-xl);
    border: 1px solid hsl(0 0% 100% / 0.08); background: hsl(0 0% 100% / 0.03);
    cursor: pointer; transition: border-color .15s, box-shadow .15s;
  }
  .rel-card:hover { border-color: hsl(var(--primary) / 0.5); }
  .rel-selected { border-color: hsl(var(--primary) / 0.4); background: hsl(var(--primary) / 0.06); }
  .rel-rejected { opacity: 0.5; }
  .rel-card-top { display: flex; align-items: flex-start; justify-content: space-between; gap: 8px; }
  .rel-card-title {
    margin: 0; flex: 1; min-width: 0; font-size: 13px; font-weight: 600;
    color: hsl(var(--foreground)); line-height: 1.4; word-break: break-all;
  }
  .rel-badge {
    flex-shrink: 0; font-size: 11px; font-weight: 700; line-height: 1;
    padding: 2px 9px; border-radius: 999px;
    background: hsl(var(--primary) / 0.18); color: hsl(var(--primary)); white-space: nowrap;
  }
  .rel-badge-neg { background: hsl(var(--danger) / 0.15); color: hsl(var(--danger)); }
  .rel-card-badges { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .rel-badge-outline {
    font-size: 11px; padding: 2px 8px; border-radius: 999px;
    border: 1px solid hsl(0 0% 100% / 0.12); color: hsl(var(--muted-foreground));
    background: transparent; white-space: nowrap;
  }
  .rel-badge-outline.mono { font-family: 'JetBrains Mono', monospace; }
  .rel-badge-outline.tone-res-2160,
  .rel-badge-outline.tone-res-1080,
  .rel-badge-outline.tone-res-720 {
    background: hsl(var(--primary) / 0.16); border-color: transparent; color: hsl(var(--primary));
    font-weight: 600;
  }
  .rel-pill-ok { background: hsl(142 70% 45% / 0.15); border-color: hsl(142 70% 45% / 0.3); color: hsl(142 60% 55%); font-weight: 600; }
  .rel-pill-danger { background: hsl(var(--danger) / 0.15); border-color: hsl(var(--danger) / 0.25); color: hsl(var(--danger)); }
  .rel-pill-warn { background: hsl(40 90% 50% / 0.15); border-color: hsl(40 90% 50% / 0.25); color: hsl(40 80% 60%); }
  .rel-card :global(button) { align-self: flex-start; }
  .compat-warnings { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 4px; }
  .compat-badge {
    font-size: 10px; font-weight: 600; padding: 2px 7px; border-radius: 8px;
    background: hsl(38 92% 50% / 0.15); color: hsl(38 92% 70%);
    border: 1px solid hsl(38 92% 50% / 0.3); cursor: default;
  }
  .rel-why { margin-top: 4px; }
  .rel-why-toggle {
    font-size: 11px; color: hsl(var(--muted-foreground)); cursor: pointer;
    padding: 2px 0; list-style: none; display: inline-flex; align-items: center; gap: 4px;
    user-select: none;
  }
  .rel-why-toggle::-webkit-details-marker { display: none; }
  .rel-why-toggle::before { content: '▶'; font-size: 9px; transition: transform 0.15s; }
  details[open] .rel-why-toggle::before { transform: rotate(90deg); }
  .rel-explanations { display: grid; gap: 3px; padding-top: 6px; }
  .rel-explanation {
    font-size: 11px; color: hsl(var(--muted-foreground)); line-height: 1.5;
    display: flex; align-items: baseline; gap: 6px;
  }
  .rel-exp-pos { color: hsl(142 71% 55% / 0.9); }
  .rel-exp-neg { color: hsl(0 72% 62% / 0.85); }
  .rel-exp-reject { color: hsl(0 72% 62%); font-weight: 500; }
  .rel-exp-delta {
    font-family: 'JetBrains Mono', monospace; font-size: 10px; min-width: 36px;
    text-align: right; flex-shrink: 0; opacity: 0.9;
  }
</style>
