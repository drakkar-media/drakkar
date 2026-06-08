<script lang="ts">
  import { onMount } from 'svelte';
  import ChevronLeft from '@lucide/svelte/icons/chevron-left';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import X from '@lucide/svelte/icons/x';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Button from '$lib/components/Button.svelte';
  import { api } from '$lib/api';
  import { toastError } from '$lib/toast';

  type Entry = {
    id: number; libraryItemId: number; type: string; title: string;
    releaseDate: string; tmdbId?: number; available: boolean; queueState?: string;
  };

  type GridDay = {
    date: string; day: number; inMonth: boolean; isToday: boolean; entries: Entry[];
  };

  const TYPE_STYLE: Record<string, string> = {
    movie:   'entry-movie',
    episode: 'entry-episode',
    tv:      'entry-tv',
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
      (byDate.get(e.releaseDate) ?? byDate.set(e.releaseDate, []).get(e.releaseDate)!).push(e);
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

  function prev() { currentKey = shiftMonth(currentKey, -1); void load(); }
  function next() { currentKey = shiftMonth(currentKey, 1); void load(); }
  function today() { currentKey = currentMonthKey(); void load(); }

  async function load() {
    loading = true;
    try {
      const r = await api.releaseCalendar(currentKey);
      entries = r.entries ?? [];
    } catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
    finally { loading = false; }
  }

  onMount(load);
</script>

<svelte:head><title>Calendar — Drakkar</title></svelte:head>

<PageHeader title="Release Calendar" subtitle="Library movies and episodes by theatrical / air date.">
</PageHeader>

<!-- Month navigation -->
<div class="cal-header">
  <div class="cal-nav">
    <button class="icon-btn" on:click={prev} aria-label="Previous month"><ChevronLeft size={18} /></button>
    <span class="cal-month-label">{label}</span>
    <button class="icon-btn" on:click={next} aria-label="Next month"><ChevronRight size={18} /></button>
  </div>
  <Button kind="secondary" on:click={today} disabled={loading}>Today</Button>
</div>

<!-- Type filters -->
<div class="filters">
  <button class="filter-btn" class:on={filters.movie} on:click={() => (filters = { ...filters, movie: !filters.movie })}>
    <span class="filter-dot dot-movie" class:active={filters.movie}></span> Movies
  </button>
  <button class="filter-btn" class:on={filters.episode} on:click={() => (filters = { ...filters, episode: !filters.episode })}>
    <span class="filter-dot dot-episode" class:active={filters.episode}></span> Episodes
  </button>
</div>

<!-- Calendar grid -->
<div class="cal-wrap" class:loading>
  <!-- Day-of-week header -->
  <div class="cal-dow-row">
    {#each ['Sun','Mon','Tue','Wed','Thu','Fri','Sat'] as d}
      <div class="cal-dow">{d}</div>
    {/each}
  </div>
  <!-- Day cells -->
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
              on:click={() => (selected = entry)}>
              <span class="entry-title">{entry.title}</span>
              <span class="entry-sub">{entry.type === 'movie' ? 'Movie' : 'Episode'}</span>
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
</div>

{#if visible.length === 0 && !loading}
  <div class="empty">No library items with release dates in {label}.</div>
{/if}

<!-- Detail modal -->
{#if selected}
  <div class="modal-backdrop" on:click={() => (selected = null)} role="presentation">
    <div class="modal" on:click|stopPropagation role="dialog" aria-modal="true">
      <div class="modal-head">
        <div>
          <span class="modal-badge {TYPE_STYLE[selected.type === 'tv' ? 'episode' : selected.type] ?? ''}">
            {selected.type === 'movie' ? 'Movie' : 'Episode'}
          </span>
          <h2 class="modal-title">{selected.title}</h2>
          <p class="modal-date">{longDate(selected.releaseDate)}</p>
        </div>
        <button class="icon-btn" on:click={() => (selected = null)} aria-label="Close"><X size={18} /></button>
      </div>
      <div class="modal-status">
        <div class="info-line">
          <span class="info-label">Status</span>
          <span class="info-value">{selected.available ? 'Available' : selected.queueState ?? 'Missing'}</span>
        </div>
        <div class="info-line">
          <span class="info-label">Release</span>
          <span class="info-value">{selected.releaseDate}</span>
        </div>
      </div>
      <div class="modal-actions">
        <Button kind="secondary" on:click={() => (selected = null)}>Close</Button>
        <a class="btn-primary" href="/library/{selected.libraryItemId}">Open Details</a>
      </div>
    </div>
  </div>
{/if}

<style>
  .cal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 16px;
    flex-wrap: wrap;
  }

  .cal-nav {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .cal-month-label {
    font-size: 1.15rem;
    font-weight: 700;
    min-width: 200px;
    text-align: center;
  }

  .icon-btn {
    display: grid;
    place-items: center;
    width: 38px; height: 38px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    cursor: pointer;
    transition: background .12s;
  }
  .icon-btn:hover { background: hsl(0 0% 100% / 0.1); }

  .filters {
    display: flex;
    gap: 8px;
    margin-bottom: 16px;
    padding: 14px;
    border-radius: 20px;
    border: 1px solid hsl(0 0% 100% / 0.07);
    background: hsl(var(--card) / 0.8);
  }

  .filter-btn {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 8px 14px;
    border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.1);
    background: hsl(var(--background) / 0.4);
    color: hsl(var(--muted-foreground));
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    transition: all .12s;
  }
  .filter-btn.on { color: hsl(var(--foreground)); }

  .filter-dot {
    width: 10px; height: 10px;
    border-radius: 50%;
    background: hsl(var(--muted-foreground) / 0.4);
    transition: background .12s;
  }
  .dot-movie.active   { background: hsl(186 95% 60%); }
  .dot-episode.active { background: hsl(270 90% 70%); }

  .cal-wrap {
    border-radius: 22px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(var(--card) / 0.6);
    overflow: hidden;
    transition: opacity .15s;
  }
  .cal-wrap.loading { opacity: .6; pointer-events: none; }

  .cal-dow-row {
    display: grid;
    grid-template-columns: repeat(7, minmax(0, 1fr));
    border-bottom: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(var(--background) / 0.4);
  }
  .cal-dow {
    padding: 10px 4px;
    text-align: center;
    font-size: 10px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: .14em;
    color: hsl(var(--muted-foreground));
  }

  .cal-cells {
    display: grid;
    grid-template-columns: repeat(7, minmax(0, 1fr));
  }

  .cell {
    min-height: 120px;
    padding: 8px 6px;
    border-right: 1px solid hsl(0 0% 100% / 0.05);
    border-bottom: 1px solid hsl(0 0% 100% / 0.05);
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .cell:nth-child(7n) { border-right: none; }
  .cell.out { background: hsl(var(--background) / 0.3); }
  .cell.today {
    background: hsl(var(--primary) / 0.07);
    box-shadow: inset 0 0 0 1px hsl(var(--primary) / 0.3);
  }

  .cell-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 2px;
  }

  .cell-day {
    font-size: 12px;
    font-weight: 700;
    color: hsl(var(--muted-foreground));
    line-height: 1;
  }
  .cell.out .cell-day { opacity: 0.4; }
  .today-badge {
    background: hsl(var(--primary));
    color: hsl(var(--primary-foreground));
    padding: 1px 6px;
    border-radius: 99px;
    font-size: 11px;
  }

  .cell-count {
    font-size: 11px;
    color: hsl(var(--muted-foreground));
  }

  .cell-entries { display: flex; flex-direction: column; gap: 3px; min-width: 0; }

  .entry {
    display: block;
    width: 100%;
    padding: 4px 6px;
    border-radius: 8px;
    border: 1px solid transparent;
    text-align: left;
    cursor: pointer;
    transition: filter .1s;
    min-width: 0;
  }
  .entry:hover { filter: brightness(1.15); }
  .entry-title {
    display: block;
    font-size: 11px;
    font-weight: 700;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .entry-sub {
    display: block;
    font-size: 10px;
    opacity: 0.75;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .entry-movie   { border-color: hsl(186 80% 50% / 0.35); background: hsl(186 80% 50% / 0.15); color: hsl(186 95% 85%); }
  .entry-episode { border-color: hsl(270 75% 65% / 0.35); background: hsl(270 75% 65% / 0.15); color: hsl(270 90% 85%); }
  .entry-tv      { border-color: hsl(142 70% 45% / 0.35); background: hsl(142 70% 45% / 0.15); color: hsl(142 80% 75%); }

  .entry-more {
    font-size: 11px;
    font-weight: 600;
    color: hsl(var(--muted-foreground));
    padding: 2px 4px;
    text-align: left;
    cursor: pointer;
    background: none;
    border: none;
  }
  .entry-more:hover { color: hsl(var(--foreground)); }

  /* Modal */
  .modal-backdrop {
    position: fixed;
    inset: 0;
    z-index: 50;
    background: hsl(0 0% 0% / 0.72);
    backdrop-filter: blur(8px);
    display: grid;
    place-items: center;
    padding: 16px;
  }

  .modal {
    width: 100%;
    max-width: 440px;
    border-radius: 28px;
    border: 1px solid hsl(0 0% 100% / 0.1);
    background: hsl(var(--card));
    padding: 22px;
    box-shadow: 0 40px 80px hsl(0 0% 0% / 0.5);
  }

  .modal-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 18px;
  }

  .modal-badge {
    display: inline-block;
    padding: 3px 10px;
    border-radius: 8px;
    font-size: 11px;
    font-weight: 700;
    margin-bottom: 8px;
  }

  .modal-title { font-size: 1.35rem; font-weight: 700; line-height: 1.2; }
  .modal-date { font-size: 13px; color: hsl(var(--muted-foreground)); margin-top: 4px; }

  .modal-status {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
    margin-bottom: 18px;
  }

  .info-line {
    padding: 10px 12px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.07);
    background: hsl(var(--background) / 0.5);
  }
  .info-label {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: .15em;
    color: hsl(var(--muted-foreground));
    display: block;
    margin-bottom: 3px;
  }
  .info-value { font-size: 13px; font-weight: 600; }

  .modal-actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
  }

  .btn-primary {
    display: inline-flex;
    align-items: center;
    padding: 0 16px;
    height: 42px;
    border-radius: 14px;
    background: hsl(var(--primary));
    color: hsl(var(--primary-foreground));
    font-size: 14px;
    font-weight: 700;
    text-decoration: none;
    transition: opacity .12s;
  }
  .btn-primary:hover { opacity: .9; }

  .empty {
    padding: 40px;
    text-align: center;
    border-radius: 18px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    color: hsl(var(--muted-foreground));
    margin-top: 12px;
  }

  @media (max-width: 640px) {
    .cell { min-height: 70px; padding: 4px 3px; }
    .cal-month-label { min-width: 160px; font-size: 1rem; }
    .entry-sub { display: none; }
    .entry { padding: 3px 4px; }
  }
</style>
