<script lang="ts">
  import X from '@lucide/svelte/icons/x';
  import { dismiss, toasts } from '$lib/toast';
</script>

<div class="toast-viewport" aria-live="polite" aria-atomic="true">
  {#each $toasts as item (item.id)}
    <div class={`toast ${item.tone}`}>
      <div class="message">{item.message}</div>
      <button class="close" type="button" aria-label="Dismiss notification" on:click={() => dismiss(item.id)}>
        <X size={14} />
      </button>
    </div>
  {/each}
</div>

<style>
  .toast-viewport {
    position: fixed;
    right: 16px;
    bottom: 16px;
    z-index: 80;
    display: grid;
    gap: 10px;
    width: min(420px, calc(100vw - 32px));
    pointer-events: none;
  }

  .toast {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 10px;
    align-items: start;
    padding: 12px 14px;
    border-radius: 16px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(212 27% 10% / 0.96);
    box-shadow: 0 18px 40px hsl(0 0% 0% / 0.3);
    pointer-events: auto;
  }

  .toast.info {
    border-color: hsl(171 82% 55% / 0.22);
  }

  .toast.success {
    border-color: hsl(140 65% 45% / 0.28);
  }

  .toast.error {
    border-color: hsl(0 72% 51% / 0.3);
  }

  .message {
    min-width: 0;
    color: hsl(var(--foreground));
    font-size: 14px;
    line-height: 1.4;
  }

  .close {
    display: grid;
    place-items: center;
    width: 28px;
    height: 28px;
    border: 0;
    border-radius: 999px;
    background: transparent;
    color: hsl(var(--muted-foreground));
    cursor: pointer;
  }

  .close:hover {
    background: hsl(0 0% 100% / 0.08);
    color: hsl(var(--foreground));
  }
</style>
