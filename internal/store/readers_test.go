package store

import (
	"context"
	"testing"
	"time"
)

// Reads go side by side: one held open doesn't keep another waiting, nor
// a write.
func TestReadsSideBySide(t *testing.T) {
	s := openTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, err := s.read.QueryContext(ctx, `SELECT name FROM meta`)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	var n int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM accounts`).Scan(&n); err != nil {
		t.Fatalf("a second read waited: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('test', x'00')`); err != nil {
		t.Fatalf("a write waited: %v", err)
	}
	// What's committed is seen by the next read.
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM meta WHERE name = 'test'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("read after write: %d, %v", n, err)
	}
}

// The read connections can't write.
func TestReadsCantWrite(t *testing.T) {
	s := openTest(t)
	if _, err := s.read.Exec(`INSERT INTO meta (name, value) VALUES ('test', x'00')`); err == nil {
		t.Fatal("a read connection wrote")
	}
}
