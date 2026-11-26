package outbound

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// A name's addresses tried in turn: the first that answers is the
// connection; none, every failure said; the caller giving up stops the
// tries.
func TestDirectTriesEachAddress(t *testing.T) {
	fakeNames(t, map[string][]string{"two.example.org": {"192.0.2.10", "192.0.2.11", "192.0.2.12"}})
	var tried []string
	answering := map[string]bool{"192.0.2.11:443": true}
	was := connect
	t.Cleanup(func() { connect = was })
	connect = func(ctx context.Context, _, addr string) (net.Conn, error) {
		tried = append(tried, addr)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !answering[addr] {
			return nil, errors.New("refused by " + addr)
		}
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}
	c, err := Direct(context.Background(), "tcp", "two.example.org:443")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if strings.Join(tried, " ") != "192.0.2.10:443 192.0.2.11:443" {
		t.Errorf("tried %v", tried)
	}
	tried, answering = nil, map[string]bool{}
	if _, err := Direct(context.Background(), "tcp", "two.example.org:443"); err == nil ||
		!strings.Contains(err.Error(), "192.0.2.10") || !strings.Contains(err.Error(), "192.0.2.12") || len(tried) != 3 {
		t.Errorf("none answering: %v, tried %v", err, tried)
	}
	tried = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Direct(ctx, "tcp", "two.example.org:443"); err == nil || len(tried) > 1 {
		t.Errorf("given up: %v, tried %v", err, tried)
	}
}

// Through has every connection made by another dialer (the backend's
// gateway), Transport's too, until it's set back.
func TestThrough(t *testing.T) {
	var asked []string
	Through(func(_ context.Context, _, addr string) (net.Conn, error) {
		asked = append(asked, addr)
		return nil, errors.New("through the gateway")
	})
	t.Cleanup(func() { Through(Direct) })
	if _, err := DialContext(context.Background(), "tcp", "peer.example.org:443"); err == nil || err.Error() != "through the gateway" {
		t.Errorf("dial: %v", err)
	}
	if _, err := Transport().DialContext(context.Background(), "tcp", "other.example.org:443"); err == nil {
		t.Error("the transport went around it")
	}
	if strings.Join(asked, " ") != "peer.example.org:443 other.example.org:443" {
		t.Errorf("asked %v", asked)
	}
}
