package msfp

import (
	"context"
	"time"
)

// Background work (engine.md section 5), every hour: expired codes and
// old forgotten keys tidied; keys' time (maintainKeys): the end of this
// server's change of key after 30 days, rotate sent again daily, the 30
// days of a revoked key, peers suspended 30 days gone. Scheduled hellos,
// catalog readings, playback expiry and moves join it in later steps.

// tidyEvery is how often Run tidies.
const tidyEvery = time.Hour

// forgetAfter is how long a forgotten key is kept (MSFP core section
// 7.4: entries may go after a year).
const forgetAfter = 365 * 24 * time.Hour

// Run does the engine's background work until ctx ends, then returns
// ctx's error. Without it, the engine still answers peers and the
// operator, but nothing happens with time. Call Run or RunOnce, never
// both.
func (e *Engine) Run(ctx context.Context) error {
	t := time.NewTicker(tidyEvery)
	defer t.Stop()
	for {
		if err := e.RunOnce(ctx); err != nil && ctx.Err() == nil {
			e.log.Warn("msfp: background work failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// RunOnce does, once, the background work due now: for tests with a
// fake clock, and for hosts that schedule work themselves.
func (e *Engine) RunOnce(ctx context.Context) error {
	now := e.now()
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		codes, err := tx.Codes()
		if err != nil {
			return err
		}
		for _, c := range codes {
			if !c.Expires.After(now) {
				if err := tx.DeleteCode(c.ID); err != nil {
					return err
				}
			}
		}
		return tx.TrimForgotten(now.Add(-forgetAfter))
	})
	if err != nil {
		return err
	}
	return e.maintainKeys(ctx)
}
