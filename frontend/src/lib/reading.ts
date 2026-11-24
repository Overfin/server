// Why the last reading of a paired server's titles failed, in plain words.
// The codes are the federation engine's own (msfp.ReadFailures), never
// anything the other server wrote; a code this panel doesn't know shows
// nothing. English, like the rest of the panel.

export interface ReadFailed {
  failure: string;
  refusal?: string;
  at: string;
}

const failures: Record<string, string> = {
  unreachable: "Its titles couldn't be read: it didn't answer.",
  timeout: "Its titles couldn't be read: it took too long to answer.",
  'certificate-refused': "Its titles couldn't be read: its certificate is neither publicly trusted nor one it named.",
  'refused-unsigned': "Its titles couldn't be read: its answer wasn't signed by its key. It may have ended the pairing or lost its records, or something else answers at its address.",
  refused: 'It refused to give its titles.',
  'not-paired': "Its titles couldn't be read: it isn't paired with this server any more, or the two must pair again.",
  suspended: "Its titles aren't read: it revoked its key.",
  uncallable: "Its titles couldn't be read: it can't be called.",
  'no-common-version': "Its titles couldn't be read: the two servers have no version of the protocol in common; one of them needs to update.",
  'catalog-off': "Its titles aren't read: sharing titles isn't on between the two servers.",
  'older-draft': "Its titles couldn't be read: it runs an older draft of the protocol and needs to update.",
  'answer-not-understood': "Its titles couldn't be read: its answer couldn't be understood.",
  internal: "Its titles couldn't be read: this server failed (its log says why).",
};

// The problem types of a signed refusal (MSFP core section 8.2).
const refusals: Record<string, string> = {
  'not-found': 'It refused to give its titles: not found.',
  'not-supported': 'It refused to give its titles: not supported.',
  'invalid-request': 'It refused to give its titles: it found the request invalid.',
  'rotation-refused': 'It refused to give its titles: key change refused.',
  'stream-limit-reached': 'It refused to give its titles: stream limit reached.',
  'not-playable': 'It refused to give its titles: not playable.',
  'delivery-refused': 'It refused to give its titles: delivery refused.',
  'playback-stopped': 'It refused to give its titles: playback stopped.',
  'rate-limited': 'It refused to give its titles: too many requests. It is asked again later.',
  'internal-error': 'It refused to give its titles: an error on its side.',
  'server-busy': 'It refused to give its titles: it is busy. It is asked again later.',
};

/** readingText is why a reading failed, "" for none or an unknown code. */
export function readingText(r: ReadFailed | undefined): string {
  if (!r) return '';
  if (r.failure === 'refused' && r.refusal && refusals[r.refusal]) return refusals[r.refusal];
  return failures[r.failure] ?? '';
}
