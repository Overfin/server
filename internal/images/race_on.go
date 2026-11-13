//go:build race

package images

// raceBuild: the race detector reserves far more address space than a
// worker's cap allows, so test builds with it go without the cap.
const raceBuild = true
