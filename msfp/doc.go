// Package msfp is an engine for MSFP, the Media Server Federation
// Protocol (https://github.com/msfp-io/msfp-spec), version 1. A media
// server that uses it, the host, pairs with other servers by hand,
// shares its titles with them and plays theirs for its own people,
// without writing any of the protocol itself: the engine signs and
// checks every call, keeps the pairings, keys, addresses and limits,
// reads peers' catalogs, starts and relays playbacks, and does what time
// asks (rotations, moves, retries). The host keeps its media, its people
// and its pages; it plugs in through a few interfaces.
//
// # The minimum
//
// A server that only pairs needs an address, a store and the routes:
//
//	store, err := jsonfile.Open("data/msfp") // its records, sealed with a key file
//	…
//	eng, err := msfp.Open(ctx, msfp.Config{
//		Address: "https://media.example.org", // where peers call it, https only
//		Store:   store,
//	})
//	…
//	mux.Handle("/api/v1/federation/", eng.FederationHandler())
//	mux.Handle("/api/v1/stream/", eng.StreamHandler(nil))
//	go eng.Run(ctx) // what time does
//
// Mount the handlers on the raw path, with no authentication in front and
// no compression of their answers. Then, from the host's panel:
//
//	code, _, err := eng.MakeCode(ctx, "for Alice") // send it to the other operator
//	peer, err := eng.RedeemCode(ctx, pasted)        // a code the other operator sent
//	_, err = eng.Accept(ctx, id)                    // after both compared fingerprints
//
// # Sharing and playing
//
// To share, the host gives Config a Catalog (its libraries and titles), a
// Playback (starting and stopping a stream) and a Media (serving it): see
// host.go. Its operator chooses what each peer is offered (SetOffers) and
// which of a peer's libraries this server reads (SetWants); the engine
// keeps peers' titles in the Store, where the host reads them as it
// likes. Its viewers play a peer's title through Play, Progress, Stop and
// Fetch, at a route of the host's own. Events (Config.Notifier) tell the
// host what happened, for its panel and its logs.
//
// # More
//
// Keys that change (Rotate, KeyStolen, ConfirmRotation), addresses that
// change (SetAddress), servers that can't be called (SetUncallable), the
// discovery document (DiscoveryHandler) and certificate pins are all
// handled here; the host only calls the method its operator chose.
// msfp/wire holds the protocol's pieces (keys, codes, signatures, strict
// JSON, statements); msfp/store/jsonfile and msfp/store/memory are ready
// stores. Neither package imports anything but the standard library and
// filippo.io/edwards25519.
//
// examples/share-folder is a whole host in a few hundred lines: a folder
// of films shared with the servers it pairs with, typed commands for a
// panel. The engine's API, in words, is api/engine.md and api/host.md in
// the specification's repository.
package msfp
