<script lang="ts">
  /**
   * Self-contained subtitle manager for one library item (a movie, a show,
   * or a single episode — all are library items with their own id). Fetches
   * its own files/candidates, stays live via SSE, and owns its own
   * search/download/delete actions.
   *
   * Replaces three separate, drifted copies of this same file-list +
   * candidate-list + search/download/delete logic that used to be
   * hand-duplicated across the details page's show-level section and its
   * per-episode section (each with its own busy-state keys, and the
   * show-level one used a different hardcoded language list than the
   * library-wide subtitle manager page). Mount/unmount this component
   * (e.g. inside an `{#if expanded}`) to get the same lazy-load-on-first-view
   * behavior the old per-episode code hand-rolled with a loading map.
   */
  import { onMount } from 'svelte';
  import SearchCheck from '@lucide/svelte/icons/search-check';
  import Languages from '@lucide/svelte/icons/languages';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import Button from '$lib/components/Button.svelte';
  import { api, subscribeEvents } from '$lib/api';
  import { toastError } from '$lib/toast';
  import { runAction, confirmed } from '$lib/actions';
  import type { SubtitleFile, SubtitleCandidate } from '$lib/types';

  /** Which library item (show/movie or a single episode) this panel manages. */
  export let libraryItemId: number;
  /** Tighter spacing/icon sizing for nested contexts (e.g. inside an expanded episode row). */
  export let compact = false;
  /** Shows a link to the library-wide subtitle manager page (show/movie-level only). */
  export let showManagerLink = false;

  let files: SubtitleFile[] = [];
  let candidates: SubtitleCandidate[] = [];
  let loading = true;
  let busy: Record<string, boolean> = {};
  function isBusy(key: string): boolean {
    return !!busy[key];
  }
  function setBusy(key: string, value: boolean) {
    busy = { ...busy, [key]: value };
  }

  $: iconSize = compact ? 13 : 14;

  async function load() {
    loading = true;
    try {
      const [filesResult, candidatesResult] = await Promise.all([
        api.subtitles(libraryItemId),
        api.subtitleCandidates(libraryItemId)
      ]);
      files = filesResult.items ?? [];
      candidates = candidatesResult.items ?? [];
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
      files = [];
      candidates = [];
    } finally {
      loading = false;
    }
  }

  async function search() {
    // Empty languages defers to the server's configured default languages
    // and its own already-satisfied-language skip -- this panel doesn't
    // need to (and must not) hardcode a language list itself.
    await runAction(() => api.searchSubtitles(libraryItemId, []), {
      setWorking: (v) => setBusy('search', v),
      successMessage: () => 'Searching subtitles in background…',
      afterSuccess: load
    });
  }

  async function download(candidateId: number) {
    await runAction(() => api.downloadSubtitleCandidate(candidateId), {
      setWorking: (v) => setBusy(`download-${candidateId}`, v),
      successMessage: () => 'Subtitle downloaded',
      afterSuccess: load
    });
  }

  async function deleteFile(fileId: number) {
    if (!confirmed('Delete this subtitle file?')) return;
    await runAction(() => api.deleteSubtitle(fileId), {
      setWorking: (v) => setBusy(`delete-${fileId}`, v),
      successMessage: () => 'Subtitle deleted',
      afterSuccess: load
    });
  }

  onMount(() => {
    void load();
    return subscribeEvents((event) => {
      if (event?.kind === 'subtitle.search' && event.libraryItemId === libraryItemId) void load();
    });
  });
</script>

<div class="subtitle-panel" class:compact>
  {#if showManagerLink}
    <div class="panel-head-inline">
      <a class="manager-link" href="/subtitles">Manager</a>
    </div>
  {/if}
  {#if loading}
    <div class="empty-side">Loading subtitles…</div>
  {:else}
    {#if files.length > 0}
      <div class="stack-list">
        {#each files as file (file.id)}
          <div class="stack-item">
            <div>
              <strong>{file.language.toUpperCase()}</strong>
              <span>{file.provider}</span>
            </div>
            <Button kind="ghost" on:click={() => deleteFile(file.id)} disabled={isBusy(`delete-${file.id}`)}>
              <Trash2 size={iconSize} />
            </Button>
          </div>
        {/each}
      </div>
    {:else}
      <div class="empty-side">No published subtitles yet.</div>
    {/if}
    {#if candidates.length > 0}
      <div class="stack-list candidates">
        {#each candidates.slice(0, 8) as candidate (candidate.id)}
          <div class="stack-item candidate">
            <div>
              <strong>{candidate.language.toUpperCase()} · {candidate.provider}</strong>
              <span>{candidate.releaseName || candidate.title}</span>
            </div>
            <Button kind="secondary" on:click={() => download(candidate.id)} disabled={isBusy(`download-${candidate.id}`)}>
              <Languages size={iconSize} />
              Get
            </Button>
          </div>
        {/each}
      </div>
    {/if}
    <Button kind="secondary" on:click={search} disabled={isBusy('search')}>
      <SearchCheck size={iconSize} />
      Search Subtitles
    </Button>
  {/if}
</div>

<style>
  .subtitle-panel {
    display: grid;
    gap: 10px;
  }

  .panel-head-inline {
    display: flex;
    justify-content: flex-end;
  }

  .manager-link {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 28px;
    padding: 0 10px;
    border-radius: 12px;
    border: 1px solid transparent;
    color: hsl(var(--muted-foreground));
    font-size: 12px;
    text-decoration: none;
  }

  .empty-side {
    padding: 12px 14px;
    border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.03);
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .stack-list {
    display: grid;
    gap: 10px;
  }

  .stack-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 14px;
    border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.03);
  }

  .stack-item strong,
  .stack-item span {
    display: block;
  }

  .stack-item span {
    margin-top: 4px;
    color: hsl(var(--muted-foreground));
    font-size: 12px;
  }

  /* Release-name/candidate text (e.g. "Show.Name.S01E04.1080p.WEB-DL-GROUP")
     is one long unbroken token with no spaces, so as a flex child its
     default min-content width is the full string width -- without
     min-width: 0 here, the row (and its Button) rendered past the panel's
     edge instead of truncating. */
  .stack-item.candidate > div {
    min-width: 0;
    overflow: hidden;
  }

  .stack-item.candidate strong,
  .stack-item.candidate span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .stack-item.candidate > :global(.button) {
    flex-shrink: 0;
  }

  .candidate {
    align-items: flex-start;
  }

  .compact .stack-item {
    padding: 10px 12px;
  }
</style>
