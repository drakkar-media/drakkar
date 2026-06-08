<script lang="ts">
  export let tone: 'ok' | 'warn' | 'danger' | 'neutral' = 'neutral';
</script>

<span class={`pill ${tone}`}><slot /></span>

<style>
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 24px;
    padding: 0 10px;
    border-radius: 999px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.05);
    font-size: 12px;
    font-weight: 700;
    color: hsl(var(--foreground));
    white-space: nowrap;
  }

  .ok {
    background: hsl(142 70% 40% / 0.18);
    color: hsl(141 80% 78%);
  }

  .warn {
    background: hsl(43 96% 44% / 0.16);
    color: hsl(47 100% 77%);
  }

  .danger {
    background: hsl(0 72% 51% / 0.16);
    color: hsl(0 96% 82%);
  }

  .neutral {
    color: hsl(var(--muted-foreground));
  }
</style>
