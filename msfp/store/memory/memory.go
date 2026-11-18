// Package memory is an msfp.Store that keeps everything in memory: for
// tests, and for runs whose pairings needn't outlive the process. Nothing
// is written anywhere, so it seals nothing (msfp.PlainSealer).
package memory

import (
	"context"

	"github.com/overfin/server/msfp"
	"github.com/overfin/server/msfp/store/internal/state"
)

// Store is an msfp.Store in memory. Its zero value isn't usable: use New.
type Store struct {
	s *state.Store
}

// New is an empty store.
func New() *Store { return &Store{s: state.NewStore(state.New())} }

// Read runs fn on a consistent view.
func (m *Store) Read(_ context.Context, fn func(msfp.Tx) error) error { return m.s.Read(fn) }

// Update runs fn in a transaction: all of it is kept, or nothing.
func (m *Store) Update(_ context.Context, fn func(msfp.Tx) error) error { return m.s.Update(fn) }

// Sealer is msfp.PlainSealer: what is kept in memory only needs no
// sealing.
func (m *Store) Sealer() msfp.Sealer { return msfp.PlainSealer{} }

var (
	_ msfp.Store          = (*Store)(nil)
	_ msfp.SealerProvider = (*Store)(nil)
)
