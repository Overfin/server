package admin

import "io/fs"

// noListing serves a folder's files, never a list of them: a folder other
// than the top one (whose index.html is the page) is not there.
type noListing struct{ fs.FS }

func (n noListing) Open(name string) (fs.File, error) {
	f, err := n.FS.Open(name)
	if err != nil || name == "." {
		return f, err
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}
