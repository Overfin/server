// Why the last reading of a paired server's titles failed, in plain words.
// The codes are the federation engine's own (msfp.ReadFailures), never
// anything the other server wrote; a code this panel doesn't know shows
// nothing. English and French: the browser's language picks.

export interface ReadFailed {
  failure: string;
  refusal?: string;
  at: string;
}

type Lang = 'en' | 'fr';

const failures: Record<Lang, Record<string, string>> = {
  en: {
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
  },
  fr: {
    unreachable: "Ses titres n'ont pas pu être lus : il n'a pas répondu.",
    timeout: "Ses titres n'ont pas pu être lus : il a mis trop de temps à répondre.",
    'certificate-refused': "Ses titres n'ont pas pu être lus : son certificat n'est reconnu ni par une autorité publique, ni parmi ceux qu'il a indiqués.",
    'refused-unsigned': "Ses titres n'ont pas pu être lus : sa réponse n'était pas signée par sa clé. Il a peut-être mis fin à l'appairage ou perdu ses données, ou autre chose répond à son adresse.",
    refused: 'Il a refusé de donner ses titres.',
    'not-paired': "Ses titres n'ont pas pu être lus : il n'est plus appairé avec ce serveur, ou les deux doivent s'appairer à nouveau.",
    suspended: "Ses titres ne sont pas lus : il a révoqué sa clé.",
    uncallable: "Ses titres n'ont pas pu être lus : il ne peut pas être appelé.",
    'no-common-version': "Ses titres n'ont pas pu être lus : les deux serveurs n'ont aucune version du protocole en commun ; l'un d'eux doit être mis à jour.",
    'catalog-off': "Ses titres ne sont pas lus : le partage des titres n'est pas activé entre les deux serveurs.",
    'older-draft': "Ses titres n'ont pas pu être lus : il utilise une ancienne version provisoire du protocole et doit être mis à jour.",
    'answer-not-understood': "Ses titres n'ont pas pu être lus : sa réponse n'a pas pu être comprise.",
    internal: "Ses titres n'ont pas pu être lus : ce serveur a rencontré une erreur (son journal en donne la raison).",
  },
};

// The problem types of a signed refusal (MSFP core section 8.2).
const refusals: Record<Lang, Record<string, string>> = {
  en: {
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
  },
  fr: {
    'not-found': 'Il a refusé de donner ses titres : introuvables.',
    'not-supported': 'Il a refusé de donner ses titres : non pris en charge.',
    'invalid-request': "Il a refusé de donner ses titres : il a jugé la requête non valide.",
    'rotation-refused': 'Il a refusé de donner ses titres : changement de clé refusé.',
    'stream-limit-reached': 'Il a refusé de donner ses titres : limite de lectures atteinte.',
    'not-playable': 'Il a refusé de donner ses titres : illisible.',
    'delivery-refused': 'Il a refusé de donner ses titres : diffusion refusée.',
    'playback-stopped': 'Il a refusé de donner ses titres : lecture arrêtée.',
    'rate-limited': 'Il a refusé de donner ses titres : trop de requêtes. Il lui est redemandé plus tard.',
    'internal-error': 'Il a refusé de donner ses titres : une erreur de son côté.',
    'server-busy': 'Il a refusé de donner ses titres : il est occupé. Il lui est redemandé plus tard.',
  },
};

/** readingLang is the language the reasons are written in: French for a
 * browser in French, English otherwise. */
export const readingLang: Lang = typeof navigator !== 'undefined' && (navigator.language || '').toLowerCase().startsWith('fr') ? 'fr' : 'en';

/** readingText is why a reading failed, "" for none or an unknown code. */
export function readingText(r: ReadFailed | undefined, lang: Lang = readingLang): string {
  if (!r) return '';
  if (r.failure === 'refused' && r.refusal && refusals[lang][r.refusal]) return refusals[lang][r.refusal];
  return failures[lang][r.failure] ?? '';
}
