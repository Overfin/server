// Package msfp is an engine for MSFP, the Media Server Federation
// Protocol (https://github.com/msfp-io/msfp-spec), version 1: it runs the
// protocol between servers, and the server using it, the host, plugs in
// through a few interfaces.
//
// A host opens one engine for the life of its process, mounts its
// handler, and calls it from its panel:
//
//	store, err := jsonfile.Open("data/msfp")
//	…
//	eng, err := msfp.Open(ctx, msfp.Config{
//		Address: "https://media.example.org",
//		Store:   store,
//	})
//	…
//	mux.Handle("/api/v1/federation/", eng.FederationHandler())
//	go eng.Run(ctx)
//
//	code, _, err := eng.MakeCode(ctx, "for Alice") // send it to the other operator
//	peer, err := eng.RedeemCode(ctx, pasted)        // a code the other operator sent
//
// What the engine does so far: this server's keys (the current one and
// the next, sealed), pairing codes (made, listed, withdrawn, read,
// redeemed), pair and hello both ways, the states ours, theirs, paired
// and gone, accepting and ending a pairing (its keys forgotten), the
// versions and parts each peer speaks, its pins, nonces in memory, the
// limits of MSFP core section 8.3, signed answers and refusals; keys
// that change (core section 12): Rotate, KeyStolen, a peer's rotate and
// revoke, a pending key confirmed (ConfirmRotation), suspension, and
// what 30 days do to each; addresses that change (core section 13):
// SetAddress, announced once this server reaches itself there, and
// peers' moves, confirmed by a hello at their new address; servers that
// can't be called (core section 7.5): SetUncallable, the scheduled
// hellos, rotations and revocations pulled from hello answers, and
// peers that can't be called, never connected to; the discovery document
// (core section 11): served when the host turns it on
// (DiscoveryHandler), and peers' fetched for the base path of every call
// to them; this server's own certificate pins, read at its addresses
// unless the host gives them. The catalog, playback and media come next.
//
// Mount the handlers on the raw path, with no authentication in front
// and no compression of their answers:
//
//	mux.Handle(prefix+"/api/v1/federation/", eng.FederationHandler())
//	mux.Handle("/.well-known/msfp", eng.DiscoveryHandler())
//
// The protocol's pieces (keys, codes, signatures, strict JSON, pins,
// statements) are package msfp/wire. Neither package imports anything
// but the standard library and filippo.io/edwards25519.
//
// A host shares by giving Config a Catalog, a Playback and a Media
// (host.go); its viewers play peers' titles through Play, Progress, Stop
// and Fetch, at a route of its own. examples/share-folder is a whole host
// in a few hundred lines.
package msfp
