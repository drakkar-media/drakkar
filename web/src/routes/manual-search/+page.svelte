<script lang="ts">
  /**
   * Provides free-text NZBHydra2 search outside the normal candidate pipeline.
   *
   * Lets an operator search for any release by title, ranks results by score,
   * and links directly to each result's NZB. Seeds the query from the `q` URL
   * parameter on mount so results can be linked to directly.
   */
  import { onMount } from 'svelte';
  import Search from '@lucide/svelte/icons/search';
  import Download from '@lucide/svelte/icons/download';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import * as Table from '$lib/components/ui/table/index.js';
  import { api } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';
  import { bytes } from '$lib/format';

  type Item = {
    title: string; externalUrl: string; indexer: string;
    sizeBytes: number; score: number; resolution?: string;
    source?: string; codec?: string; audio?: string; hdr?: string;
  };

  let query = '';
  let results: Item[] = [];
  let loading = false;
  let searched = false;
  let queryInput: HTMLInputElement | null = null;

  async function doSearch() {
    if (!query.trim()) return;
    loading = true; searched = false; results = [];
    try {
      const r = await api.manualSearch(query.trim());
      results = (r.items ?? []).sort((a, b) => b.score - a.score);
      searched = true;
    } catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
    finally { loading = false; }
  }

  function scoreTone(s: number) {
    if (s >= 600) return 'ok' as const;
    if (s >= 200) return 'neutral' as const;
    return 'warn' as const;
  }

  onMount(() => {
    const q = new URLSearchParams(window.location.search).get('q');
    if (q) { query = q; void doSearch(); }
    queryInput?.focus();
  });
</script>

<svelte:head><title>Manual Search — Drakkar</title></svelte:head>

<PageHeader title="Manual Search" subtitle="Free-text NZBHydra2 search — find any release and open its NZB directly.">
  <StatusPill tone="neutral">{results.length} results</StatusPill>
</PageHeader>

<form class="mb-5 flex items-center gap-2.5" on:submit|preventDefault={doSearch}>
  <div class="relative flex-1">
    <Search size={15} class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground" />
    <input
      bind:this={queryInput}
      bind:value={query}
      placeholder="e.g. The Dark Knight 2008 1080p BluRay"
      class="!h-10 pl-9 text-sm"
    />
  </div>
  <Button kind="primary" type="submit" disabled={loading || !query.trim()}>
    {loading ? 'Searching…' : 'Search Hydra'}
  </Button>
</form>

{#if searched && results.length === 0}
  <div class="rounded-xl border border-border bg-white/[0.02] p-8 text-center text-sm text-muted-foreground">No results for "{query}".</div>
{:else if results.length > 0}
  <Panel title="Candidates" subtitle="Scored against default quality profile. Click NZB to download via NZBHydra.">
    <Table.Root>
      <Table.Header>
        <Table.Row>
          <Table.Head class="text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Title</Table.Head>
          <Table.Head class="text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Indexer</Table.Head>
          <Table.Head class="text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Size</Table.Head>
          <Table.Head class="text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Score</Table.Head>
          <Table.Head class="text-[11px] uppercase tracking-[0.18em] text-muted-foreground"></Table.Head>
        </Table.Row>
      </Table.Header>
      <Table.Body>
        {#each results as item (item.externalUrl)}
          <Table.Row>
            <Table.Cell class="max-w-[380px] whitespace-normal align-top">
              <div class="mb-1 truncate font-medium">{item.title}</div>
              <div class="flex flex-wrap gap-1">
                {#each [item.resolution, item.source, item.codec].filter(Boolean) as t}
                  <span class="rounded-md bg-white/[0.08] px-1.75 py-0.25 font-mono text-[11px] text-muted-foreground">{t}</span>
                {/each}
                {#if item.audio}<span class="rounded-md px-1.75 py-0.25 font-mono text-[11px]" style="background: hsl(271 75% 65% / 0.2); color: hsl(271 75% 82%)">{item.audio}</span>{/if}
                {#if item.hdr && item.hdr !== 'SDR'}<span class="rounded-md px-1.75 py-0.25 font-mono text-[11px]" style="background: hsl(38 96% 55% / 0.2); color: hsl(38 100% 72%)">{item.hdr}</span>{/if}
              </div>
            </Table.Cell>
            <Table.Cell class="font-mono text-xs text-muted-foreground">{item.indexer || '—'}</Table.Cell>
            <Table.Cell class="font-mono text-xs text-muted-foreground">{bytes(item.sizeBytes)}</Table.Cell>
            <Table.Cell><StatusPill tone={scoreTone(item.score)}>{item.score}</StatusPill></Table.Cell>
            <Table.Cell>
              <a href={item.externalUrl} target="_blank" rel="noopener" class="inline-flex items-center gap-1.5 whitespace-nowrap rounded-md border border-border bg-white/[0.05] px-3 py-1.25 text-xs text-foreground transition-colors hover:bg-white/[0.1]">
                <ExternalLink size={13} /> NZB
              </a>
            </Table.Cell>
          </Table.Row>
        {/each}
      </Table.Body>
    </Table.Root>
  </Panel>
{/if}
