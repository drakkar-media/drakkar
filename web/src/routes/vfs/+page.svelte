<script lang="ts">
  /**
   * Displays a directory browser for the mounted FUSE virtual filesystem
   * alongside live active-stream sessions and mount-level metrics.
   *
   * Browsing, metrics, and stream data are refetched together on every
   * navigation and refresh so the sidebar always reflects the currently
   * displayed directory's mount state.
   */
  import { onMount } from 'svelte';
  import Folder from '@lucide/svelte/icons/folder';
  import File from '@lucide/svelte/icons/file';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import Copy from '@lucide/svelte/icons/copy';
  import MonitorPlay from '@lucide/svelte/icons/monitor-play';
  import HardDrive from '@lucide/svelte/icons/hard-drive';
  import Activity from '@lucide/svelte/icons/activity';
  import X from '@lucide/svelte/icons/x';
  import Home from '@lucide/svelte/icons/home';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import * as Table from '$lib/components/ui/table/index.js';
  import { api } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';
  import { bytes as fmtBytes } from '$lib/format';
  import { copyToClipboard } from '$lib/clipboard';

  type VFSEntry = { name: string; path: string; isDir: boolean; size: number };
  type StreamItem = {
    sessionId?: string;
    sessionID?: string;
    fileName?: string;
    filePath?: string;
    currentOffset?: number;
    fileSize?: number;
    fileSizeBytes?: number;
  };

  let currentPath = '/';
  let entries: VFSEntry[] = [];
  let streams: StreamItem[] = [];
  let metrics: Record<string, number> = {};
  let loading = false;

  async function browse(path: string) {
    loading = true;
    try {
      const [listing, nextMetrics, nextStreams] = await Promise.all([
        api.vfs(path),
        api.metrics(),
        api.streams()
      ]);
      entries = listing.entries ?? [];
      currentPath = path;
      metrics = nextMetrics;
      streams = nextStreams.sessions ?? [];
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  // Explicitly depends on currentPath via $: -- calling this as a bare
  // function from the template (as it originally was) doesn't give Svelte's
  // legacy-mode compiler a visible dependency on currentPath, so the
  // breadcrumb silently never re-rendered after navigating into a folder.
  $: crumbs = ((): { label: string; path: string; isHome: boolean }[] => {
    const parts = currentPath.split('/').filter(Boolean);
    const result: { label: string; path: string; isHome: boolean }[] = [
      { label: 'vfs', path: '/', isHome: true }
    ];
    let acc = '';
    for (const part of parts) {
      acc += `/${part}`;
      result.push({ label: part, path: acc, isHome: false });
    }
    return result;
  })();


  async function copyPath(entryPath: string) {
    const full = `/mnt/drakkar/vfs${entryPath === '/' ? '' : entryPath}`;
    try {
      await copyToClipboard(full);
      toastSuccess('Path copied');
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    }
  }

  async function stopStream(sessionId: string) {
    try {
      await api.stopStream(sessionId);
      toastSuccess('Stream stopped');
      void browse(currentPath);
    } catch (e) {
      toastError(e instanceof Error ? e.message : String(e));
    }
  }

  function streamProgress(stream: StreamItem): number {
    const offset = stream.currentOffset ?? 0;
    const size = stream.fileSizeBytes ?? stream.fileSize ?? 0;
    if (!size) return 0;
    return Math.min(100, (offset / size) * 100);
  }

  function streamLabel(stream: StreamItem): string {
    const name = stream.fileName ?? stream.filePath ?? '';
    if (!name) return 'Unknown stream';
    const parts = name.split(/[\\/]/);
    return parts[parts.length - 1] || name;
  }

  $: sorted = [...entries].sort(
    (a, b) => Number(b.isDir) - Number(a.isDir) || a.name.localeCompare(b.name)
  );

  onMount(() => {
    void browse('/');
  });
</script>

<svelte:head><title>VFS — Drakkar</title></svelte:head>

<PageHeader title="VFS Browser" subtitle="Browse the mounted virtual filesystem, inspect paths, and monitor active stream sessions.">
  <Button kind="secondary" on:click={() => browse(currentPath)} disabled={loading}>
    <RefreshCw size={14} class={loading ? 'animate-spin' : ''} />
    Refresh
  </Button>
</PageHeader>

<div class="grid grid-cols-1 items-start gap-5 md:grid-cols-[minmax(0,1fr)_280px]">
  <!-- ── File Browser ─────────────────────────────────────────────── -->
  <div class="flex min-w-0 flex-col gap-3">
    <!-- Toolbar -->
    <div class="flex items-center gap-3 rounded-2xl border border-border bg-card/60 px-4 py-2.5">
      <nav class="flex min-w-0 flex-1 flex-wrap items-center gap-1" aria-label="Directory path">
        {#each crumbs as crumb, i}
          {#if i > 0}
            <span class="flex shrink-0 items-center text-muted-foreground/45" aria-hidden="true"><ChevronRight size={13} /></span>
          {/if}
          <button
            class="inline-flex items-center gap-1.5 whitespace-nowrap rounded-lg border border-transparent px-2.5 py-1 text-sm font-medium transition-colors {crumb.path === currentPath ? 'cursor-default border-primary bg-primary text-primary-foreground' : 'text-muted-foreground hover:bg-white/[0.06] hover:text-foreground'}"
            on:click={() => browse(crumb.path)}
            aria-current={crumb.path === currentPath ? 'page' : undefined}
          >
            {#if crumb.isHome}<Home size={13} />{/if}
            {crumb.label}
          </button>
        {/each}
      </nav>
    </div>

    <!-- File table -->
    <div class="relative overflow-hidden rounded-2xl border border-border bg-card/80">
      {#if loading}
        <div class="loading-shimmer-bar absolute inset-x-0 top-0 z-10 h-0.5" aria-label="Loading"></div>
      {/if}
      <Table.Root>
        <Table.Header>
          <Table.Row class="border-b border-white/[0.06]">
            <Table.Head class="w-full whitespace-nowrap text-[11px] font-semibold uppercase tracking-[0.10em] text-muted-foreground/70">Name</Table.Head>
            <Table.Head class="min-w-20 whitespace-nowrap text-right text-[11px] font-semibold uppercase tracking-[0.10em] text-muted-foreground/70">Size</Table.Head>
            <Table.Head class="min-w-27.5 whitespace-nowrap pr-3 text-right text-[11px] font-semibold uppercase tracking-[0.10em] text-muted-foreground/70">Actions</Table.Head>
          </Table.Row>
        </Table.Header>
        <Table.Body>
          {#each sorted as entry (entry.path)}
            <Table.Row class="group border-b border-white/[0.04] transition-colors last:border-b-0 hover:bg-white/[0.03]">
              <Table.Cell class="p-0 align-middle">
                <div class="flex items-center gap-2.5 px-4 py-2.5">
                  <span class="flex size-7 shrink-0 items-center justify-center rounded-lg {entry.isDir ? 'bg-primary text-primary-foreground' : 'bg-white/[0.04] text-muted-foreground/70'}">
                    {#if entry.isDir}
                      <Folder size={16} />
                    {:else}
                      <File size={16} />
                    {/if}
                  </span>
                  {#if entry.isDir}
                    <button class="cursor-pointer break-all text-left text-sm font-medium leading-snug text-foreground transition-colors hover:text-primary hover:underline hover:underline-offset-[3px]" on:click={() => browse(entry.path)}>
                      {entry.name}
                    </button>
                  {:else}
                    <span class="cursor-default break-all text-sm font-medium leading-snug text-foreground">{entry.name}</span>
                  {/if}
                </div>
              </Table.Cell>
              <Table.Cell class="p-0 align-middle">
                <span class="block whitespace-nowrap px-4 text-right text-sm tabular-nums text-muted-foreground">
                  {entry.isDir ? '—' : fmtBytes(entry.size)}
                </span>
              </Table.Cell>
              <Table.Cell class="px-4 py-2.5 pr-3 text-right align-middle">
                <button
                  class="inline-flex items-center gap-1.5 whitespace-nowrap rounded-lg border border-border bg-white/[0.05] px-2.5 py-1 text-xs font-medium text-muted-foreground opacity-0 transition-colors group-hover:opacity-100 hover:border-primary hover:bg-primary hover:text-primary-foreground"
                  title="Copy VFS path"
                  on:click={() => copyPath(entry.path)}
                >
                  <Copy size={13} />
                  <span>Copy path</span>
                </button>
              </Table.Cell>
            </Table.Row>
          {/each}

          {#if !loading && sorted.length === 0}
            <Table.Row>
              <Table.Cell colspan={3} class="whitespace-normal">
                <div class="flex flex-col items-center gap-2.5 px-4 py-13 text-center text-muted-foreground/50">
                  <Folder size={32} />
                  <p class="m-0 text-sm">This directory is empty</p>
                </div>
              </Table.Cell>
            </Table.Row>
          {/if}
        </Table.Body>
      </Table.Root>
    </div>
  </div>

  <!-- ── Sidebar ─────────────────────────────────────────────────── -->
  <aside class="flex flex-col gap-3.5">
    <!-- Mount metrics -->
    <Panel flush>
      <svelte:fragment slot="actions" />
      <div class="flex flex-col gap-3 px-4 py-4">
        <div class="flex items-center gap-1.5 text-[11px] font-bold uppercase tracking-[0.1em] text-muted-foreground/70">
          <HardDrive size={14} />
          <span>Mount</span>
        </div>
        <div class="flex flex-col gap-0.5">
          <div class="flex items-baseline justify-between gap-2 rounded-lg bg-white/[0.025] px-3 py-2 hover:bg-white/[0.04]">
            <span class="whitespace-nowrap text-xs text-muted-foreground">Mount path</span>
            <code class="break-all text-right text-sm font-semibold font-mono">/mnt/drakkar/vfs</code>
          </div>
          <div class="flex items-baseline justify-between gap-2 rounded-lg bg-white/[0.025] px-3 py-2 hover:bg-white/[0.04]">
            <span class="whitespace-nowrap text-xs text-muted-foreground">Active streams</span>
            <span class="text-right text-sm font-semibold text-primary">{metrics.active_streams ?? 0}</span>
          </div>
          <div class="flex items-baseline justify-between gap-2 rounded-lg bg-white/[0.025] px-3 py-2 hover:bg-white/[0.04]">
            <span class="whitespace-nowrap text-xs text-muted-foreground">Active NNTP</span>
            <span class="text-right text-sm font-semibold">{metrics.active_nntp_connections ?? 0}</span>
          </div>
          <div class="flex items-baseline justify-between gap-2 rounded-lg bg-white/[0.025] px-3 py-2 hover:bg-white/[0.04]">
            <span class="whitespace-nowrap text-xs text-muted-foreground">Idle NNTP</span>
            <span class="text-right text-sm font-semibold">{metrics.idle_nntp_connections ?? 0}</span>
          </div>
          <div class="flex items-baseline justify-between gap-2 rounded-lg bg-white/[0.025] px-3 py-2 hover:bg-white/[0.04]">
            <span class="whitespace-nowrap text-xs text-muted-foreground">Cache used</span>
            <span class="text-right text-sm font-semibold">{fmtBytes(metrics.disk_cache_used_bytes ?? 0)}</span>
          </div>
        </div>
      </div>
    </Panel>

    <!-- Active stream sessions -->
    <Panel flush>
      <div class="flex flex-col gap-3 px-4 py-4">
        <div class="flex items-center gap-1.5 text-[11px] font-bold uppercase tracking-[0.1em] text-muted-foreground/70">
          <Activity size={14} />
          <span>Streams</span>
          {#if streams.length > 0}
            <span class="inline-flex h-4.5 min-w-4.5 items-center justify-center rounded-full bg-primary px-1.5 text-[10px] font-bold normal-case tracking-normal text-primary-foreground">{streams.length}</span>
          {/if}
        </div>

        {#if streams.length === 0}
          <div class="flex flex-col items-center gap-2 py-7 pb-3 text-center text-muted-foreground/40">
            <MonitorPlay size={24} />
            <p class="m-0 text-sm">No active sessions</p>
          </div>
        {:else}
          <div class="flex flex-col gap-2">
            {#each streams as stream}
              {@const sid = stream.sessionId ?? stream.sessionID ?? ''}
              {@const pct = streamProgress(stream)}
              {@const totalSize = stream.fileSizeBytes ?? stream.fileSize ?? 0}
              <div class="flex flex-col gap-2 rounded-xl border border-white/[0.07] bg-white/[0.03] px-3 py-2.75">
                <div class="flex items-start gap-2">
                  <span class="flex-1 break-words text-xs font-semibold leading-snug" title={stream.fileName ?? stream.filePath ?? ''}>
                    {streamLabel(stream)}
                  </span>
                  {#if sid}
                    <button
                      class="flex size-5.5 shrink-0 items-center justify-center rounded-md border transition-colors"
                      style="border-color: hsl(var(--status-failed) / 0.25); background: hsl(var(--status-failed) / 0.08); color: hsl(var(--status-failed) / 0.8)"
                      title="Stop stream"
                      on:click={() => stopStream(sid)}
                    >
                      <X size={12} />
                    </button>
                  {/if}
                </div>

                <div class="flex flex-col gap-1">
                  <div class="h-1 overflow-hidden rounded-full bg-white/[0.08]">
                    <div class="h-full min-w-[3px] rounded-full bg-gradient-to-r from-primary/70 to-primary transition-[width] duration-300" style="width: {pct}%"></div>
                  </div>
                  <div class="flex justify-between text-[10px] tabular-nums text-muted-foreground/60">
                    <span>{fmtBytes(stream.currentOffset ?? 0)}</span>
                    <span>{fmtBytes(totalSize)}</span>
                  </div>
                </div>

                {#if sid}
                  <div class="overflow-hidden truncate font-mono text-[10px] text-muted-foreground/45">{sid.slice(0, 20)}&hellip;</div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </Panel>
  </aside>
</div>

