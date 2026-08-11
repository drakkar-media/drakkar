<script lang="ts">
  /**
   * Page-level heading with an optional subtitle and a right-aligned action
   * slot. Used at the top of most route pages.
   */
  export let title = '';
  export let subtitle = '';
</script>

<header class="flex flex-wrap items-end justify-between gap-4 mb-5">
  <div>
    <h1>{title}</h1>
    {#if subtitle}<p class="mt-1.5 mb-0 text-[13px] text-muted-foreground">{subtitle}</p>{/if}
  </div>
  <div class="flex flex-wrap gap-2.5">
    <slot />
  </div>
</header>
