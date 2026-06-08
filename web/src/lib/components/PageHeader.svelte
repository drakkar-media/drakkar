<script lang="ts">
  export let title = '';
  export let subtitle = '';
</script>

<header class="page-head">
  <div>
    <h1>{title}</h1>
    {#if subtitle}<p>{subtitle}</p>{/if}
  </div>
  <div class="actions">
    <slot />
  </div>
</header>

<style>
  .page-head {
    display: flex;
    justify-content: space-between;
    align-items: end;
    gap: 16px;
    flex-wrap: wrap;
    margin-bottom: 20px;
  }

  h1 {
    margin: 0;
    font-size: clamp(1.8rem, 4vw, 2.6rem);
    line-height: 1.02;
  }

  p {
    margin: 8px 0 0;
    color: hsl(var(--muted-foreground));
    font-size: 14px;
  }

  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 10px;
  }
</style>
