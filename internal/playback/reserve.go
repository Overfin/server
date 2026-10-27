package playback

// A stream reserved for each profile (an account template's setting): each
// profile of the account owns one of its streams, and the rest, the limit
// less the number of profiles, are shared. A profile may start a stream
// when its own is free, or while shared ones last. A profile's own stream
// is never lent, so another profile's playback is never stopped to make
// room.

// SharedStreams is how many of an account's streams its profiles share:
// the limit less one for each profile, none below zero.
func SharedStreams(limit, profiles int) int {
	return max(0, limit-profiles)
}

// Reserved decides a new stream for a profile while each has one
// reserved. active are the account's playbacks, oldest first (as
// ForAccount lists them); profiles is how many the account has, and limit
// its stream limit. It answers whether the stream may start once the
// profile's own playbacks in replace, its oldest, stop: none when there's
// room already. ok is false when even stopping all of the profile's own
// wouldn't make room, as only other profiles' playbacks take it.
func Reserved(active []*Stream, profileID string, profiles, limit int) (replace []*Stream, ok bool) {
	var own []*Stream
	for _, s := range active {
		if s.ProfileID == profileID {
			own = append(own, s)
		}
	}
	shared := SharedStreams(limit, profiles)
	// Shared streams in use: every profile's playbacks beyond its first.
	perProfile := map[string]int{}
	for _, s := range active {
		perProfile[s.ProfileID]++
	}
	used := 0
	for _, n := range perProfile {
		used += max(0, n-1)
	}
	total := len(active)
	for stopped := 0; ; stopped++ {
		// The profile's own stream free, or a shared one; never more than
		// the limit in all, should the account have more profiles than
		// streams.
		if (len(own)-stopped == 0 || used < shared) && total < limit {
			return own[:stopped], true
		}
		if stopped == len(own) {
			return nil, false
		}
		// Stopping one of the profile's own: a shared one comes back if
		// the profile had more than its own.
		if len(own)-stopped > 1 {
			used--
		}
		total--
	}
}
