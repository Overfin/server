<!-- An icon of lib/icons, in the text's color. -->
<script lang="ts">
  let { paths }: { paths: string[] } = $props();
</script>

<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
  {#each paths as d, i (i)}<path {d} />{/each}
</svg>
