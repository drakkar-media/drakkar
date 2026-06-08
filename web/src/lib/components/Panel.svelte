<script lang="ts">
  export let title = '';
  export let subtitle = '';
  export let wide = false;
  export let flush = false;
</script>

<section class:wide class:flush class="panel">
  {#if title || subtitle}
    <header class="panel-head">
      <div>
        {#if title}<h2>{title}</h2>{/if}
        {#if subtitle}<p>{subtitle}</p>{/if}
      </div>
      <slot name="actions" />
    </header>
  {/if}
  <div class="panel-body">
    <slot />
  </div>
</section>

<style>
  .panel {
    min-width: 0;
    border: 1px solid hsl(var(--border) / 0.9);
    border-radius: 24px;
    background: hsl(var(--card) / 0.82);
    box-shadow: 0 20px 60px hsl(190 80% 6% / 0.24);
    backdrop-filter: blur(20px);
  }

  .panel-head {
    display: flex;
    justify-content: space-between;
    align-items: start;
    gap: 12px;
    padding: 18px 20px 14px;
    border-bottom: 1px solid hsl(0 0% 100% / 0.05);
  }

  h2 {
    margin: 0;
    font-size: 18px;
    line-height: 1.1;
  }

  p {
    margin: 6px 0 0;
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .panel-body {
    padding: 16px 20px 20px;
  }

  .flush .panel-body {
    padding-top: 0;
  }
</style>
