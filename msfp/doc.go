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
// limits of MSFP core section 8.3, signed answers and refusals. Key
// rotation, address changes, servers that can't be called, the catalog,
// playback and media come next.
//
// The protocol's pieces (keys, codes, signatures, strict JSON, pins,
// statements) are package msfp/wire. Neither package imports anything
// but the standard library and filippo.io/edwards25519.
package msfp
