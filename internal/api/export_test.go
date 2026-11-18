package api

// TakeSubtitleSlots takes every slot for reads held whole, for a test,
// and gives what frees them.
func TakeSubtitleSlots() (free func()) {
	for range cap(subtitleReads) {
		subtitleReads <- struct{}{}
	}
	return func() {
		for range cap(subtitleReads) {
			<-subtitleReads
		}
	}
}
