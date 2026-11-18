//go:build !unix

package jsonfile

import "os"

// lock does nothing where flock isn't there: one process per directory
// is then the host's to make sure of.
func lock(*os.File) error { return nil }
