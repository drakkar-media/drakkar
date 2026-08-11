<script lang="ts">
  /**
   * Small rounded status chip, color-coded by `tone`. Used inline for short
   * status indicators (e.g. queue state, health checks).
   */
  export let tone: 'ok' | 'warn' | 'danger' | 'neutral' = 'neutral';
</script>

<span class={`status-pill status-pill-${tone}`}><slot /></span>
