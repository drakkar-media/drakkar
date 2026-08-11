<script lang="ts">
  /**
   * Generic card/section container with an optional title, subtitle, and an
   * "actions" slot in the header, plus a default slot for body content.
   * Used as the standard content wrapper across dashboard and settings pages.
   */
  export let title = '';
  export let subtitle = '';
  export let wide = false;
  /** Removes the body's top padding, for content that should sit flush against the header (e.g. a table). */
  export let flush = false;
</script>

<section class:wide class="min-w-0 rounded-xl bg-card ring-1 ring-foreground/10">
  {#if title || subtitle}
    <header class="flex items-start justify-between gap-3 pt-[18px] px-5 pb-3.5 border-b border-border">
      <div>
        {#if title}<h2 class="m-0 text-lg leading-[1.1]">{title}</h2>{/if}
        {#if subtitle}<p class="mt-1.5 mb-0 text-[13px] text-muted-foreground">{subtitle}</p>{/if}
      </div>
      <slot name="actions" />
    </header>
  {/if}
  <div class="px-5 pb-5 {flush ? 'pt-0' : 'pt-4'}">
    <slot />
  </div>
</section>
