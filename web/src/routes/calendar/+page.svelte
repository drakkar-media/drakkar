<script lang="ts">
  import { onMount } from 'svelte';
  import ChevronLeft from '@lucide/svelte/icons/chevron-left';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import X from '@lucide/svelte/icons/x';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import CheckCircle2 from '@lucide/svelte/icons/check-circle-2';
  import Clock from '@lucide/svelte/icons/clock';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Button from '$lib/components/Button.svelte';
  import { api } from '$lib/api';
  import { toastError } from '$lib/toast';

  type Entry = {
    id: number; libraryItemId: number; type: string; title: string;
    releaseDate: string; tmdbId?: number; posterUrl?: string;
    available: boolean; queueState?: string;
  };

  type GridDay = {
    date: string; day: number; inMonth: boolean; isToday: boolean; entries: Entry[];
  };

  const TYPE_STYLE: Record<string, string> = {
    movie:   'entry-movie',
    episode: 'entry-episode',
    tv:      'entry-tv',
  };

  const STATE_LABEL: Record<string, string> = {
    selected: 'Queued', downloading: 'Downloading', complete: 'Complete',
    failed: 'Failed', preflight: 'Preflight',
  };

  function monthKey(year: number, month: number) {
    return `${year}-${String(month).padStart(2, '0')}`;
  }

  function currentMonthKey() {
    const n = new Date();
    return monthKey(n.getFullYear(), n.getMonth() + 1);
  }

  function shiftMonth(key: string, delta: number) {
    const [y, m] = key.split('-').map(Number);
    const d = new Date(Date.UTC(y, m - 1 + delta, 1));
    return monthKey(d.getUTCFullYear(), d.getUTCMonth() + 1);
  }

  function monthLabel(key: string) {
    const [y, m] = key.split('-').map(Number);
    return new Intl.DateTimeFormat('en-US', { month: 'long', year: 'numeric', timeZone: 'UTC' })
      .format(new Date(Date.UTC(y, m - 1, 1)));
  }

  function longDate(date: string) {
    return new Intl.DateTimeFormat('en-US', {
      weekday: 'long', month: 'long', day: 'numeric', year: 'numeric', timeZone: 'UTC'
    }).format(new Date(`${date}T00:00:00Z`));
  }

  function buildGrid(key: string, entries: Entry[]): GridDay[] {
    const [year, month] = key.split('-').map(Number);
    const first = new Date(Date.UTC(year, month - 1, 1));
    const last  = new Date(Date.UTC(year, month, 0));
    const startDay = first.getUTCDay();
    const gridStart = new Date(Date.UTC(year, month - 1, 1 - startDay));
    const todayISO = new Date().toISOString().slice(0, 10);

    const byDate = new Map<string, Entry[]>();
    for (const e of entries) {
      const arr = byDate.get(e.releaseDate) ?? [];
      arr.push(e);
      byDate.set(e.releaseDate, arr);
    }

    return Array.from({ length: 42 }, (_, i) => {
      const d = new Date(gridStart);
      d.setUTCDate(gridStart.getUTCDate() + i);
      const iso = d.toISOString().slice(0, 10);
      return {
        date: iso,
        day: d.getUTCDate(),
        inMonth: d >= first && d <= last,
        isToday: iso === todayISO,
        entries: (byDate.get(iso) ?? []).sort((a, b) => a.title.localeCompare(b.title))
      };
    });
  }

  // Cache for fetched months so navigating back is instant
  const cache = new Map<string, Entry[]>();

  let currentKey = currentMonthKey();
  let entries: Entry[] = [];
  let loading = false;
  let selected: Entry | null = null;
  let filters = { movie: true, episode: true };

  $: label = monthLabel(currentKey);
  $: visible = entries.filter(e => {
    const t = e.type === 'tv' ? 'episode' : e.type;
    return filters[t as keyof typeof filters] ?? true;
  });
  $: grid = buildGrid(currentKey, visible);
  $: movieCount = entries.filter(e => e.type === 'movie').length;
  $: episodeCount = entries.filter(e => e.type === 'episode' || e.type === 'tv').length;

  function statusLabel(e: Entry) {
    if (e.available) return 'Available';
    if (e.queueState && STATE_LABEL[e.queueState]) return STATE_LABEL[e.queueState];
    if (e.queueState) return e.queueState;
    return 'Not downloaded';
  }

  function statusTone(e: Entry) {
    if (e.available) return 'ok';
    if (e.queueState === 'failed') return 'err';
    if (e.queueState) return 'pending';
    return 'none';
  }

  async function fetchMonth(key: string): Promise<Entry[]> {
    if (cache.has(key)) return cache.get(key)!;
    const r = await api.releaseCalendar(key);
    const items = r.entries ?? [];
    cache.set(key, items);
    return items;
  }

  async function load(key: string) {
    // Use cached data immediately if available (no flicker)
    if (cache.has(key)) {
      entries = cache.get(key)!;
    } else {
      loading = true;
    }
    try {
      entries = await fetchMonth(key);
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    } finally {
      loading = false;
    }
    // Preload adjacent months in background
    void fetchMonth(shiftMonth(key, -1)).catch(() => {});
    void fetchMonth(shiftMonth(key, 1)).catch(() => {});
  }

  function prev() { currentKey = shiftMonth(currentKey, -1); void load(currentKey); }
  function next() { currentKey = shiftMonth(currentKey, 1); void load(currentKey); }
  function today() { currentKey = currentMonthKey(); void load(currentKey); }

  onMount(() => void load(currentKey));
</script>

<svelte:head><title>Calendar — Drakkar</title></svelte:head>

<PageHeader title="Release Calendar" subtitle="Library movies and episodes by theatrical / air date." />

<!-- Month navigation -->
<div class="cal-header">
  <div class="cal-nav">
    <button class="icon-btn" on:click={prev} aria-label="Previous month"><ChevronLeft size={18} /></button>
    <span class="cal-month-label">{label}</span>
    <button class="icon-btn" on:click={next} aria-label="Next month"><ChevronRight size={18} /></button>
    <Button kind="secondary" on:click={today} disabled={loading}>Today</Button>
  </div>
  <div class="cal-stats">
    <button class="filter-pill" class:active={filters.movie} on:click={() => (filters = { ...filters, movie: !filters.movie })} type="button">
      <span class="dot dot-movie" class:dim={!filters.movie}></span>
      <span>{movieCount} Movie{movieCount !== 1 ? 's' : ''}</span>
    </button>
    <button class="filter-pill" class:active={filters.episode} on:click={() => (filters = { ...filters, episode: !filters.episode })} type="button">
      <span class="dot dot-episode" class:dim={!filters.episode}></span>
      <span>{episodeCount} Episode{episodeCount !== 1 ? 's' : ''}</span>
    </button>
  </div>
</div>

<!-- Calendar grid -->
<div class="cal-wrap" class:loading>
  <div class="cal-dow-row">
    {#each ['Sun','Mon','Tue','Wed','Thu','Fri','Sat'] as d}
      <div class="cal-dow">{d}</div>
    {/each}
  </div>

  {#if loading && entries.length === 0}
    <!-- Skeleton while first load -->
    <div class="cal-cells">
      {#each Array(42) as _, i}
        <div class="cell" class:out={i < 2 || i > 30}>
          <div class="cell-head"><span class="skel skel-day"></span></div>
          {#if Math.random() > 0.72}
            <div class="skel skel-entry"></div>
          {/if}
        </div>
      {/each}
    </div>
  {:else}
    <div class="cal-cells">
      {#each grid as day (day.date)}
        <div class="cell" class:out={!day.inMonth} class:today={day.isToday}>
          <div class="cell-head">
            <span class="cell-day" class:today-badge={day.isToday}>{day.day}</span>
            {#if day.entries.length > 0}
              <span class="cell-count">{day.entries.length}</span>
            {/if}
          </div>
          <div class="cell-entries">
            {#each day.entries.slice(0, 3) as entry (entry.id)}
              <button class="entry {TYPE_STYLE[entry.type === 'tv' ? 'episode' : entry.type] ?? ''}"
                class:entry-available={entry.available}
                title={statusLabel(entry)}
                on:click={() => (selected = entry)}>
                <span class="entry-title">{entry.title}</span>
                {#if entry.available}
                  <span class="entry-dot avail" aria-hidden="true"></span>
                {:else if entry.queueState === 'failed'}
                  <span class="entry-dot fail" aria-hidden="true"></span>
                {:else if entry.queueState}
                  <span class="entry-dot queued" aria-hidden="true"></span>
                {/if}
              </button>
            {/each}
            {#if day.entries.length > 3}
              <button class="entry-more" on:click={() => (selected = day.entries[3])}>
                +{day.entries.length - 3} more
              </button>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

{#if visible.length === 0 && !loading}
  <div class="empty">No library items with release dates in {label}.</div>
{/if}

<!-- Detail modal -->
{#if selected}
  {@const s = selected}
  <div
    class="modal-backdrop"
    on:click={(e) => e.target === e.currentTarget && (selected = null)}
    on:keydown={(e) => e.key === 'Escape' && (selected = null)}
    role="button"
    tabindex="0"
    aria-label="Close details dialog"
  >
    <div class="modal" role="dialog" aria-modal="true" tabindex="-1">
      <div class="modal-inner">
        {#if s.posterUrl}
          <img class="modal-poster" src={s.posterUrl} alt={s.title} loading="lazy" />
        {/if}
        <div class="modal-body">
          <div class="modal-head">
            <div class="modal-meta">
              <span class="modal-badge {TYPE_STYLE[s.type === 'tv' ? 'episode' : s.type] ?? ''}">
                {s.type === 'movie' ? 'Movie' : 'Episode'}
              </span>
              <h2 class="modal-title">{s.title}</h2>
              <p class="modal-date">{longDate(s.releaseDate)}</p>
            </div>
            <button class="icon-btn" on:click={() => (selected = null)} aria-label="Close"><X size={18} /></button>
          </div>

          <div class="modal-status-row">
            <div class="status-chip tone-{statusTone(s)}">
              {#if s.available}
                <CheckCircle2 size={13} />
              {:else if s.queueState}
                <Clock size={13} />
              {/if}
              {statusLabel(s)}
            </div>
          </div>

          <div class="modal-actions">
            <Button kind="secondary" on:click={() => (selected = null)}>Close</Button>
            <a class="btn-primary" href="/library/{s.libraryItemId}">
              <ExternalLink size={14} /> Open Details
            </a>
          </div>
        </div>
      </div>
    </div>
  </div>
{/if}

<style>
  .cal-header {
    display: flex; align-items: center; justify-content: space-between;
    gap: 12px; margin-bottom: 16px; flex-wrap: wrap;
  }
  .cal-nav { display: flex; align-items: center; gap: 10px; }
  .cal-month-label {
    font-size: 1.1rem; font-weight: 700; min-width: 190px; text-align: center;
  }
  .icon-btn {
    display: grid; place-items: center; width: 38px; height: 38px;
    border-radius: 12px; border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04); color: hsl(var(--foreground));
    cursor: pointer; transition: background .12s;
  }
  .icon-btn:hover { background: hsl(0 0% 100% / 0.1); }

  /* Filter pills / stats */
  .cal-stats { display: flex; gap: 8px; flex-wrap: wrap; }
  .filter-pill {
    display: inline-flex; align-items: center; gap: 7px;
    padding: 7px 14px; border-radius: 22px;
    border: 1px solid hsl(0 0% 100% / 0.1); background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--muted-foreground)); font-size: 13px; font-weight: 600;
    cursor: pointer; transition: all .12s;
  }
  .filter-pill.active { color: hsl(var(--foreground)); background: hsl(0 0% 100% / 0.08); }
  .dot {
    width: 9px; height: 9px; border-radius: 50%; flex-shrink: 0;
    transition: opacity .12s;
  }
  .dot.dim { opacity: 0.3; }
  .dot-movie   { background: hsl(186 95% 60%); }
  .dot-episode { background: hsl(270 90% 70%); }

  /* Grid */
  .cal-wrap {
    border-radius: 22px; border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(var(--card) / 0.6); overflow: hidden;
    transition: opacity .2s;
  }
  .cal-wrap.loading { opacity: .65; pointer-events: none; }

  .cal-dow-row {
    display: grid; grid-template-columns: repeat(7, minmax(0, 1fr));
    border-bottom: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(var(--background) / 0.4);
  }
  .cal-dow {
    padding: 10px 4px; text-align: center;
    font-size: 10px; font-weight: 700; text-transform: uppercase;
    letter-spacing: .14em; color: hsl(var(--muted-foreground));
  }

  .cal-cells { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); }

  .cell {
    min-height: 110px; padding: 8px 6px;
    border-right: 1px solid hsl(0 0% 100% / 0.05);
    border-bottom: 1px solid hsl(0 0% 100% / 0.05);
    display: flex; flex-direction: column; gap: 4px;
  }
  .cell:nth-child(7n) { border-right: none; }
  .cell.out { background: hsl(var(--background) / 0.25); }
  .cell.today {
    background: hsl(var(--primary) / 0.07);
    box-shadow: inset 0 0 0 1px hsl(var(--primary) / 0.25);
  }

  .cell-head {
    display: flex; align-items: center; justify-content: space-between; margin-bottom: 2px;
  }
  .cell-day {
    font-size: 12px; font-weight: 700; color: hsl(var(--muted-foreground)); line-height: 1;
  }
  .cell.out .cell-day { opacity: 0.35; }
  .today-badge {
    background: hsl(var(--primary)); color: hsl(var(--primary-foreground));
    padding: 1px 6px; border-radius: 99px; font-size: 11px;
  }
  .cell-count { font-size: 10px; color: hsl(var(--muted-foreground)); }
  .cell-entries { display: flex; flex-direction: column; gap: 3px; min-width: 0; }

  .entry {
    display: flex; align-items: center; justify-content: space-between;
    width: 100%; padding: 4px 6px; border-radius: 8px;
    border: 1px solid transparent; text-align: left; cursor: pointer;
    transition: filter .1s; min-width: 0; gap: 4px;
  }
  .entry:hover { filter: brightness(1.18); }
  .entry-title {
    font-size: 11px; font-weight: 700;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; min-width: 0;
  }
  .entry-dot {
    width: 6px; height: 6px; border-radius: 50%; flex-shrink: 0;
  }
  .entry-dot.avail  { background: hsl(142 70% 55%); }
  .entry-dot.fail   { background: hsl(0 80% 60%); }
  .entry-dot.queued { background: hsl(43 90% 60%); }

  .entry-movie   { border-color: hsl(186 80% 50% / 0.35); background: hsl(186 80% 50% / 0.15); color: hsl(186 95% 85%); }
  .entry-episode { border-color: hsl(270 75% 65% / 0.35); background: hsl(270 75% 65% / 0.15); color: hsl(270 90% 85%); }
  .entry-tv      { border-color: hsl(142 70% 45% / 0.35); background: hsl(142 70% 45% / 0.15); color: hsl(142 80% 75%); }

  .entry-more {
    font-size: 11px; font-weight: 600; color: hsl(var(--muted-foreground));
    padding: 2px 4px; text-align: left; cursor: pointer; background: none; border: none;
  }
  .entry-more:hover { color: hsl(var(--foreground)); }

  /* Skeleton */
  .skel { border-radius: 6px; background: hsl(0 0% 100% / 0.06); animation: pulse 1.6s ease-in-out infinite; }
  .skel-day { display: block; width: 18px; height: 12px; }
  .skel-entry { display: block; width: 100%; height: 24px; border-radius: 8px; }
  @keyframes pulse { 0%,100% { opacity: .6; } 50% { opacity: 1; } }

  /* Modal */
  .modal-backdrop {
    position: fixed; inset: 0; z-index: 50;
    background: hsl(0 0% 0% / 0.72); backdrop-filter: blur(8px);
    display: grid; place-items: center; padding: 16px;
  }
  .modal {
    width: 100%; max-width: 520px; border-radius: 28px;
    border: 1px solid hsl(0 0% 100% / 0.1); background: hsl(var(--card));
    box-shadow: 0 40px 80px hsl(0 0% 0% / 0.5); overflow: hidden;
  }
  .modal-inner { display: flex; gap: 0; }
  .modal-poster {
    width: 140px; flex-shrink: 0; object-fit: cover; object-position: center top;
    border-right: 1px solid hsl(0 0% 100% / 0.08);
  }
  .modal-body { flex: 1; min-width: 0; padding: 22px; display: flex; flex-direction: column; gap: 16px; }
  .modal-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
  .modal-meta { flex: 1; min-width: 0; }
  .modal-badge {
    display: inline-block; padding: 3px 10px; border-radius: 8px;
    font-size: 11px; font-weight: 700; margin-bottom: 8px;
  }
  .modal-title { font-size: 1.25rem; font-weight: 700; line-height: 1.25; }
  .modal-date { font-size: 13px; color: hsl(var(--muted-foreground)); margin-top: 5px; }

  .modal-status-row { display: flex; gap: 8px; flex-wrap: wrap; }
  .status-chip {
    display: inline-flex; align-items: center; gap: 6px;
    padding: 6px 12px; border-radius: 12px; font-size: 13px; font-weight: 600;
    border: 1px solid transparent;
  }
  .tone-ok     { background: hsl(142 60% 40% / 0.2); border-color: hsl(142 60% 40% / 0.3); color: hsl(142 70% 70%); }
  .tone-err    { background: hsl(0 60% 45% / 0.2); border-color: hsl(0 60% 45% / 0.3); color: hsl(0 80% 75%); }
  .tone-pending{ background: hsl(43 80% 50% / 0.15); border-color: hsl(43 80% 50% / 0.3); color: hsl(43 90% 75%); }
  .tone-none   { background: hsl(0 0% 100% / 0.05); border-color: hsl(0 0% 100% / 0.08); color: hsl(var(--muted-foreground)); }

  .modal-actions { display: flex; justify-content: flex-end; gap: 10px; margin-top: auto; }
  .btn-primary {
    display: inline-flex; align-items: center; gap: 7px;
    padding: 0 16px; height: 40px; border-radius: 14px;
    background: hsl(var(--primary)); color: hsl(var(--primary-foreground));
    font-size: 13px; font-weight: 700; text-decoration: none; transition: opacity .12s;
  }
  .btn-primary:hover { opacity: .88; }

  .empty {
    text-align: center; padding: 48px; color: hsl(var(--muted-foreground)); font-size: 14px;
  }

  @media (max-width: 640px) {
    .modal-poster { display: none; }
    .cal-month-label { min-width: 150px; font-size: 1rem; }
    .cell { min-height: 80px; }
  }
</style>
