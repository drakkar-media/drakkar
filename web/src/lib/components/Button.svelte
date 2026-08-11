<script lang="ts">
  /**
   * Standard clickable button with primary/secondary/ghost/danger visual
   * variants. Forwards event listeners and extra attributes, so it can be
   * used as a drop-in replacement for a native <button>.
   *
   * Renders the exact shadcn-svelte Button classes (via buttonVariants from
   * ui/button) so every button in the app -- whether it goes through this
   * legacy-style wrapper or the shadcn component directly -- looks identical.
   */
  import { buttonVariants } from '$lib/components/ui/button/index.js';
  import { cn } from '$lib/utils.js';

  export let kind: 'primary' | 'secondary' | 'ghost' | 'danger' = 'secondary';
  export let type: 'button' | 'submit' = 'button';
  export let disabled = false;
  // Captured explicitly (rather than left in $$restProps) so it can be
  // merged with the generated variant classes below instead of silently
  // replacing them -- $$restProps spread after a static `class` attribute
  // overwrites it wholesale, which stripped every button of its background/
  // border/layout classes the moment a caller passed so much as `class="self-start"`.
  let className = '';
  export { className as class };

  const VARIANT: Record<typeof kind, 'default' | 'secondary' | 'ghost' | 'destructive'> = {
    primary: 'default',
    secondary: 'secondary',
    ghost: 'ghost',
    danger: 'destructive'
  };
</script>

<button class={cn(buttonVariants({ variant: VARIANT[kind] }), className)} {type} {disabled} on:click on:submit on:keydown on:keyup on:mouseenter on:mouseleave on:focus on:blur {...$$restProps}>
  <slot />
</button>
