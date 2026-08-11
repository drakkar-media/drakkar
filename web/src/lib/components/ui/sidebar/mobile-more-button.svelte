<script lang="ts">
  import Menu from '@lucide/svelte/icons/menu';
  import { useSidebar } from './context.svelte.js';

  const sidebar = useSidebar();
</script>

<button
  type="button"
  class="flex min-h-11 min-w-11 flex-col items-center justify-center gap-0.5 rounded-md text-muted-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
  aria-label="More"
  onclick={() => sidebar.toggle()}
>
  <Menu size={20} />
</button>
