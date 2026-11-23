package jellyfin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/overfin/server/internal/upstream"
)

// streamClient fetches media. Unlike the API client, it has no overall
// timeout, which would cut every film after a few seconds; it only limits
// how long Jellyfin may take to start answering. It never follows redirects.
var streamClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: 60 * time.Second, // transcodes can take a moment to start
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   16,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// StreamUserAgent is the user agent of Overfin's media requests to
// Jellyfin, the one its plugin's deliveries are started with.
const StreamUserAgent = "Overfin"

// Open starts fetching a stream: a media file, a playlist or a segment. path
// and query come from Overfin's own stream state, never from the client;
// rangeHeader is the client's Range header, already checked. The caller
// closes the body.
func (c *Client) Open(ctx context.Context, a Auth, path string, query url.Values, rangeHeader string) (*http.Response, error) {
	return c.open(ctx, streamClient, a, path, query, rangeHeader)
}

func (c *Client) open(ctx context.Context, client *http.Client, a Auth, path string, query url.Values, rangeHeader string) (*http.Response, error) {
	u := c.baseURL()
	u.Path += path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.header(a))
	// Jellyfin names a transcode's files after the request's user agent
	// too: Overfin's plugin starts its deliveries with this one, so the
	// segments Overfin asks are that transcode's, not another's.
	req.Header.Set("User-Agent", StreamUserAgent)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jellyfin: stream: %w", upstream.Plain(err))
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
		return res, nil
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	return nil, &StatusError{Op: "stream", Status: res.StatusCode}
}

// ReadSmall fetches a small text resource (a playlist) whole,
// refusing anything larger than max bytes.
func (c *Client) ReadSmall(ctx context.Context, a Auth, path string, query url.Values, max int) ([]byte, error) {
	return c.readSmall(ctx, streamClient, a, path, query, max)
}

// smallReadTime bounds a small read whole, its answer and its body (MSFP
// media, section 2.4: a playlist or a subtitle part is read within a
// bounded time). A film's own stream has no such bound.
var smallReadTime = 2 * time.Minute

func (c *Client) readSmall(ctx context.Context, client *http.Client, a Auth, path string, query url.Values, max int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, smallReadTime)
	defer cancel()
	res, err := c.open(ctx, client, a, path, query, "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, &StatusError{Op: "stream", Status: res.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("jellyfin: stream: response larger than %d bytes", max)
	}
	return b, nil
}
