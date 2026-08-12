<script lang="ts">
  /**
   * Displays a monthly calendar of library movies and episodes by release/air date.
   *
   * Fetched months are cached in memory (`cache`): switching months shows cached
   * data immediately with no loading flicker, while the previous/next months are
   * preloaded in the background so navigation feels instant.
   */
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
    seasonNumber?: number; episodeNumber?: number; episodeTitle?: string;
  };

  type GridDay = {
    date: string; day: number; inMonth: boolean; isToday: boolean; entries: Entry[];
  };

  const TYPE_STYLE: Record<string, string> = {
    movie:   'border-cyan-400/35 bg-cyan-400/15 text-cyan-200',
    episode: 'border-violet-400/35 bg-violet-400/15 text-violet-200',
    tv:      'border-emerald-400/35 bg-emerald-400/15 text-emerald-200',
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

  // Short "SxEy" form for the compact grid cell.
  function episodeCode(e: Entry): string {
    if (e.type !== 'tv' || !e.seasonNumber || !e.episodeNumber) return '';
    return `S${e.seasonNumber}E${e.episodeNumber}`;
  }

  // Fuller "SxEy · Episode Name" form for the detail modal.
  function episodeLabel(e: Entry): string {
    const code = episodeCode(e);
    if (!code) return '';
    return e.episodeTitle ? `${code} · ${e.episodeTitle}` : code;
  }

  function buildGrid(key: string, entries: Entry[]): GridDay[] {
    const [year, month] = key.split('-').map(Number);
    const first = new Date(Date.UTC(year, month - 1, 1));
    const last  = new Date(Date.UTC(year, month, 0));
    const startDay = first.getUTCDay();
    const gridStart = new Date(Date.UTC(year, month - 1, 1 - startDay));
    // Deliberately local date parts, not toISOString() (which converts to
    // UTC first): every grid cell below is a plain calendar date with no
    // timezone (built from Date.UTC/getUTCDate to match release dates,
    // which are just "YYYY-MM-DD"), so "today" must be the user's own
    // local calendar date too. toISOString() briefly disagreed with it
    // for ~2 hours after each local midnight in any UTC+ timezone (e.g.
    // Netherlands/CEST) -- still showing the previous day as "today".
    const now = new Date();
    const todayISO = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;

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
<div class="mb-4 flex flex-wrap items-center justify-between gap-3">
  <div class="flex items-center gap-2.5">
    <button class="grid size-9.5 place-items-center rounded-xl border border-border bg-white/[0.04] text-foreground transition-colors hover:bg-white/[0.1]" on:click={prev} aria-label="Previous month"><ChevronLeft size={18} /></button>
    <span class="min-w-47.5 text-center text-[1.1rem] font-bold">{label}</span>
    <button class="grid size-9.5 place-items-center rounded-xl border border-border bg-white/[0.04] text-foreground transition-colors hover:bg-white/[0.1]" on:click={next} aria-label="Next month"><ChevronRight size={18} /></button>
    <Button kind="secondary" on:click={today} disabled={loading}>Today</Button>
  </div>
  <div class="flex flex-wrap gap-2">
    <button
      class="inline-flex items-center gap-1.75 rounded-full border border-border px-3.5 py-1.75 text-sm font-semibold transition-colors {filters.movie ? 'bg-white/[0.08] text-foreground' : 'bg-white/[0.04] text-muted-foreground'}"
      on:click={() => (filters = { ...filters, movie: !filters.movie })} type="button"
    >
      <span class="size-2.25 shrink-0 rounded-full bg-cyan-400 transition-opacity {filters.movie ? '' : 'opacity-30'}"></span>
      <span>{movieCount} Movie{movieCount !== 1 ? 's' : ''}</span>
    </button>
    <button
      class="inline-flex items-center gap-1.75 rounded-full border border-border px-3.5 py-1.75 text-sm font-semibold transition-colors {filters.episode ? 'bg-white/[0.08] text-foreground' : 'bg-white/[0.04] text-muted-foreground'}"
      on:click={() => (filters = { ...filters, episode: !filters.episode })} type="button"
    >
      <span class="size-2.25 shrink-0 rounded-full bg-violet-400 transition-opacity {filters.episode ? '' : 'opacity-30'}"></span>
      <span>{episodeCount} Episode{episodeCount !== 1 ? 's' : ''}</span>
    </button>
  </div>
</div>

<!-- Calendar grid -->
<div class="overflow-hidden rounded-[22px] border border-border bg-card/60 transition-opacity {loading ? 'pointer-events-none opacity-65' : ''}">
  <div class="grid grid-cols-7 border-b border-white/[0.06] bg-background/40">
    {#each ['Sun','Mon','Tue','Wed','Thu','Fri','Sat'] as d}
      <div class="px-1 py-2.5 text-center text-[10px] font-bold uppercase tracking-[0.14em] text-muted-foreground">{d}</div>
    {/each}
  </div>

  {#if loading && entries.length === 0}
    <!-- Skeleton while first load -->
    <div class="grid grid-cols-7">
      {#each Array(42) as _, i}
        <div class="flex min-h-27.5 flex-col gap-1 border-b border-r border-white/5 p-2 [&:nth-child(7n)]:border-r-0 {i < 2 || i > 30 ? 'bg-background/25' : ''}">
          <div class="mb-0.5 flex items-center justify-between"><span class="animate-pulse block h-3 w-4.5 rounded-md bg-white/[0.06]"></span></div>
          {#if Math.random() > 0.72}
            <div class="animate-pulse block h-6 w-full rounded-lg bg-white/[0.06]"></div>
          {/if}
        </div>
      {/each}
    </div>
  {:else}
    <div class="grid grid-cols-7">
      {#each grid as day (day.date)}
        <div
          class="flex min-h-27.5 flex-col gap-1 border-b border-r border-white/5 p-2 [&:nth-child(7n)]:border-r-0 {day.inMonth ? '' : 'bg-background/25'}"
          style={day.isToday ? 'background: color-mix(in oklch, var(--primary) 7%, transparent); box-shadow: inset 0 0 0 1px color-mix(in oklch, var(--primary) 25%, transparent);' : undefined}
        >
          <div class="mb-0.5 flex items-center justify-between">
            <span
              class="text-xs font-bold leading-none {day.isToday ? 'rounded-full bg-primary px-1.5 py-0.5 text-[11px] text-primary-foreground' : 'text-muted-foreground'} {!day.inMonth && !day.isToday ? 'opacity-35' : ''}"
            >{day.day}</span>
            {#if day.entries.length > 0}
              <span class="text-[10px] text-muted-foreground">{day.entries.length}</span>
            {/if}
          </div>
          <div class="flex min-w-0 flex-col gap-0.75">
            {#each day.entries.slice(0, 3) as entry (entry.id)}
              <button
                class="flex min-w-0 items-center justify-between gap-1 rounded-lg border px-1.5 py-1 text-left transition-[filter] hover:brightness-[1.18] {TYPE_STYLE[entry.type === 'tv' ? 'episode' : entry.type] ?? ''}"
                title={statusLabel(entry)}
                on:click={() => (selected = entry)}>
                <span class="min-w-0 flex-1 truncate text-[11px] font-bold">
                  {entry.title}{#if episodeCode(entry)}<span class="font-medium opacity-75"> · {episodeCode(entry)}</span>{/if}
                </span>
                {#if entry.available}
                  <span class="size-1.5 shrink-0 rounded-full bg-emerald-400" aria-hidden="true"></span>
                {:else if entry.queueState === 'failed'}
                  <span class="size-1.5 shrink-0 rounded-full bg-red-400" aria-hidden="true"></span>
                {:else if entry.queueState}
                  <span class="size-1.5 shrink-0 rounded-full bg-amber-400" aria-hidden="true"></span>
                {/if}
              </button>
            {/each}
            {#if day.entries.length > 3}
              <button class="rounded border-none bg-none px-1 py-0.5 text-left text-[11px] font-semibold text-muted-foreground hover:text-foreground" on:click={() => (selected = day.entries[3])}>
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
  <div class="p-12 text-center text-sm text-muted-foreground">No library items with release dates in {label}.</div>
{/if}

<!-- Detail modal -->
{#if selected}
  {@const s = selected}
  <div
    class="fixed inset-0 z-50 grid place-items-center bg-black/72 p-4 backdrop-blur-[8px]"
    on:click={(e) => e.target === e.currentTarget && (selected = null)}
    on:keydown={(e) => e.key === 'Escape' && (selected = null)}
    role="button"
    tabindex="0"
    aria-label="Close details dialog"
  >
    <div class="w-full max-w-130 overflow-hidden rounded-[28px] border border-white/10 bg-card shadow-[0_40px_80px_hsl(0_0%_0%/0.5)]" role="dialog" aria-modal="true" tabindex="-1">
      <div class="flex max-sm:flex-col">
        {#if s.posterUrl}
          <img class="hidden w-35 shrink-0 border-r border-white/[0.08] object-cover object-top sm:block" src={s.posterUrl} alt={s.title} loading="lazy" />
        {/if}
        <div class="flex min-w-0 flex-1 flex-col gap-4 p-5.5">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <span class="mb-2 inline-block rounded-lg border px-2.5 py-0.75 text-[11px] font-bold {TYPE_STYLE[s.type === 'tv' ? 'episode' : s.type] ?? ''}">
                {s.type === 'movie' ? 'Movie' : 'Episode'}
              </span>
              <h2 class="text-[1.25rem] font-bold leading-tight">{s.title}</h2>
              {#if episodeLabel(s)}
                <p class="mt-0.75 text-sm font-semibold text-muted-foreground">{episodeLabel(s)}</p>
              {/if}
              <p class="mt-1.25 text-sm text-muted-foreground">{longDate(s.releaseDate)}</p>
            </div>
            <button class="grid size-9.5 shrink-0 place-items-center rounded-xl border border-border bg-white/[0.04] text-foreground transition-colors hover:bg-white/[0.1]" on:click={() => (selected = null)} aria-label="Close"><X size={18} /></button>
          </div>

          <div class="flex flex-wrap gap-2">
            <div
              class="inline-flex items-center gap-1.5 rounded-xl border px-3 py-1.5 text-sm font-semibold"
              style={statusTone(s) === 'ok' ? 'background: hsl(142 60% 40% / 0.2); border-color: hsl(142 60% 40% / 0.3); color: hsl(142 70% 70%)'
                : statusTone(s) === 'err' ? 'background: hsl(0 60% 45% / 0.2); border-color: hsl(0 60% 45% / 0.3); color: hsl(0 80% 75%)'
                : statusTone(s) === 'pending' ? 'background: hsl(43 80% 50% / 0.15); border-color: hsl(43 80% 50% / 0.3); color: hsl(43 90% 75%)'
                : 'background: hsl(0 0% 100% / 0.05); border-color: hsl(0 0% 100% / 0.08); color: var(--muted-foreground)'}
            >
              {#if s.available}
                <CheckCircle2 size={13} />
              {:else if s.queueState}
                <Clock size={13} />
              {/if}
              {statusLabel(s)}
            </div>
          </div>

          <div class="mt-auto flex justify-end gap-2.5">
            <Button kind="secondary" on:click={() => (selected = null)}>Close</Button>
            <a class="inline-flex h-10 items-center gap-1.75 rounded-2xl bg-primary px-4 text-sm font-bold text-primary-foreground transition-opacity hover:opacity-88" href="/library/{s.libraryItemId}">
              <ExternalLink size={14} /> Open Details
            </a>
          </div>
        </div>
      </div>
    </div>
  </div>
{/if}
