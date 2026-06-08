<script lang="ts">
  import { onMount } from 'svelte';
  import Search from '@lucide/svelte/icons/search';
  import Download from '@lucide/svelte/icons/download';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
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
  });
</script>

<svelte:head><title>Manual Search — Drakkar</title></svelte:head>

<PageHeader title="Manual Search" subtitle="Free-text NZBHydra2 search — find any release and open its NZB directly.">
  <StatusPill tone="neutral">{results.length} results</StatusPill>
</PageHeader>

<form class="sf" on:submit|preventDefault={doSearch}>
  <div class="si-wrap">
    <Search size={15} />
    <input class="si" bind:value={query} placeholder="e.g. The Dark Knight 2008 1080p BluRay" autofocus />
  </div>
  <Button kind="primary" type="submit" disabled={loading || !query.trim()}>
    {loading ? 'Searching…' : 'Search Hydra'}
  </Button>
</form>

{#if searched && results.length === 0}
  <div class="empty">No results for "{query}".</div>
{:else if results.length > 0}
  <Panel title="Candidates" subtitle="Scored against default quality profile. Click NZB to download via NZBHydra.">
    <div class="tw">
      <table>
        <thead><tr><th>Title</th><th>Indexer</th><th>Size</th><th>Score</th><th></th></tr></thead>
        <tbody>
          {#each results as item (item.externalUrl)}
            <tr>
              <td class="tc">
                <div class="tl">{item.title}</div>
                <div class="tags">
                  {#each [item.resolution, item.source, item.codec].filter(Boolean) as t}
                    <span class="tag">{t}</span>
                  {/each}
                  {#if item.audio}<span class="tag audio">{item.audio}</span>{/if}
                  {#if item.hdr && item.hdr !== 'SDR'}<span class="tag hdr">{item.hdr}</span>{/if}
                </div>
              </td>
              <td class="mono muted small">{item.indexer || '—'}</td>
              <td class="mono muted small">{bytes(item.sizeBytes)}</td>
              <td><StatusPill tone={scoreTone(item.score)}>{item.score}</StatusPill></td>
              <td>
                <a href={item.externalUrl} target="_blank" rel="noopener" class="nzb-btn">
                  <ExternalLink size={13} /> NZB
                </a>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  </Panel>
{/if}

<style>
  .sf { display:flex; gap:10px; margin-bottom:20px; align-items:center; }
  .si-wrap { flex:1; display:flex; align-items:center; gap:10px; height:46px; padding:0 14px;
    border-radius:14px; border:1px solid hsl(0 0% 100%/.1); background:hsl(0 0% 100%/.04);
    color:hsl(var(--muted-foreground)); }
  .si { flex:1; background:transparent; border:none; outline:none; color:hsl(var(--foreground)); font-size:14px; }
  .si::placeholder { color:hsl(var(--muted-foreground)); }
  .tw { overflow-x:auto; }
  table { width:100%; min-width:700px; border-collapse:collapse; }
  th { padding:10px 12px; text-align:left; font-size:11px; text-transform:uppercase; letter-spacing:.12em; color:hsl(var(--muted-foreground)); border-bottom:1px solid hsl(0 0% 100%/.06); }
  td { padding:11px 12px; border-bottom:1px solid hsl(0 0% 100%/.04); vertical-align:middle; font-size:13px; }
  tr:last-child td { border-bottom:none; }
  tr:hover td { background:hsl(0 0% 100%/.03); }
  .tc { max-width:380px; }
  .tl { font-weight:500; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; margin-bottom:4px; }
  .tags { display:flex; flex-wrap:wrap; gap:4px; }
  .tag { padding:1px 7px; border-radius:6px; font-size:11px; font-family:'JetBrains Mono',monospace; background:hsl(0 0% 100%/.08); color:hsl(var(--muted-foreground)); }
  .tag.audio { background:hsl(271 75% 65%/.2); color:hsl(271 75% 82%); }
  .tag.hdr   { background:hsl(38 96% 55%/.2);  color:hsl(38 100% 72%); }
  .mono { font-family:'JetBrains Mono',monospace; }
  .muted { color:hsl(var(--muted-foreground)); }
  .small { font-size:12px; }
  .nzb-btn { display:inline-flex; align-items:center; gap:5px; padding:5px 12px; border-radius:10px; border:1px solid hsl(0 0% 100%/.1); background:hsl(0 0% 100%/.05); color:hsl(var(--foreground)); font-size:12px; text-decoration:none; white-space:nowrap; }
  .nzb-btn:hover { background:hsl(0 0% 100%/.1); }
  .empty { padding:32px; text-align:center; border-radius:18px; border:1px solid hsl(0 0% 100%/.06); color:hsl(var(--muted-foreground)); }
</style>
