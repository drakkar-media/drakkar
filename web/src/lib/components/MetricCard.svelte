<script lang="ts">
  export let label = '';
  export let value: string | number = '';
  export let detail = '';
  export let accent = false;
</script>

<div class="metric-card" class:accent>
  <div class="metric-label">{label}</div>
  <div class="metric-value">{value}</div>
  {#if detail}
    <div class="metric-detail">{detail}</div>
  {/if}
</div>

<style>
  .metric-card {
    padding: 16px 18px;
    border-radius: var(--radius-lg, 0.75rem);
    border: 1px solid hsl(0 0% 100% / 0.07);
    background: hsl(var(--card) / 0.85);
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }

  .metric-card.accent {
    border-color: hsl(var(--primary) / 0.2);
    background: hsl(var(--primary) / 0.05);
  }

  .metric-label {
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: hsl(var(--muted-foreground));
  }

  .metric-value {
    font-size: 1.75rem;
    font-weight: 700;
    font-family: 'JetBrains Mono', monospace;
    letter-spacing: -0.02em;
    line-height: 1;
    color: hsl(var(--foreground));
  }

  .metric-detail {
    font-size: 11px;
    color: hsl(var(--muted-foreground));
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
