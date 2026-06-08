<script lang="ts">
  export let label = '';
  export let value = '';
  export let detail = '';
</script>

<div class="tile">
  <div class="label">{label}</div>
  <div class="value">{value}</div>
  {#if detail}<div class="detail">{detail}</div>{/if}
</div>

<style>
  .tile {
    min-width: 0;
    padding: 16px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    border-radius: 18px;
    background: hsl(0 0% 100% / 0.035);
  }

  .label {
    color: hsl(var(--muted-foreground));
    font-size: 12px;
    font-weight: 700;
    text-transform: uppercase;
  }

  .value {
    margin-top: 10px;
    font-size: 28px;
    font-weight: 800;
    line-height: 1;
  }

  .detail {
    margin-top: 8px;
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }
</style>
