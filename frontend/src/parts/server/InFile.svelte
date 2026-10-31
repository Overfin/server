<!-- A part set in Overfin's configuration file: shown, changed there. -->
<script lang="ts">
  import { Card, Badge } from '@overfin/ui';

  let { title, text }: { title: string; text: string } = $props();
</script>

<Card {title}>
  {#snippet badge()}<Badge kind="info">Configuration file</Badge>{/snippet}
  <div class="box">
    <div>{text}</div>
    <div class="hint">Set in Overfin's configuration (its file, or OVERFIN_JELLYFIN_URL): change it there.</div>
  </div>
</Card>
