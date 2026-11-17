// Package msfp implements MSFP, the Media Server Federation Protocol
// (https://github.com/msfp-io/msfp-spec), as servers speak it today
// (version 0), with the media checks of version 1's media part: each
// server's own key and its fingerprint, the one-time pairing code,
// signed requests and answers and their nonces, MSFP's strict JSON,
// addresses, certificate pins, and the checks of the playlists and PGS
// pictures a server relays from another.
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
