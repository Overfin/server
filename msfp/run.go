package msfp

import (
	"context"
	"time"
)

// Background work (engine.md section 5). Every second: this server's
// new address tried until it answers (then announced), and while it
// can't be called, its scheduled hellos. Every hour: expired codes and
// old forgotten keys tidied; keys' time (maintainKeys): the end of this
// server's change of key after 30 days, rotate sent again daily, the 30
// days of a revoked key, peers suspended 30 days gone; addresses' time
// (maintainAddresses): the end of this server's move, peers late to move
// told again, peers' new addresses tried again; the playbacks left on
// peers before a restart stopped (once); the engine's own change log of
// the host's catalog refreshed. Every second too: peers' catalogs read
// when due (a minute after the start, then every SyncEvery, at once when
// what is wanted changes), peers' playbacks here expired (looked at
// every 30 seconds), and peers told where this server's viewers are in
// their playbacks there, when 45 seconds have passed (Progress).

// tidyEvery is how often Run does the hourly work; tickEvery, the rest.
const (
	tidyEvery = time.Hour
	tickEvery = time.Second
)

// forgetAfter is how long a forgotten key is kept (MSFP core section
// 7.4: entries may go after a year).
const forgetAfter = 365 * 24 * time.Hour

// Run does the engine's background work until ctx ends, then returns
// ctx's error. Without it, the engine still answers peers and the
// operator, but nothing happens with time. Call Run or RunOnce, never
// both.
func (e *Engine) Run(ctx context.Context) error {
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	var hourly time.Time
	for {
		var err error
		if time.Since(hourly) >= tidyEvery {
			hourly = time.Now()
			err = e.RunOnce(ctx)
		} else {
			e.tick(ctx)
		}
		if err != nil && ctx.Err() == nil {
			e.log.Warn("msfp: background work failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// RunOnce does, once, all the background work due now, the hourly work
// included, and waits for it: for tests with a fake clock, and for hosts
// that schedule work themselves (every second or so for a server that
// can't be called, whose hellos are on a schedule of seconds; every few
// minutes otherwise).
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
	if err := e.maintainKeys(ctx); err != nil {
		return err
	}
	if err := e.maintainAddresses(ctx); err != nil {
		return err
	}
	e.stopLeftovers(ctx)
	if e.keepsLog() {
		if err := e.refreshOwn(ctx); err != nil {
			e.log.Warn("msfp: the catalog's change log couldn't be refreshed", "error", err)
		}
	}
	e.readDue(ctx, true)
	e.expirePlaybacks(true)
	e.tick(ctx)
	return nil
}

// tick is the work of each second: this server's new address tried, its
// scheduled hellos sent, catalogs due read, playbacks expired, progress
// due sent.
func (e *Engine) tick(ctx context.Context) {
	e.reachAndAnnounce(ctx)
	e.scheduledHellos(ctx)
	e.readDue(ctx, false)
	e.expirePlaybacks(false)
	e.tellWatched()
}
