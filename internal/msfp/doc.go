// Package msfp implements MSFP, the Media Server Federation Protocol
// (https://github.com/msfp-io/msfp-spec), version 1: of its core, each
// server's own key and its fingerprint, the one-time pairing code, signed
// requests and answers and their nonces, MSFP's strict JSON, addresses
// (HTTPS only), certificate pins, hello and the versions of the core and
// its parts, key rotation and revocation (their statements, the next key
// commitment), changes of address (the former host accepted, the
// announced address checked), servers that can't be called (their
// address, their hello schedule, the statements their peers' answers
// carry); of its catalog and video parts, video title keys and what a
// server keeps of a peer's titles; of playback and media, the forms of a
// play answer's IDs, URLs and previews; of media and video, the checks of
// the playlists, WebVTT and PGS pictures a server relays from another.
//
// A server proves who it is with its key only, never with a web
// certificate, a name or an address. Two servers learn each other's keys
// when their operators pair them, and from then on every request between
// them is signed by the sender, and every answer by the server answering.
//
// The package is the protocol alone, meant to become a library of its
// own: it imports only the standard library and third-party modules,
// never the server that uses it (imports_test.go checks this). Storage,
// limits, connections and the calls' handlers stay in the server.
package msfp
