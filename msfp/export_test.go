package msfp

import "context"

// For the tests of package msfp_test: the late ends of a change of key,
// called by hand, as if learnt after the next change began.

// FinishRotationForTest is finishRotation: the change away from old
// ends, if it is still the one under way.
func (e *Engine) FinishRotationForTest(ctx context.Context, old []byte) (bool, error) {
	return e.finishRotation(ctx, old)
}

// PeerMovedForTest is peerMoved: peer id knows us by current now.
func (e *Engine) PeerMovedForTest(ctx context.Context, id string, current []byte) bool {
	return e.peerMoved(ctx, id, current)
}

// WaitForTest waits for the work the engine started in the background
// (confirmations of peers' new addresses, discovery documents fetched).
func (e *Engine) WaitForTest() { e.bg.Wait() }

// FetchBaseForTest is fetchBase: the base path address's discovery
// document gives ("" for the default).
func (e *Engine) FetchBaseForTest(ctx context.Context, address string) string {
	return e.fetchBase(ctx, address)
}

// CallForTest sends peer id a call with the body in, as the engine
// does, and gives the status of its signed answer, read into out.
func (e *Engine) CallForTest(ctx context.Context, id, call string, in, out any) (int, error) {
	p, err := e.peerRecord(ctx, id)
	if err != nil {
		return 0, err
	}
	r, err := e.call(ctx, p, call, in, out, false)
	return r.status, err
}

// TouchForTest notes a use of a peer's playback here, as a progress
// does.
func (e *Engine) TouchForTest(id string) {
	e.plays.mu.Lock()
	pb := e.plays.byID[id]
	e.plays.mu.Unlock()
	if pb != nil {
		e.used(pb)
	}
}
