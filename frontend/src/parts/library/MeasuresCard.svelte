<!-- Whether the library follows the people using it, over the last 90
     days: what arrived and was watched, what left and was asked for
     again, and how much of the library people watch. Totals for the whole
     server, nothing per person. -->
<script lang="ts">
  import { Card, Stats, Notice, Loading } from '@overfin/ui';
  import { api } from '../../lib/api';
  import { message } from '../../lib/format';

  interface Measures {
    days: number;
    arrived: number;
    started: number;
    finished: number;
    left: number;
    missed: number;
    library: number;
    watched: number;
  }

  let m = $state<Measures | null>(null);
  let problem = $state('');
  api<Measures>('GET', '/admin/api/measures').then(
    (d) => (m = d),
    (err) => (problem = message(err)),
  );

  const share = (part: number, whole: number) => (whole ? `${Math.round((part / whole) * 100)}%` : '—');
  const count = (n: number) => n.toLocaleString('en');
</script>

<Card title="How the library is used" description="The last 90 days, for the whole server. Films and seasons count as one each; nothing here is about one person.">
  {#if problem}
    <Notice warn>{problem}</Notice>
  {:else if !m}
    <Loading />
  {:else}
    <Stats
      items={[
        { value: share(m.started, m.arrived), label: `of the ${count(m.arrived)} arrived were started · ${share(m.finished, m.arrived)} finished` },
        { value: share(m.watched, m.library), label: `of the library watched (${count(m.watched)} of ${count(m.library)})` },
        { value: count(m.missed), label: `of the ${count(m.left)} that left were wished for again` },
      ]}
    />
  {/if}
</Card>
