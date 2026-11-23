package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/overfin/server/internal/upstream"
)

// Deliveries: remuxes and transcodes made by Jellyfin's own transcoder,
// as Overfin's plugin (0.9.0 and later) starts, follows and stops them.
// Overfin sends IDs and closed values only; the plugin answers IDs,
// closed values and Jellyfin's media playlist, never a path.

// PluginEncoders are what the plugin says this Jellyfin encodes now.
type PluginEncoders struct {
	Video, Audio []string
	// Hardware: Jellyfin encodes on a GPU; ToneMapping: it maps HDR to SDR.
	Hardware, ToneMapping bool
}

// Encoders asks the plugin what Jellyfin encodes now.
func (c *Client) Encoders(ctx context.Context, admin Auth) (*PluginEncoders, error) {
	var out PluginEncoders
	return &out, c.do(ctx, "the encoders", http.MethodGet, "/Overfin/Encoders", admin, nil, &out)
}

// DeliveryOrder is a delivery as the plugin takes it.
type DeliveryOrder struct {
	ItemId, SourceId string
	Segments         string // "ts" or "mp4"
	StartTicks       int64
	Video            *DeliveryVideo `json:",omitempty"`
	Audio            *DeliveryAudio `json:",omitempty"`
}

// DeliveryVideo is a delivery's picture: Copy, or converted.
type DeliveryVideo struct {
	Copy       bool
	Codec      string `json:",omitempty"`
	MaxHeight  int    `json:",omitempty"`
	MaxBitrate int    `json:",omitempty"`
	ToneMap    bool
}

// DeliveryAudio is a delivery's audio stream (Jellyfin's index): Copy,
// or converted.
type DeliveryAudio struct {
	Index    int
	Copy     bool
	Codec    string `json:",omitempty"`
	Channels int    `json:",omitempty"`
}

// DeliveryMade is what the plugin says Jellyfin made, as it read it when
// Jellyfin's ffmpeg started, and the media playlist Jellyfin answered.
type DeliveryMade struct {
	Id            string
	VideoCopied   bool
	VideoCodec    string
	Width, Height int
	VideoBitrate  int
	ToneMapped    bool
	AudioCopied   bool
	AudioCodec    string
	AudioChannels int
	AudioIndex    int
	Segments      string
	Playlist      string
}

// DeliveryRefused is the plugin's refusal of a delivery: a closed code
// (no-encoder, hdr-kept-needs-gpu, made-differs:height-cap…).
type DeliveryRefused struct {
	Code string
}

func (e *DeliveryRefused) Error() string { return "jellyfin: delivery refused: " + e.Code }

// refusalCode is what a refusal's code may be: closed codes only.
var refusalCode = regexp.MustCompile(`^[a-z0-9:,-]{1,200}$`)

// DeliveryID is the plugin's delivery IDs: 32 hex digits.
var DeliveryID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// deliveryStartTime bounds a delivery's start: the plugin waits for its
// first segment (a transcode's may take a while on a CPU).
const deliveryStartTime = 2 * time.Minute

// StartDelivery asks the plugin for a delivery. A refusal is a
// *DeliveryRefused.
func (c *Client) StartDelivery(ctx context.Context, admin Auth, o DeliveryOrder) (*DeliveryMade, error) {
	ctx, cancel := context.WithTimeout(ctx, deliveryStartTime)
	defer cancel()
	body, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	u := c.baseURL()
	u.Path += "/Overfin/Deliveries"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.header(admin))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	res, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jellyfin: a delivery: %w", upstream.Plain(err))
	}
	defer res.Body.Close()
	limited := io.LimitReader(res.Body, maxResponse)
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var out DeliveryMade
		if err := json.NewDecoder(limited).Decode(&out); err != nil {
			return nil, fmt.Errorf("jellyfin: a delivery: decoding response: %w", err)
		}
		if !DeliveryID.MatchString(out.Id) {
			return nil, fmt.Errorf("jellyfin: a delivery: a malformed ID")
		}
		return &out, nil
	case http.StatusConflict:
		var out struct{ Refused string }
		if err := json.NewDecoder(limited).Decode(&out); err != nil || !refusalCode.MatchString(out.Refused) {
			return nil, &DeliveryRefused{Code: "refused"}
		}
		return nil, &DeliveryRefused{Code: out.Refused}
	}
	io.Copy(io.Discard, limited)
	return nil, &StatusError{Op: "a delivery", Status: res.StatusCode}
}

// PingDelivery keeps a delivery alive; a 404 StatusError when it's gone.
func (c *Client) PingDelivery(ctx context.Context, admin Auth, id string) error {
	if !DeliveryID.MatchString(id) {
		return &StatusError{Op: "a delivery's ping", Status: http.StatusNotFound}
	}
	return c.do(ctx, "a delivery's ping", http.MethodPost, "/Overfin/Deliveries/"+id+"/Ping", admin, nil, nil)
}

// StopDelivery stops a delivery; one already gone is no error.
func (c *Client) StopDelivery(ctx context.Context, admin Auth, id string) error {
	if !DeliveryID.MatchString(id) {
		return nil
	}
	err := c.do(ctx, "a delivery's stop", http.MethodDelete, "/Overfin/Deliveries/"+id, admin, nil, nil)
	if IsStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

// ClearDeliveries stops every delivery the plugin has.
func (c *Client) ClearDeliveries(ctx context.Context, admin Auth) error {
	return c.do(ctx, "the deliveries' clear", http.MethodDelete, "/Overfin/Deliveries", admin, nil, nil)
}
