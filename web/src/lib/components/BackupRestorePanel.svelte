<script lang="ts">
  /**
   * Provides the shared administrative backup/restore surface used by both
   * Settings and Tasks. Restore waits through the container restart and reloads
   * once the restored or rolled-back service is healthy again.
   */
  import { onMount } from 'svelte';
  import DatabaseBackup from '@lucide/svelte/icons/database-backup';
  import Download from '@lucide/svelte/icons/download';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import Upload from '@lucide/svelte/icons/upload';
  import Button from '$lib/components/Button.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api } from '$lib/api';
  import { bytes, dateTime } from '$lib/format';
  import { toastError, toastSuccess } from '$lib/toast';
  import type { BackupInfo, RestoreStatus } from '$lib/types';

  let backups: BackupInfo[] = [];
  let restoreStatus: RestoreStatus = { state: 'idle' };
  let loading = true;
  let creating = false;
  let uploading = false;
  let restoring = '';
  let deleting = '';
  let fileInput: HTMLInputElement;
  let disposed = false;

  async function load() {
    try {
      const [backupResponse, status] = await Promise.all([api.backups(), api.restoreStatus()]);
      backups = backupResponse.items ?? [];
      restoreStatus = status;
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      loading = false;
    }
  }

  async function createBackup() {
    creating = true;
    try {
      const backup = await api.createBackup();
      toastSuccess(`Backup created: ${backup.name}`);
      await load();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      creating = false;
    }
  }

  async function uploadBackup(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    input.value = '';
    if (!file) return;
    uploading = true;
    try {
      const backup = await api.uploadBackup(file);
      toastSuccess(`Backup imported: ${backup.name}`);
      await load();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      uploading = false;
    }
  }

  function downloadBackup(name: string) {
    window.location.assign(api.backupDownloadURL(name));
  }

  async function deleteBackup(name: string) {
    if (!confirm(`Delete backup "${name}"?`)) return;
    deleting = name;
    try {
      await api.deleteBackup(name);
      toastSuccess('Backup deleted');
      await load();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      deleting = '';
    }
  }

  async function restoreBackup(name: string) {
    const confirmation = prompt(`Restore "${name}"?\n\nType the full backup name to continue.`);
    if (confirmation === null) return;
    if (confirmation.trim() !== name) {
      toastError('Backup name did not match');
      return;
    }
    restoring = name;
    try {
      restoreStatus = await api.restoreBackup(name);
      toastSuccess('Restore staged; Drakkar is restarting');
      await waitForRestart();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
      restoring = '';
    }
  }

  async function waitForRestart() {
    let observedOffline = false;
    for (let attempt = 0; attempt < 900 && !disposed; attempt++) {
      await new Promise((resolve) => window.setTimeout(resolve, 2000));
      try {
        const response = await fetch('/health', { cache: 'no-store' });
        if (!response.ok) {
          observedOffline = true;
          continue;
        }
        if (observedOffline) {
          window.location.reload();
          return;
        }
        const status = await api.restoreStatus();
        if (!['scheduled', 'restoring'].includes(status.state)) {
          window.location.reload();
          return;
        }
      } catch {
        observedOffline = true;
      }
    }
    restoring = '';
    toastError('Restore restart did not finish within 30 minutes');
  }

  function statusTone(state: RestoreStatus['state']): 'neutral' | 'ok' | 'warn' | 'danger' {
    if (state === 'completed') return 'ok';
    if (state === 'scheduled' || state === 'restoring' || state === 'rebuilding') return 'warn';
    if (state === 'failed' || state === 'rolled_back' || state === 'fatal') return 'danger';
    return 'neutral';
  }

  onMount(() => {
    void load();
    return () => { disposed = true; };
  });
</script>

<Panel title="Backup & Restore" subtitle="Settings and PostgreSQL database bundles.">
  <svelte:fragment slot="actions">
    <div class="flex flex-wrap justify-end gap-2">
      <Button kind="secondary" on:click={() => fileInput.click()} disabled={uploading || creating || !!restoring}>
        {#if uploading}<RefreshCw size={14} class="animate-spin" />{:else}<Upload size={14} />{/if}
        Import
      </Button>
      <Button kind="primary" on:click={createBackup} disabled={creating || uploading || !!restoring}>
        {#if creating}<RefreshCw size={14} class="animate-spin" />{:else}<DatabaseBackup size={14} />{/if}
        {creating ? 'Creating…' : 'Create Backup'}
      </Button>
      <input bind:this={fileInput} class="hidden" type="file" accept=".drakkar-backup,application/x-tar" on:change={uploadBackup} />
    </div>
  </svelte:fragment>

  {#if restoreStatus.state !== 'idle'}
    <div class="mb-3 flex min-w-0 items-center justify-between gap-3 rounded-lg border border-border bg-background/40 px-3 py-2.5">
      <div class="min-w-0">
        <div class="truncate text-sm font-semibold">{restoreStatus.backupName ?? 'Restore'}</div>
        {#if restoreStatus.error}<div class="mt-1 break-words text-xs text-muted-foreground">{restoreStatus.error}</div>{/if}
      </div>
      <StatusPill tone={statusTone(restoreStatus.state)}>{restoreStatus.state.replace('_', ' ')}</StatusPill>
    </div>
  {/if}

  {#if loading}
    <div class="py-5 text-center text-sm text-muted-foreground">Loading…</div>
  {:else if backups.length === 0}
    <div class="py-5 text-center text-sm text-muted-foreground">No backups available.</div>
  {:else}
    <div class="divide-y divide-border overflow-hidden rounded-lg border border-border">
      {#each backups as backup (backup.name)}
        <div class="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-3 px-3 py-3 max-[620px]:grid-cols-1">
          <div class="min-w-0">
            <div class="truncate font-mono text-xs font-semibold" title={backup.name}>{backup.name}</div>
            <div class="mt-1 text-xs text-muted-foreground">{dateTime(backup.createdAt)} · {bytes(backup.sizeBytes)} · v{backup.drakkarVersion}</div>
          </div>
          <div class="flex flex-wrap items-center justify-end gap-1.5 max-[620px]:justify-start">
            <Button kind="ghost" class="h-8 w-8 px-0" title="Download backup" aria-label={`Download ${backup.name}`} on:click={() => downloadBackup(backup.name)} disabled={!!restoring}>
              <Download size={14} />
            </Button>
            <Button kind="secondary" on:click={() => restoreBackup(backup.name)} disabled={!!restoring || creating || uploading}>
              {#if restoring === backup.name}<RefreshCw size={14} class="animate-spin" />{:else}<RotateCcw size={14} />{/if}
              Restore
            </Button>
            <Button kind="ghost" class="h-8 w-8 px-0" title="Delete backup" aria-label={`Delete ${backup.name}`} on:click={() => deleteBackup(backup.name)} disabled={deleting === backup.name || !!restoring}>
              <Trash2 size={14} />
            </Button>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</Panel>
