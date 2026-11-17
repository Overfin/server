// The detail beside a list (People's account, Valuation's "Why this
// value"): on a narrow screen the kit puts it under the list, out of sight,
// so picking something brings it into view. On a wide screen it's sticky
// beside the list, already seen.
import { tick } from 'svelte';

export async function reveal(detail: HTMLElement | undefined) {
  await tick();
  if (!detail || getComputedStyle(detail).position === 'sticky') return;
  const still = matchMedia('(prefers-reduced-motion: reduce)').matches;
  detail.scrollIntoView({ behavior: still ? 'auto' : 'smooth', block: 'start' });
}
