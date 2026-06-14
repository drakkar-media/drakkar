<script lang="ts">
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
  import { toastError, toastSuccess } from '$lib/toast';
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
    saving = true;
    try {
      const saved = await api.saveProfile(selected);
      toastSuccess(`Profile "${saved.name}" saved`);
      await load();
      const found = profiles.find(p => p.name === saved.name);
      if (found) selected = { ...found };
    } catch (err) { toastError(err instanceof Error ? err.message : String(err)); }
    finally { saving = false; }
  }

  async function deleteProfile(p: QualityProfile) {
    if (!p.id || p.isDefault) return;
    try {
      await api.deleteProfile(p.id);
      toastSuccess(`Profile "${p.name}" deleted`);
      if (selected?.id === p.id) selected = null;
      await load();
    } catch (err) { toastError(err instanceof Error ? err.message : String(err)); }
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

<div class="profiles-shell">
  <!-- Sidebar list -->
  <aside class="profile-list">
    {#each profiles as p (p.id ?? p.name)}
      <button
        class="profile-item"
        class:selected={selected?.id === p.id}
        on:click={() => selectProfile(p)}
        type="button"
      >
        <div class="profile-item-name">
          {#if p.isDefault}<Star size={12} class="star" />{/if}
          {p.name}
        </div>
        <div class="profile-item-meta">{p.resolutions.slice(0, 2).join(', ')}</div>
      </button>
    {/each}
    {#if profiles.length === 0 && !loading}
      <div class="empty">No profiles yet.</div>
    {/if}
  </aside>

  <!-- Editor panel -->
  {#if selected}
    <div class="editor">
      <Panel title={selected.id ? `Edit: ${selected.name}` : 'New Profile'} subtitle="Settings control how releases are ranked and filtered.">
        <div slot="actions">
          {#if selected.isDefault}<StatusPill tone="ok">Default</StatusPill>{/if}
        </div>

        <!-- Name -->
        <div class="field">
          <label class="field-label" for="pname">Profile Name</label>
          <input id="pname" class="field-input" bind:value={selected.name} placeholder="e.g. Movie HD" />
        </div>

        <div class="divider"></div>

        <!-- Resolutions (ordered) -->
        <div class="field">
          <div class="field-label">Resolutions <span class="field-hint">drag to re-rank</span></div>
          <div class="ordered-list">
            {#each selected.resolutions as res, i}
              <div class="ordered-row">
                <span class="rank">{i + 1}</span>
                <span class="ordered-value">{res}</span>
                <button type="button" class="rank-btn" on:click={() => { selected = { ...selected!, resolutions: moveUp(selected!.resolutions, i) }; }} disabled={i === 0}>
                  <ChevronUp size={13} />
                </button>
                <button type="button" class="rank-btn" on:click={() => { selected = { ...selected!, resolutions: moveDown(selected!.resolutions, i) }; }} disabled={i === selected.resolutions.length - 1}>
                  <ChevronDown size={13} />
                </button>
                <button type="button" class="rank-btn remove" on:click={() => { selected = { ...selected!, resolutions: selected!.resolutions.filter(v => v !== res) }; }}>✕</button>
              </div>
            {/each}
            <div class="chip-row">
              {#each ALL_RESOLUTIONS.filter(r => !selected!.resolutions.includes(r)) as r}
                <button type="button" class="chip add" on:click={() => { selected = { ...selected!, resolutions: [...selected!.resolutions, r] }; }}>{r} +</button>
              {/each}
            </div>
          </div>
        </div>

        <!-- Sources (ordered) -->
        <div class="field">
          <div class="field-label">Sources <span class="field-hint">rank by priority</span></div>
          <div class="ordered-list">
            {#each selected.sources as src, i}
              <div class="ordered-row">
                <span class="rank">{i + 1}</span>
                <span class="ordered-value">{src}</span>
                <button type="button" class="rank-btn" on:click={() => { selected = { ...selected!, sources: moveUp(selected!.sources, i) }; }} disabled={i === 0}><ChevronUp size={13} /></button>
                <button type="button" class="rank-btn" on:click={() => { selected = { ...selected!, sources: moveDown(selected!.sources, i) }; }} disabled={i === selected.sources.length - 1}><ChevronDown size={13} /></button>
                <button type="button" class="rank-btn remove" on:click={() => { selected = { ...selected!, sources: selected!.sources.filter(v => v !== src) }; }}>✕</button>
              </div>
            {/each}
            <div class="chip-row">
              {#each ALL_SOURCES.filter(s => !selected!.sources.includes(s)) as s}
                <button type="button" class="chip add" on:click={() => { selected = { ...selected!, sources: [...selected!.sources, s] }; }}>{s} +</button>
              {/each}
            </div>
          </div>
        </div>

        <!-- Codecs (ordered) -->
        <div class="field">
          <div class="field-label">Codecs <span class="field-hint">rank by priority</span></div>
          <div class="ordered-list">
            {#each selected.codecs as c, i}
              <div class="ordered-row">
                <span class="rank">{i + 1}</span>
                <span class="ordered-value">{c}</span>
                <button type="button" class="rank-btn" on:click={() => { selected = { ...selected!, codecs: moveUp(selected!.codecs, i) }; }} disabled={i === 0}><ChevronUp size={13} /></button>
                <button type="button" class="rank-btn" on:click={() => { selected = { ...selected!, codecs: moveDown(selected!.codecs, i) }; }} disabled={i === selected.codecs.length - 1}><ChevronDown size={13} /></button>
                <button type="button" class="rank-btn remove" on:click={() => { selected = { ...selected!, codecs: selected!.codecs.filter(v => v !== c) }; }}>✕</button>
              </div>
            {/each}
            <div class="chip-row">
              {#each ALL_CODECS.filter(c => !selected!.codecs.includes(c)) as c}
                <button type="button" class="chip add" on:click={() => { selected = { ...selected!, codecs: [...selected!.codecs, c] }; }}>{c} +</button>
              {/each}
            </div>
          </div>
        </div>

        <div class="divider"></div>

        <!-- Audio formats (ordered — new) -->
        <div class="field">
          <div class="field-label">Audio Formats <span class="field-hint">rank by priority — top scores highest</span></div>
          <div class="ordered-list">
            {#each selected.audioFormats as a, i}
              <div class="ordered-row">
                <span class="rank">{i + 1}</span>
                <span class="ordered-value">{a}</span>
                <button type="button" class="rank-btn" on:click={() => { selected = { ...selected!, audioFormats: moveUp(selected!.audioFormats, i) }; }} disabled={i === 0}><ChevronUp size={13} /></button>
                <button type="button" class="rank-btn" on:click={() => { selected = { ...selected!, audioFormats: moveDown(selected!.audioFormats, i) }; }} disabled={i === selected.audioFormats.length - 1}><ChevronDown size={13} /></button>
                <button type="button" class="rank-btn remove" on:click={() => { selected = { ...selected!, audioFormats: selected!.audioFormats.filter(v => v !== a) }; }}>✕</button>
              </div>
            {/each}
            <div class="chip-row">
              {#each ALL_AUDIO.filter(a => !selected!.audioFormats.includes(a)) as a}
                <button type="button" class="chip add" on:click={() => { selected = { ...selected!, audioFormats: [...selected!.audioFormats, a] }; }}>{a} +</button>
              {/each}
            </div>
          </div>
        </div>

        <!-- HDR formats (ordered — new) -->
        <div class="field">
          <div class="field-label">HDR Formats <span class="field-hint">rank by priority</span></div>
          <div class="ordered-list">
            {#each selected.hdrFormats as h, i}
              <div class="ordered-row">
                <span class="rank">{i + 1}</span>
                <span class="ordered-value">{h}</span>
                <button type="button" class="rank-btn" on:click={() => { selected = { ...selected!, hdrFormats: moveUp(selected!.hdrFormats, i) }; }} disabled={i === 0}><ChevronUp size={13} /></button>
                <button type="button" class="rank-btn" on:click={() => { selected = { ...selected!, hdrFormats: moveDown(selected!.hdrFormats, i) }; }} disabled={i === selected.hdrFormats.length - 1}><ChevronDown size={13} /></button>
                <button type="button" class="rank-btn remove" on:click={() => { selected = { ...selected!, hdrFormats: selected!.hdrFormats.filter(v => v !== h) }; }}>✕</button>
              </div>
            {/each}
            <div class="chip-row">
              {#each ALL_HDR.filter(h => !selected!.hdrFormats.includes(h)) as h}
                <button type="button" class="chip add" on:click={() => { selected = { ...selected!, hdrFormats: [...selected!.hdrFormats, h] }; }}>{h} +</button>
              {/each}
            </div>
          </div>
        </div>

        <div class="divider"></div>

        <!-- Languages (chips) -->
        <div class="field">
          <div class="field-label">Languages</div>
          <div class="chip-row">
            {#each ALL_LANGUAGES as lang}
              <button
                type="button"
                class="chip"
                class:on={selected.languages.includes(lang)}
                on:click={() => { selected = { ...selected!, languages: toggleOrdered(selected!.languages, lang) }; }}
              >{lang}</button>
            {/each}
          </div>
        </div>

        <div class="divider"></div>

        <!-- Flags — Radarr/Sonarr style -->
        <div class="field">
          <div class="field-label">Release Flags</div>
          <div class="flags-grid">
            <label class="flag-row">
              <input type="checkbox" bind:checked={selected.preferProper} />
              <div>
                <strong>Prefer Proper</strong>
                <span>Boost score when release is marked PROPER</span>
              </div>
            </label>
            <label class="flag-row">
              <input type="checkbox" bind:checked={selected.preferRepack} />
              <div>
                <strong>Prefer Repack</strong>
                <span>Boost score when release is marked REPACK</span>
              </div>
            </label>
            <label class="flag-row">
              <input type="checkbox" bind:checked={selected.rejectCam} />
              <div>
                <strong>Reject CAM / TS / Telecine</strong>
                <span>Hard-reject low-quality cam captures and telesyncs</span>
              </div>
            </label>
            <label class="flag-row">
              <input type="checkbox" bind:checked={selected.allowUpgrade} />
              <div>
                <strong>Allow Quality Upgrade</strong>
                <span>Periodically re-search available items for better releases</span>
              </div>
            </label>
          </div>
        </div>

        <div class="divider"></div>

        <div class="field">
          <div class="field-label">Upgrade Rules</div>
          <div class="size-row">
            <label>
              <span>Minimum CF Upgrade</span>
              <input type="number" min="0" bind:value={selected.minimumUpgradeCustomFormatScore} class="size-input" placeholder="0 = no minimum" />
            </label>
          </div>
          <div class="field-hint" style="margin-top:8px">When upgrades are enabled, the new release must improve the custom-format subtotal by at least this amount.</div>
        </div>

        <div class="divider"></div>

        <!-- Size limits -->
        <div class="field">
          <div class="field-label">Size Limits</div>
          <div class="size-row">
            <label>
              <span>Min (MB/min)</span>
              <input type="number" min="0" bind:value={selected.minMbPerMinute} class="size-input" placeholder="0 = no limit" />
            </label>
            <label>
              <span>Max (MB/min)</span>
              <input type="number" min="0" bind:value={selected.maxMbPerMinute} class="size-input" placeholder="0 = no limit" />
            </label>
          </div>
          <div class="field-hint" style="margin-top:8px">Applied per runtime minute. Releases without runtime metadata skip these limits.</div>
        </div>

        <div class="divider"></div>

        <!-- Actions -->
        <div class="editor-actions">
          {#if selected.id && !selected.isDefault}
            <Button kind="danger" on:click={() => selected && deleteProfile(selected)} disabled={saving}>
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
    <div class="no-selection">
      Select a profile to edit, or create a new one.
    </div>
  {/if}
</div>

<style>
  .profiles-shell {
    display: grid;
    grid-template-columns: 240px minmax(0, 1fr);
    gap: 16px;
    align-items: start;
  }

  /* Sidebar */
  .profile-list {
    display: grid;
    gap: 8px;
    position: sticky;
    top: 88px;
  }

  .profile-item {
    display: grid;
    gap: 3px;
    padding: 12px 14px;
    border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.03);
    text-align: left;
    cursor: pointer;
    transition: background 0.12s;
  }

  .profile-item:hover, .profile-item.selected {
    background: hsl(var(--primary) / 0.12);
    border-color: hsl(var(--primary) / 0.28);
  }

  .profile-item-name {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
    font-weight: 600;
  }

  .profile-item-name :global(.star) { color: hsl(var(--primary)); }

  .profile-item-meta {
    font-size: 11px;
    color: hsl(var(--muted-foreground));
    font-family: 'JetBrains Mono', monospace;
  }

  /* Editor */
  .editor { display: grid; gap: 0; }

  .no-selection {
    padding: 32px;
    border-radius: 18px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.02);
    color: hsl(var(--muted-foreground));
    text-align: center;
  }

  .field {
    margin-bottom: 20px;
  }

  .field-label {
    font-size: 13px;
    font-weight: 600;
    margin-bottom: 10px;
    display: flex;
    align-items: baseline;
    gap: 8px;
  }

  .field-hint {
    font-size: 11px;
    font-weight: 400;
    color: hsl(var(--muted-foreground));
  }

  .field-input {
    width: 100%;
    padding: 10px 12px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    font-size: 13px;
  }

  .divider {
    height: 1px;
    background: hsl(0 0% 100% / 0.06);
    margin: 6px 0 20px;
  }

  /* Ordered list */
  .ordered-list { display: grid; gap: 6px; }

  .ordered-row {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border-radius: 10px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.03);
  }

  .rank {
    min-width: 22px;
    font-size: 11px;
    font-weight: 700;
    font-family: 'JetBrains Mono', monospace;
    color: hsl(var(--primary));
  }

  .ordered-value {
    flex: 1;
    font-size: 13px;
    font-family: 'JetBrains Mono', monospace;
  }

  .rank-btn {
    display: grid;
    place-items: center;
    width: 26px;
    height: 26px;
    border-radius: 7px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: transparent;
    color: hsl(var(--muted-foreground));
    cursor: pointer;
    font-size: 12px;
  }

  .rank-btn:hover { background: hsl(0 0% 100% / 0.08); color: hsl(var(--foreground)); }
  .rank-btn:disabled { opacity: 0.3; cursor: default; }
  .rank-btn.remove:hover { background: hsl(0 72% 51% / 0.15); color: hsl(0 96% 82%); }

  /* Chips */
  .chip-row { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }

  .chip {
    padding: 5px 12px;
    border-radius: 10px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--muted-foreground));
    font-size: 12px;
    font-family: 'JetBrains Mono', monospace;
    cursor: pointer;
    transition: all 0.12s;
  }

  .chip.on {
    background: hsl(var(--primary) / 0.18);
    border-color: hsl(var(--primary) / 0.4);
    color: hsl(var(--primary));
  }

  .chip.add {
    border-style: dashed;
    font-size: 11px;
  }

  .chip.add:hover, .chip:not(.on):hover {
    background: hsl(0 0% 100% / 0.08);
    color: hsl(var(--foreground));
  }

  /* Flags (Radarr/Sonarr style) */
  .flags-grid { display: grid; gap: 10px; }

  .flag-row {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 12px 14px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.03);
    cursor: pointer;
  }

  .flag-row input[type=checkbox] {
    width: 16px;
    height: 16px;
    flex-shrink: 0;
    margin-top: 2px;
    accent-color: hsl(var(--primary));
    cursor: pointer;
  }

  .flag-row strong { display: block; font-size: 13px; margin-bottom: 2px; }
  .flag-row span   { display: block; font-size: 12px; color: hsl(var(--muted-foreground)); }

  /* Size limits */
  .size-row { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }

  .size-row label { display: grid; gap: 6px; }
  .size-row span  { font-size: 12px; color: hsl(var(--muted-foreground)); }
  .size-input {
    width: 100%;
    padding: 10px 12px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    font-size: 13px;
    font-family: 'JetBrains Mono', monospace;
  }

  /* Actions */
  .editor-actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
    margin-top: 6px;
  }

  .empty {
    color: hsl(var(--muted-foreground));
    font-size: 13px;
    padding: 8px 14px;
  }

  @media (max-width: 1100px) {
    .profiles-shell { grid-template-columns: 1fr; }
    .profile-list { position: static; }
  }

  @media (max-width: 700px) {
    .size-row { grid-template-columns: 1fr; }
  }
</style>
