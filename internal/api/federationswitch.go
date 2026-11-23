package api

// FederationOn is federation's switch on this build. It is off on the
// branch that moves Overfin to msfp-go's revised v0 and MSFP's media
// model (branch msfp-model), from the msfp-go bump (plan phase 1) until
// the federation phase (plan phase 7) rebuilds both sides on copies:
// until then no engine opens (overfin's federation), so nothing pairs,
// no peer calls in and no peer's title plays here, and no paired
// server's copy is offered as a source (peerSources). The federation
// code stays and compiles; phase 7 turns this back on, then removes it.
const FederationOn = false
