<script lang="ts">
  export let size: number = 24;
</script>

<!--
  Stylized Drakkar (Viking longship) silhouette.
  Three shapes: mast, sail, hull.
  Uses currentColor so it adapts to any background.
-->
<svg
  width={size}
  height={size}
  viewBox="0 0 36 36"
  fill="none"
  xmlns="http://www.w3.org/2000/svg"
  aria-hidden="true"
>
  <!-- Sail -->
  <rect x="9" y="5" width="18" height="14" rx="2.5" fill="currentColor" opacity="0.55"/>
  <!-- Mast (rendered after sail so it draws on top) -->
  <rect x="16.5" y="2" width="3" height="21" rx="1.5" fill="currentColor"/>
  <!-- Hull -->
  <path d="M4 23 L32 23 Q31 31 18 32 Q5 31 4 23 Z" fill="currentColor"/>
</svg>
