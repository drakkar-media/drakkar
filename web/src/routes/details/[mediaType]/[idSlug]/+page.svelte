<script lang="ts">
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
  import Button from '$lib/components/Button.svelte';
  import PosterCard from '$lib/components/PosterCard.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api, subscribeEvents } from '$lib/api';
  import { idFromSlug } from '$lib/detailsHref';
  import { toastError, toastSuccess } from '$lib/toast';
  import { bytes as fmtBytes } from '$lib/format';
  import { onMount } from 'svelte';
  import type { DiscoverDetails, GrabHistoryEntry, LibraryDetail, LibraryItem, ManualSearchItem, QualityProfile, ReleaseItem, SubtitleCandidate, SubtitleFile } from '$lib/types';

  let detail: DiscoverDetails | null = null;
  let libraryMatch: LibraryItem | null = null;
  let localDetail: LibraryDetail | null = null;
  let subtitles: SubtitleFile[] = [];
  let subtitleCandidates: SubtitleCandidate[] = [];
  let grabHistory: GrabHistoryEntry[] = [];
  let releaseCandidates: ReleaseItem[] = [];
  let profiles: QualityProfile[] = [];
  let activeProfileId: number | null = null;
  let showReleasePicker = false;
  let pickerLabel = '';
  let pickerLibraryItemID: number | null = null;
  let pickerSearching = false;
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
  let working = false;
  let activeKey = '';

  type EpisodeSubtitleState = {
    loading: boolean;
    files: SubtitleFile[];
    candidates: SubtitleCandidate[];
  };
  let expandedEpisodeId: number | null = null;
  let episodeSubtitles: Record<number, EpisodeSubtitleState> = {};
  // Guards against a slower earlier navigation's response landing after a
  // faster later one's and overwriting the newer page's data (e.g. quickly
  // clicking through several poster cards in a row).
  let loadToken = 0;


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

  function normalizeTitle(value: string) {
    return value.toLowerCase().replace(/[''']/g, '').replace(/[^a-z0-9]+/g, ' ').trim();
  }

  type ParsedExplanation = { text: string; delta: number | null; isReject: boolean };
  function parseExplanation(line: string): ParsedExplanation {
    const isReject = line.startsWith('Rejected:') || line.startsWith('Rejected by');
    const m = line.match(/\(([+-]\d+)\)$/);
    const delta = m ? parseInt(m[1], 10) : null;
    return { text: line, delta, isReject };
  }

  function sameIdentity(item: LibraryItem, mediaType: string, title: string, year?: number, tmdbId?: number, imdbId?: string) {
    const mapped = item.mediaType === 'episode' ? 'tv' : item.mediaType;
    if (mapped !== mediaType) return false;
    if (tmdbId && item.tmdbId === tmdbId) return true;
    if (imdbId && item.imdbId === imdbId) return true;
    return normalizeTitle(item.title) === normalizeTitle(title) && (!!year ? item.year === year : true);
  }

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
        const [detailResult, subtitleResult, candidateResult, historyResult, profilesResult, activeProfileResult] = await Promise.all([
          api.libraryDetail(libraryMatch.id),
          api.subtitles(libraryMatch.id),
          api.subtitleCandidates(libraryMatch.id),
          api.grabHistory(libraryMatch.id).catch(() => ({ items: [] })),
          api.listProfiles().catch(() => ({ profiles: [] })),
          api.getLibraryProfile(libraryMatch.id).catch(() => ({ profile: null }))
        ]);
        if (token !== loadToken) return;
        localDetail = detailResult;
        subtitles = subtitleResult.items ?? [];
        subtitleCandidates = candidateResult.items ?? [];
        grabHistory = historyResult.items ?? [];
        profiles = profilesResult.profiles ?? [];
        activeProfileId = activeProfileResult.profile?.id ?? null;
      } else {
        localDetail = null;
        subtitles = [];
        subtitleCandidates = [];
        grabHistory = [];
        profiles = [];
        activeProfileId = null;
      }
    } catch (error) {
      if (token !== loadToken) return;
      toastError(error instanceof Error ? error.message : String(error));
      detail = null;
      libraryMatch = null;
      localDetail = null;
      subtitles = [];
      subtitleCandidates = [];
      profiles = [];
      activeProfileId = null;
    } finally {
      if (token === loadToken) loading = false;
    }
  }

  $: {
    const nextKey = `${page.params.mediaType}:${page.params.idSlug}:${page.url.search}`;
    if (nextKey !== activeKey) {
      activeKey = nextKey;
      void loadDetail();
    }
  }

  onMount(() => {
    return subscribeEvents((event) => {
      if (!event) return;
      if (event.kind === 'library.replacements' && event.libraryItemId === pickerLibraryItemID) {
        // Background search completed — refresh candidates and clear searching indicator
        api.releases(pickerLibraryItemID as number).then((r) => {
          releaseCandidates = (r.items ?? []).sort((a, b) => b.score - a.score);
          pickerSearching = false;
        }).catch(() => { pickerSearching = false; });
      }
      if (event.kind === 'subtitle.search' && event.libraryItemId === libraryMatch?.id) {
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
    // Always fetch live — never reuse a previously loaded candidate list,
    // since candidates can be replaced/deleted server-side between views.
    pickerSearching = true;
    try {
      const result = await api.replacementCandidates(pickerLibraryItemID);
      releaseCandidates = (result.items ?? []).sort((a, b) => b.score - a.score);
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      pickerSearching = false;
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

  async function pickRelease(candidateId: number) {
    working = true;
    try {
      await api.selectRelease(candidateId);
      showReleasePicker = false;
      await loadDetail();
      toastSuccess('Release selected');
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      working = false;
    }
  }

  async function runManualSearch() {
    if (!manualQuery.trim()) return;
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
    manualImporting = true;
    try {
      await api.manualImportRelease(pickerLibraryItemID, item);
      toastSuccess('Manual release imported');
      showReleasePicker = false;
      manualQuery = '';
      manualResults = [];
      await loadDetail();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      manualImporting = false;
    }
  }

  async function uploadNzbFile(file: File) {
    if (!pickerLibraryItemID) return;
    uploadingNzb = true;
    try {
      await api.manualImportUpload(pickerLibraryItemID, file);
      toastSuccess('NZB file imported');
      showReleasePicker = false;
      await loadDetail();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      uploadingNzb = false;
    }
  }

  async function resetItem(targetLibraryItemId: number, label: string) {
    if (!confirm(`Reset "${label}"?\n\nThis removes the symlink and re-queues the item from scratch.`)) return;
    working = true;
    try {
      await api.resetLibraryItem(targetLibraryItemId);
      await loadDetail();
      toastSuccess('Item reset — re-queued');
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      working = false;
    }
  }

  async function runRepublish() {
    if (!libraryMatch) return;
    working = true;
    try {
      await api.republishLibrary(libraryMatch.id);
      toastSuccess('republished library item');
      await loadDetail();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      working = false;
    }
  }

  async function runSubtitleSearch(forLibraryItemId?: number) {
    const itemId = forLibraryItemId ?? libraryMatch?.id;
    if (!itemId) return;
    working = true;
    try {
      await api.searchSubtitles(itemId, ['nl', 'en']);
      toastSuccess('Searching subtitles in background...');
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      working = false;
    }
  }

  async function requestSeason(seasonNumber: number, seasonLabel: string) {
    if (!detail?.tmdbId) return;
    working = true;
    try {
      const result = await api.requestMedia(detail.tmdbId, 'tv', [seasonNumber]);
      toastSuccess(`Requested ${seasonLabel} — ${result.created} item(s) added`);
      await loadDetail();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      working = false;
    }
  }

  async function prioritizeMissingForShow() {
    if (!localDetail?.tvShowId) return;
    working = true;
    try {
      const result = await api.prioritizeTVShowMissing(localDetail.tvShowId);
      toastSuccess(`Prioritized show — queued ${result.queued}, created ${result.itemsCreated}`);
      await loadDetail();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      working = false;
    }
  }

  async function downloadSubtitle(candidateID: number) {
    if (!libraryMatch) return;
    working = true;
    try {
      await api.downloadSubtitleCandidate(candidateID);
      toastSuccess('subtitle downloaded');
      await loadDetail();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      working = false;
    }
  }

  async function deleteSubtitleFile(subtitleID: number) {
    working = true;
    try {
      await api.deleteSubtitle(subtitleID);
      toastSuccess('subtitle deleted');
      await loadDetail();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      working = false;
    }
  }

  async function loadEpisodeSubtitles(epLibraryItemId: number) {
    episodeSubtitles = { ...episodeSubtitles, [epLibraryItemId]: { loading: true, files: [], candidates: [] } };
    try {
      const [filesResult, candidatesResult] = await Promise.all([
        api.subtitles(epLibraryItemId),
        api.subtitleCandidates(epLibraryItemId)
      ]);
      episodeSubtitles = {
        ...episodeSubtitles,
        [epLibraryItemId]: { loading: false, files: filesResult.items ?? [], candidates: candidatesResult.items ?? [] }
      };
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
      episodeSubtitles = { ...episodeSubtitles, [epLibraryItemId]: { loading: false, files: [], candidates: [] } };
    }
  }

  function toggleEpisodeSubtitles(epLibraryItemId: number) {
    if (expandedEpisodeId === epLibraryItemId) {
      expandedEpisodeId = null;
      return;
    }
    expandedEpisodeId = epLibraryItemId;
    if (!episodeSubtitles[epLibraryItemId]) void loadEpisodeSubtitles(epLibraryItemId);
  }

  async function downloadEpisodeSubtitle(epLibraryItemId: number, candidateID: number) {
    working = true;
    try {
      await api.downloadSubtitleCandidate(candidateID);
      toastSuccess('subtitle downloaded');
      await loadEpisodeSubtitles(epLibraryItemId);
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      working = false;
    }
  }

  async function deleteEpisodeSubtitle(epLibraryItemId: number, subtitleID: number) {
    working = true;
    try {
      await api.deleteSubtitle(subtitleID);
      toastSuccess('subtitle deleted');
      await loadEpisodeSubtitles(epLibraryItemId);
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      working = false;
    }
  }

  async function updateQualityProfile(nextValue: string) {
    if (!libraryMatch) return;
    const parsedProfileId = nextValue ? Number(nextValue) : null;
    const nextProfileId = parsedProfileId != null && Number.isFinite(parsedProfileId) ? parsedProfileId : null;
    working = true;
    try {
      await api.setLibraryProfile(libraryMatch.id, nextProfileId);
      activeProfileId = nextProfileId;
      toastSuccess(activeProfileId ? 'quality profile updated' : 'quality profile override cleared');
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
      await loadDetail();
    } finally {
      working = false;
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
              <Button kind="secondary" on:click={runLocalSearch} disabled={working}>
                <Search size={15} />
                {libraryMatch.available ? 'Find Upgrade' : localDetail?.tvShowId ? 'Search Show' : 'Search'}
              </Button>
              {#if localDetail?.tvShowId && (libraryMatch.missingCount ?? 0) > 0}
                <Button kind="secondary" on:click={prioritizeMissingForShow} disabled={working}>
                  <Download size={15} />
                  Prioritize Missing
                </Button>
              {/if}
              <Button kind="secondary" on:click={() => runSubtitleSearch()} disabled={working}>
                <Languages size={15} />
                Subs
              </Button>
              <Button kind="secondary" on:click={runRepublish} disabled={working}>
                <RotateCcw size={15} />
                Republish
              </Button>
              <Button kind="ghost" on:click={() => resetItem(libraryMatch!.id, detail?.title ?? 'this item')} disabled={working}>
                <Trash2 size={15} />
                Reset
              </Button>
            {/if}
            <a class="link-btn secondary" href="/search">Back To Search</a>
            <Button kind="ghost" on:click={loadDetail} disabled={working || loading}>
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
                        <button
                          class="ep-sub-btn"
                          type="button"
                          aria-label={`Request ${season.name} in Seerr`}
                          title={`Request ${season.name} in Seerr`}
                          disabled={working}
                          on:click|preventDefault|stopPropagation={() => requestSeason(season.seasonNumber, season.name)}
                        >
                          <Download size={11} />
                          Request
                        </button>
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
                        </div>
                        <div class="ep-right">
                          <StatusPill tone={episode.status === 'available' ? 'ok' : 'neutral'}>{episode.status}</StatusPill>
                          {#if episode.libraryItemId}
                            {@const epId = episode.libraryItemId}
                            <button
                              class="ep-sub-btn"
                              title="Search releases for this episode (includes season packs)"
                              disabled={working}
                              on:click={() => runEpisodeSearch(epId, episode.seasonNumber, episode.episodeNumber, episode.title)}
                            ><Search size={11} /> Search</button>
                            {#if episode.status === 'available'}
                              <button
                                class="ep-sub-btn"
                                title="Manage subtitles for this episode"
                                disabled={working}
                                on:click={() => toggleEpisodeSubtitles(epId)}
                              ><Languages size={11} /> Subs</button>
                              <button
                                class="ep-sub-btn ep-reset-btn"
                                title="Reset this episode"
                                disabled={working}
                                on:click={() => resetItem(epId, `S${String(episode.seasonNumber).padStart(2,'0')}E${String(episode.episodeNumber).padStart(2,'0')} ${episode.title}`)}
                              ><Trash2 size={11} /></button>
                            {/if}
                          {/if}
                        </div>
                      </div>
                      {#if episode.libraryItemId && expandedEpisodeId === episode.libraryItemId}
                        {@const epId = episode.libraryItemId}
                        {@const state = episodeSubtitles[epId]}
                        <div class="episode-subs">
                          {#if !state || state.loading}
                            <div class="empty-side">Loading subtitles…</div>
                          {:else}
                            {#if state.files.length > 0}
                              <div class="stack-list">
                                {#each state.files as file}
                                  <div class="stack-item">
                                    <div>
                                      <strong>{file.language.toUpperCase()}</strong>
                                      <span>{file.provider}</span>
                                    </div>
                                    <Button kind="ghost" on:click={() => deleteEpisodeSubtitle(epId, file.id)} disabled={working}>
                                      <Trash2 size={13} />
                                    </Button>
                                  </div>
                                {/each}
                              </div>
                            {:else}
                              <div class="empty-side">No published subtitles for this episode.</div>
                            {/if}
                            {#if state.candidates.length > 0}
                              <div class="stack-list">
                                {#each state.candidates.slice(0, 5) as candidate}
                                  <div class="stack-item candidate">
                                    <div>
                                      <strong>{candidate.language.toUpperCase()} · {candidate.provider}</strong>
                                      <span>{candidate.releaseName || candidate.title}</span>
                                    </div>
                                    <Button kind="secondary" on:click={() => downloadEpisodeSubtitle(epId, candidate.id)} disabled={working}>
                                      <Languages size={13} />
                                      Get
                                    </Button>
                                  </div>
                                {/each}
                              </div>
                            {/if}
                            <Button kind="secondary" on:click={() => runSubtitleSearch(epId).then(() => loadEpisodeSubtitles(epId))} disabled={working}>
                              <Search size={13} />
                              Search Subtitles
                            </Button>
                          {/if}
                        </div>
                      {/if}
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
                value={activeProfileId == null ? '' : String(activeProfileId)}
                disabled={working || profiles.length === 0}
                on:change={(e) => updateQualityProfile((e.currentTarget as HTMLSelectElement).value)}
              >
                <option value="">Default profile</option>
                {#each profiles as profile}
                  <option value={profile.id}>{profile.name}{profile.isDefault ? ' · default' : ''}</option>
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
                  value={localDetail.monitoringMode ?? 'all'}
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

        {#if libraryMatch}
          <section class="panel">
            <div class="panel-head">
              <h2>Subtitles</h2>
              <a class="link-btn ghost" href="/subtitles">Manager</a>
            </div>
            {#if subtitles.length > 0}
              <div class="stack-list">
                {#each subtitles as subtitle}
                  <div class="stack-item">
                    <div>
                      <strong>{subtitle.language.toUpperCase()}</strong>
                      <span>{subtitle.provider}</span>
                    </div>
                    <Button kind="ghost" on:click={() => deleteSubtitleFile(subtitle.id)} disabled={working}>
                      <Trash2 size={14} />
                    </Button>
                  </div>
                {/each}
              </div>
            {:else}
              <div class="empty-side">No published subtitles yet.</div>
            {/if}
            {#if subtitleCandidates.length > 0}
              <div class="stack-list">
                {#each subtitleCandidates.slice(0, 8) as candidate}
                  <div class="stack-item candidate">
                    <div>
                      <strong>{candidate.language.toUpperCase()} · {candidate.provider}</strong>
                      <span>{candidate.releaseName || candidate.title}</span>
                    </div>
                    <Button kind="secondary" on:click={() => downloadSubtitle(candidate.id)} disabled={working}>
                      <Languages size={14} />
                      Get
                    </Button>
                  </div>
                {/each}
              </div>
            {/if}
          </section>

          {#if grabHistory.length > 0}
            <section class="panel">
              <h2>Grab History</h2>
              <div class="stack-list">
                {#each grabHistory as entry}
                  <div class="stack-item">
                    <div class="gh-info">
                      <strong class="gh-title">{entry.title}</strong>
                      <span class="gh-meta">
                        {entry.indexerName}{entry.resolution ? ` · ${entry.resolution}` : ''} · score {entry.score}
                      </span>
                      <span class="gh-date">{new Date(entry.grabbedAt).toLocaleString('en-GB', { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' })}</span>
                    </div>
                  </div>
                {/each}
              </div>
            </section>
          {/if}
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
                  on:keydown={(e) => e.key === 'Enter' && runManualSearch()}
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
          {#if pickerSearching}
            <div class="rel-empty">Searching for releases…</div>
          {/if}
          {#if releaseCandidates.length === 0 && !pickerSearching}
            <div class="rel-empty">No candidates found.</div>
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
                  <Button kind={c.selected ? 'primary' : 'secondary'} on:click={() => pickRelease(c.releaseCandidateId)} disabled={working}>
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
  .panel-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
  .panel-head h2 { margin: 0; }
  .episode-subs {
    padding: 10px 12px 12px 30px; margin: -4px 0 6px;
    border-left: 2px solid hsl(0 0% 100% / 0.08);
    display: grid; gap: 8px;
  }
  .grid { display: grid; grid-template-columns: minmax(0,1.7fr) minmax(300px,0.8fr); gap: 20px; align-items: start; }
  .main, .side { display: grid; gap: 18px; }
  .panel {
    border-radius: 24px; border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(var(--card) / 0.82); padding: 18px;
    min-width: 0;
  }
  .panel h2 { margin: 0 0 14px; font-size: 18px; }
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
  .stack-list { display: grid; gap: 10px; }
  .stack-item {
    display: flex; align-items: center; justify-content: space-between; gap: 12px;
    padding: 12px 14px; border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.06); background: hsl(0 0% 100% / 0.03);
  }
  .stack-item strong, .stack-item span { display: block; }
  .stack-item span {
    margin-top: 4px; color: hsl(var(--muted-foreground)); font-size: 12px;
  }
  .candidate { align-items: flex-start; }
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
  .ep-sub-btn {
    display: inline-flex; align-items: center; gap: 4px; padding: 3px 9px;
    border-radius: 8px; border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04); color: hsl(var(--muted-foreground));
    font-size: 11px; cursor: pointer; flex-shrink: 0;
  }
  .ep-sub-btn:hover { background: hsl(var(--primary) / 0.15); color: hsl(var(--primary)); border-color: hsl(var(--primary) / 0.3); }
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
  .gh-info { display: grid; gap: 2px; min-width: 0; }
  .gh-title { font-size: 12px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .gh-meta { font-size: 11px; color: hsl(var(--muted-foreground)); font-family: 'JetBrains Mono', monospace; }
  .gh-date { font-size: 11px; color: hsl(var(--muted-foreground)); }
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
