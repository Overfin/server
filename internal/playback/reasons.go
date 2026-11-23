package playback

import "slices"

// Reasons are the codes a player of this server says why it chose its
// delivery (the SDK's decision), for the control panel's "Now playing",
// which translates them: a closed list, never free text from a player.
//
//   - as-is: the copy plays as it is (file);
//   - container: its streams play, its container doesn't (remux);
//   - audio-track: the audio chosen isn't the one the device plays of the
//     file (remux);
//   - audio-codec: the audio doesn't play on the device (converted);
//   - video-codec: the picture doesn't play on the device (converted);
//   - picture-range: the device doesn't show the picture's range (HDR
//     converted);
//   - bitrate: the copy is above the quality chosen (converted);
//   - fallback: the play before failed on the device, a step down;
//   - quirk: a known quirk of the device;
//   - in-doubt: what the copy holds isn't known well enough (the
//     baseline).
var Reasons = []string{"as-is", "container", "audio-track", "audio-codec", "video-codec", "picture-range",
	"bitrate", "fallback", "quirk", "in-doubt"}

// KnownReason says whether r is one of Reasons.
func KnownReason(r string) bool { return slices.Contains(Reasons, r) }
