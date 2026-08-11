<script lang="ts">
  /**
   * Manages quality profiles used to rank and filter release candidates.
   *
   * Lists existing profiles in a sidebar and edits the selected one — ranked
   * resolution/source/codec/audio/HDR preference lists, language chips,
   * release flags, upgrade rules, and size limits — saving or deleting via the
   * profiles API. Deleting a profile requires confirmation and is disallowed
   * for the default profile.
   */
  import { onMount } from 'svelte';
  import Plus from '@lucide/svelte/icons/plus';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import Save from '@lucide/svelte/icons/save';
  import ChevronUp from '@lucide/svelte/icons/chevron-up';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import Star from '@lucide/svelte/icons/star';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api } from '$lib/api';
  import { toastError } from '$lib/toast';
  import { runAction, confirmed } from '$lib/actions';
  import type { QualityProfile } from '$lib/types';

  const ALL_RESOLUTIONS = ['2160p', '1080p', '720p', '576p', '480p'];
  const ALL_SOURCES     = ['BluRay', 'Remux', 'WEB-DL', 'WEBRip', 'HDTV', 'DVDRip'];
  const ALL_CODECS      = ['x265', 'HEVC', 'x264', 'AVC', 'AV1', 'VP9'];
  const ALL_LANGUAGES   = ['nl', 'en', 'de', 'fr', 'es', 'pt', 'it', 'ja', 'ko', 'zh', 'multi'];
  const ALL_AUDIO       = ['Atmos', 'TrueHD', 'DTS-HD', 'DTS', 'DD+', 'AC3', 'AAC', 'FLAC', 'MP3'];
  const ALL_HDR         = ['DV', 'HDR10+', 'HDR10', 'HLG', 'SDR'];

  let profiles: QualityProfile[] = [];
  let selected: QualityProfile | null = null;
  let loading = true;
  let saving = false;

  function blankProfile(): QualityProfile {
    return {
      name: 'New Profile',
      isDefault: false,
      resolutions: ['1080p', '2160p', '720p'],
      sources: ['WEB-DL', 'BluRay', 'WEBRip'],
      codecs: ['x265', 'x264'],
      languages: ['nl', 'en'],
      audioFormats: ['TrueHD', 'DTS-HD', 'DTS', 'DD+', 'AC3', 'AAC'],
      hdrFormats: ['HDR10', 'SDR'],
      excludePatterns: [],
      preferProper: true,
      preferRepack: true,
      rejectCam: true,
      allowUpgrade: false,
      minimumUpgradeCustomFormatScore: 0,
      cutoffResolution: '',
      minimumAgeHours: 0,
      minMbPerMinute: 0,
      maxMbPerMinute: 0,
    };
  }

  async function load() {
    loading = true;
    try {
      const r = await api.listProfiles();
      profiles = r.profiles ?? [];
      if (!selected && profiles.length) selected = { ...profiles[0] };
    } catch (err) { toastError(err instanceof Error ? err.message : String(err)); }
    finally { loading = false; }
  }

  function selectProfile(p: QualityProfile) { selected = { ...p }; }

  async function save() {
    if (!selected) return;
    const toSave = selected;
    await runAction(() => api.saveProfile(toSave), {
      setWorking: (v) => (saving = v),
      successMessage: (saved) => `Profile "${saved.name}" saved`,
      afterSuccess: async (saved) => {
        await load();
        const found = profiles.find(p => p.name === saved.name);
        if (found) selected = { ...found };
      }
    });
  }

  let deleting = false;

  async function deleteProfile(p: QualityProfile) {
    if (!p.id || p.isDefault || deleting) return;
    if (!confirmed(`Delete profile "${p.name}"?`)) return;
    const id = p.id;
    await runAction(() => api.deleteProfile(id), {
      setWorking: (v) => (deleting = v),
      successMessage: () => `Profile "${p.name}" deleted`,
      afterSuccess: async () => {
        if (selected?.id === p.id) selected = null;
        await load();
      }
    });
  }

  // Ordered list reorder helpers
  function moveUp(arr: string[], i: number): string[] {
    if (i === 0) return arr;
    const n = [...arr];
    [n[i - 1], n[i]] = [n[i], n[i - 1]];
    return n;
  }
  function moveDown(arr: string[], i: number): string[] {
    if (i >= arr.length - 1) return arr;
    const n = [...arr];
    [n[i], n[i + 1]] = [n[i + 1], n[i]];
    return n;
  }
  function toggleOrdered(arr: string[], value: string): string[] {
    return arr.includes(value) ? arr.filter(v => v !== value) : [...arr, value];
  }

  onMount(load);
</script>

<svelte:head><title>Profiles — Drakkar</title></svelte:head>

<PageHeader title="Quality Profiles" subtitle="Configure resolution, source, codec, audio, and HDR ranking preferences for release selection.">
  <Button kind="secondary" on:click={() => (selected = blankProfile())}>
    <Plus size={16} />
    New Profile
  </Button>
</PageHeader>

<div class="grid grid-cols-1 items-start gap-4 lg:grid-cols-[240px_minmax(0,1fr)]">
  <!-- Sidebar list -->
  <aside class="grid gap-2 lg:sticky lg:top-22">
    {#each profiles as p (p.id ?? p.name)}
      <button
        class="grid gap-0.75 rounded-2xl border px-3.5 py-3 text-left transition-colors {selected?.id === p.id ? 'border-primary/28 bg-primary/12' : 'border-white/[0.06] bg-white/[0.03] hover:border-primary/28 hover:bg-primary/12'}"
        on:click={() => selectProfile(p)}
        type="button"
      >
        <div class="flex items-center gap-1.5 text-sm font-semibold">
          {#if p.isDefault}<Star size={12} class="text-primary" />{/if}
          {p.name}
        </div>
        <div class="font-mono text-[11px] text-muted-foreground">{p.resolutions.slice(0, 2).join(', ')}</div>
      </button>
    {/each}
    {#if profiles.length === 0 && !loading}
      <div class="px-3.5 py-2 text-sm text-muted-foreground">No profiles yet.</div>
    {/if}
  </aside>

  <!-- Editor panel -->
  {#if selected}
    <div class="grid">
      <Panel title={selected.id ? `Edit: ${selected.name}` : 'New Profile'} subtitle="Settings control how releases are ranked and filtered.">
        <div slot="actions">
          {#if selected.isDefault}<StatusPill tone="ok">Default</StatusPill>{/if}
        </div>

        <!-- Name -->
        <div class="mb-5">
          <label class="mb-2.5 block text-sm font-semibold" for="pname">Profile Name</label>
          <input id="pname" class="w-full" bind:value={selected.name} placeholder="e.g. Movie HD" />
        </div>

        <div class="my-5 h-px bg-white/[0.06]"></div>

        <!-- Resolutions (ordered) -->
        <div class="mb-5">
          <div class="mb-2.5 flex items-baseline gap-2 text-sm font-semibold">Resolutions <span class="text-[11px] font-normal text-muted-foreground">drag to re-rank</span></div>
          <div class="grid gap-1.5">
            {#each selected.resolutions as res, i}
              <div class="flex items-center gap-2 rounded-[10px] border border-white/[0.06] bg-white/[0.03] px-2.5 py-2">
                <span class="min-w-5.5 font-mono text-[11px] font-bold text-primary">{i + 1}</span>
                <span class="flex-1 font-mono text-sm">{res}</span>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-muted-foreground hover:bg-white/[0.08] hover:text-foreground disabled:opacity-30" aria-label={`Move ${res} up`} on:click={() => { selected = { ...selected!, resolutions: moveUp(selected!.resolutions, i) }; }} disabled={i === 0}>
                  <ChevronUp size={13} />
                </button>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-muted-foreground hover:bg-white/[0.08] hover:text-foreground disabled:opacity-30" aria-label={`Move ${res} down`} on:click={() => { selected = { ...selected!, resolutions: moveDown(selected!.resolutions, i) }; }} disabled={i === selected.resolutions.length - 1}>
                  <ChevronDown size={13} />
                </button>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-xs text-muted-foreground transition-colors hover:bg-[hsl(var(--status-failed)/0.15)] hover:text-[hsl(var(--status-failed))]" aria-label={`Remove ${res}`} on:click={() => { selected = { ...selected!, resolutions: selected!.resolutions.filter(v => v !== res) }; }}>✕</button>
              </div>
            {/each}
            <div class="mt-2 flex flex-wrap gap-1.5">
              {#each ALL_RESOLUTIONS.filter(r => !selected!.resolutions.includes(r)) as r}
                <button type="button" class="rounded-[10px] border border-dashed border-white/[0.08] bg-white/[0.04] px-3 py-1.25 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-white/[0.08] hover:text-foreground" on:click={() => { selected = { ...selected!, resolutions: [...selected!.resolutions, r] }; }}>{r} +</button>
              {/each}
            </div>
          </div>
        </div>

        <!-- Sources (ordered) -->
        <div class="mb-5">
          <div class="mb-2.5 flex items-baseline gap-2 text-sm font-semibold">Sources <span class="text-[11px] font-normal text-muted-foreground">rank by priority</span></div>
          <div class="grid gap-1.5">
            {#each selected.sources as src, i}
              <div class="flex items-center gap-2 rounded-[10px] border border-white/[0.06] bg-white/[0.03] px-2.5 py-2">
                <span class="min-w-5.5 font-mono text-[11px] font-bold text-primary">{i + 1}</span>
                <span class="flex-1 font-mono text-sm">{src}</span>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-muted-foreground hover:bg-white/[0.08] hover:text-foreground disabled:opacity-30" aria-label={`Move ${src} up`} on:click={() => { selected = { ...selected!, sources: moveUp(selected!.sources, i) }; }} disabled={i === 0}><ChevronUp size={13} /></button>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-muted-foreground hover:bg-white/[0.08] hover:text-foreground disabled:opacity-30" aria-label={`Move ${src} down`} on:click={() => { selected = { ...selected!, sources: moveDown(selected!.sources, i) }; }} disabled={i === selected.sources.length - 1}><ChevronDown size={13} /></button>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-xs text-muted-foreground transition-colors hover:bg-[hsl(var(--status-failed)/0.15)] hover:text-[hsl(var(--status-failed))]" aria-label={`Remove ${src}`} on:click={() => { selected = { ...selected!, sources: selected!.sources.filter(v => v !== src) }; }}>✕</button>
              </div>
            {/each}
            <div class="mt-2 flex flex-wrap gap-1.5">
              {#each ALL_SOURCES.filter(s => !selected!.sources.includes(s)) as s}
                <button type="button" class="rounded-[10px] border border-dashed border-white/[0.08] bg-white/[0.04] px-3 py-1.25 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-white/[0.08] hover:text-foreground" on:click={() => { selected = { ...selected!, sources: [...selected!.sources, s] }; }}>{s} +</button>
              {/each}
            </div>
          </div>
        </div>

        <!-- Codecs (ordered) -->
        <div class="mb-5">
          <div class="mb-2.5 flex items-baseline gap-2 text-sm font-semibold">Codecs <span class="text-[11px] font-normal text-muted-foreground">rank by priority</span></div>
          <div class="grid gap-1.5">
            {#each selected.codecs as c, i}
              <div class="flex items-center gap-2 rounded-[10px] border border-white/[0.06] bg-white/[0.03] px-2.5 py-2">
                <span class="min-w-5.5 font-mono text-[11px] font-bold text-primary">{i + 1}</span>
                <span class="flex-1 font-mono text-sm">{c}</span>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-muted-foreground hover:bg-white/[0.08] hover:text-foreground disabled:opacity-30" aria-label={`Move ${c} up`} on:click={() => { selected = { ...selected!, codecs: moveUp(selected!.codecs, i) }; }} disabled={i === 0}><ChevronUp size={13} /></button>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-muted-foreground hover:bg-white/[0.08] hover:text-foreground disabled:opacity-30" aria-label={`Move ${c} down`} on:click={() => { selected = { ...selected!, codecs: moveDown(selected!.codecs, i) }; }} disabled={i === selected.codecs.length - 1}><ChevronDown size={13} /></button>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-xs text-muted-foreground transition-colors hover:bg-[hsl(var(--status-failed)/0.15)] hover:text-[hsl(var(--status-failed))]" aria-label={`Remove ${c}`} on:click={() => { selected = { ...selected!, codecs: selected!.codecs.filter(v => v !== c) }; }}>✕</button>
              </div>
            {/each}
            <div class="mt-2 flex flex-wrap gap-1.5">
              {#each ALL_CODECS.filter(c => !selected!.codecs.includes(c)) as c}
                <button type="button" class="rounded-[10px] border border-dashed border-white/[0.08] bg-white/[0.04] px-3 py-1.25 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-white/[0.08] hover:text-foreground" on:click={() => { selected = { ...selected!, codecs: [...selected!.codecs, c] }; }}>{c} +</button>
              {/each}
            </div>
          </div>
        </div>

        <div class="my-5 h-px bg-white/[0.06]"></div>

        <!-- Audio formats (ordered — new) -->
        <div class="mb-5">
          <div class="mb-2.5 flex items-baseline gap-2 text-sm font-semibold">Audio Formats <span class="text-[11px] font-normal text-muted-foreground">rank by priority — top scores highest</span></div>
          <div class="grid gap-1.5">
            {#each selected.audioFormats as a, i}
              <div class="flex items-center gap-2 rounded-[10px] border border-white/[0.06] bg-white/[0.03] px-2.5 py-2">
                <span class="min-w-5.5 font-mono text-[11px] font-bold text-primary">{i + 1}</span>
                <span class="flex-1 font-mono text-sm">{a}</span>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-muted-foreground hover:bg-white/[0.08] hover:text-foreground disabled:opacity-30" aria-label={`Move ${a} up`} on:click={() => { selected = { ...selected!, audioFormats: moveUp(selected!.audioFormats, i) }; }} disabled={i === 0}><ChevronUp size={13} /></button>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-muted-foreground hover:bg-white/[0.08] hover:text-foreground disabled:opacity-30" aria-label={`Move ${a} down`} on:click={() => { selected = { ...selected!, audioFormats: moveDown(selected!.audioFormats, i) }; }} disabled={i === selected.audioFormats.length - 1}><ChevronDown size={13} /></button>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-xs text-muted-foreground transition-colors hover:bg-[hsl(var(--status-failed)/0.15)] hover:text-[hsl(var(--status-failed))]" aria-label={`Remove ${a}`} on:click={() => { selected = { ...selected!, audioFormats: selected!.audioFormats.filter(v => v !== a) }; }}>✕</button>
              </div>
            {/each}
            <div class="mt-2 flex flex-wrap gap-1.5">
              {#each ALL_AUDIO.filter(a => !selected!.audioFormats.includes(a)) as a}
                <button type="button" class="rounded-[10px] border border-dashed border-white/[0.08] bg-white/[0.04] px-3 py-1.25 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-white/[0.08] hover:text-foreground" on:click={() => { selected = { ...selected!, audioFormats: [...selected!.audioFormats, a] }; }}>{a} +</button>
              {/each}
            </div>
          </div>
        </div>

        <!-- HDR formats (ordered — new) -->
        <div class="mb-5">
          <div class="mb-2.5 flex items-baseline gap-2 text-sm font-semibold">HDR Formats <span class="text-[11px] font-normal text-muted-foreground">rank by priority</span></div>
          <div class="grid gap-1.5">
            {#each selected.hdrFormats as h, i}
              <div class="flex items-center gap-2 rounded-[10px] border border-white/[0.06] bg-white/[0.03] px-2.5 py-2">
                <span class="min-w-5.5 font-mono text-[11px] font-bold text-primary">{i + 1}</span>
                <span class="flex-1 font-mono text-sm">{h}</span>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-muted-foreground hover:bg-white/[0.08] hover:text-foreground disabled:opacity-30" aria-label={`Move ${h} up`} on:click={() => { selected = { ...selected!, hdrFormats: moveUp(selected!.hdrFormats, i) }; }} disabled={i === 0}><ChevronUp size={13} /></button>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-muted-foreground hover:bg-white/[0.08] hover:text-foreground disabled:opacity-30" aria-label={`Move ${h} down`} on:click={() => { selected = { ...selected!, hdrFormats: moveDown(selected!.hdrFormats, i) }; }} disabled={i === selected.hdrFormats.length - 1}><ChevronDown size={13} /></button>
                <button type="button" class="grid size-6.5 place-items-center rounded-md border border-white/[0.06] bg-transparent text-xs text-muted-foreground transition-colors hover:bg-[hsl(var(--status-failed)/0.15)] hover:text-[hsl(var(--status-failed))]" aria-label={`Remove ${h}`} on:click={() => { selected = { ...selected!, hdrFormats: selected!.hdrFormats.filter(v => v !== h) }; }}>✕</button>
              </div>
            {/each}
            <div class="mt-2 flex flex-wrap gap-1.5">
              {#each ALL_HDR.filter(h => !selected!.hdrFormats.includes(h)) as h}
                <button type="button" class="rounded-[10px] border border-dashed border-white/[0.08] bg-white/[0.04] px-3 py-1.25 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-white/[0.08] hover:text-foreground" on:click={() => { selected = { ...selected!, hdrFormats: [...selected!.hdrFormats, h] }; }}>{h} +</button>
              {/each}
            </div>
          </div>
        </div>

        <div class="my-5 h-px bg-white/[0.06]"></div>

        <!-- Languages (chips) -->
        <div class="mb-5">
          <div class="mb-2.5 text-sm font-semibold">Languages</div>
          <div class="flex flex-wrap gap-1.5">
            {#each ALL_LANGUAGES as lang}
              <button
                type="button"
                class="rounded-[10px] border px-3 py-1.25 font-mono text-xs transition-colors {selected.languages.includes(lang) ? 'border-primary bg-primary text-primary-foreground' : 'border-white/[0.08] bg-white/[0.04] text-muted-foreground hover:bg-white/[0.08] hover:text-foreground'}"
                on:click={() => { selected = { ...selected!, languages: toggleOrdered(selected!.languages, lang) }; }}
              >{lang}</button>
            {/each}
          </div>
        </div>

        <div class="my-5 h-px bg-white/[0.06]"></div>

        <!-- Flags — Radarr/Sonarr style -->
        <div class="mb-5">
          <div class="mb-2.5 text-sm font-semibold">Release Flags</div>
          <div class="grid gap-2.5">
            <label class="flex items-start gap-3 rounded-xl border border-white/[0.06] bg-white/[0.03] px-3.5 py-3 cursor-pointer">
              <input type="checkbox" class="mt-0.5 shrink-0" bind:checked={selected.preferProper} />
              <div>
                <strong class="block text-sm mb-0.5">Prefer Proper</strong>
                <span class="block text-xs text-muted-foreground">Boost score when release is marked PROPER</span>
              </div>
            </label>
            <label class="flex items-start gap-3 rounded-xl border border-white/[0.06] bg-white/[0.03] px-3.5 py-3 cursor-pointer">
              <input type="checkbox" class="mt-0.5 shrink-0" bind:checked={selected.preferRepack} />
              <div>
                <strong class="block text-sm mb-0.5">Prefer Repack</strong>
                <span class="block text-xs text-muted-foreground">Boost score when release is marked REPACK</span>
              </div>
            </label>
            <label class="flex items-start gap-3 rounded-xl border border-white/[0.06] bg-white/[0.03] px-3.5 py-3 cursor-pointer">
              <input type="checkbox" class="mt-0.5 shrink-0" bind:checked={selected.rejectCam} />
              <div>
                <strong class="block text-sm mb-0.5">Reject CAM / TS / Telecine</strong>
                <span class="block text-xs text-muted-foreground">Hard-reject low-quality cam captures and telesyncs</span>
              </div>
            </label>
            <label class="flex items-start gap-3 rounded-xl border border-white/[0.06] bg-white/[0.03] px-3.5 py-3 cursor-pointer">
              <input type="checkbox" class="mt-0.5 shrink-0" bind:checked={selected.allowUpgrade} />
              <div>
                <strong class="block text-sm mb-0.5">Allow Quality Upgrade</strong>
                <span class="block text-xs text-muted-foreground">Periodically re-search available items for better releases</span>
              </div>
            </label>
          </div>
        </div>

        <div class="my-5 h-px bg-white/[0.06]"></div>

        <div class="mb-5">
          <div class="mb-2.5 text-sm font-semibold">Upgrade Rules</div>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="grid gap-1.5">
              <span class="text-xs text-muted-foreground">Minimum CF Upgrade</span>
              <input type="number" min="0" bind:value={selected.minimumUpgradeCustomFormatScore} class="font-mono" placeholder="0 = no minimum" />
            </label>
          </div>
          <div class="mt-2 text-[11px] text-muted-foreground">When upgrades are enabled, the new release must improve the custom-format subtotal by at least this amount.</div>
        </div>

        <div class="my-5 h-px bg-white/[0.06]"></div>

        <!-- Size limits -->
        <div class="mb-5">
          <div class="mb-2.5 text-sm font-semibold">Size Limits</div>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <label class="grid gap-1.5">
              <span class="text-xs text-muted-foreground">Min (MB/min)</span>
              <input type="number" min="0" bind:value={selected.minMbPerMinute} class="font-mono" placeholder="0 = no limit" />
            </label>
            <label class="grid gap-1.5">
              <span class="text-xs text-muted-foreground">Max (MB/min)</span>
              <input type="number" min="0" bind:value={selected.maxMbPerMinute} class="font-mono" placeholder="0 = no limit" />
            </label>
          </div>
          <div class="mt-2 text-[11px] text-muted-foreground">Applied per runtime minute. Releases without runtime metadata skip these limits.</div>
        </div>

        <div class="my-5 h-px bg-white/[0.06]"></div>

        <!-- Actions -->
        <div class="mt-1.5 flex justify-end gap-2.5">
          {#if selected.id && !selected.isDefault}
            <Button kind="danger" on:click={() => selected && deleteProfile(selected)} disabled={saving || deleting}>
              <Trash2 size={15} />
              Delete
            </Button>
          {/if}
          <Button kind="primary" on:click={save} disabled={saving}>
            <Save size={15} />
            {saving ? 'Saving…' : 'Save Profile'}
          </Button>
        </div>
      </Panel>
    </div>
  {:else}
    <div class="rounded-2xl border border-white/[0.06] bg-white/[0.02] p-8 text-center text-sm text-muted-foreground">
      Select a profile to edit, or create a new one.
    </div>
  {/if}
</div>
