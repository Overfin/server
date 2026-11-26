package library

import "testing"

// A library's kind from Jellyfin's collection type, as the menu shows it;
// one it doesn't know, or none, mixed. A paired server's library by its
// ID here (32 hex digits) and its name, plain.
func TestLibraryKinds(t *testing.T) {
	for collection, want := range map[string]string{
		"movies": "movies", "tvshows": "shows", "music": "music", "musicvideos": "musicVideos", "books": "books",
		"photos": "photos", "homevideos": "homeVideos", "livetv": "liveTv", "boxsets": "collections",
		"playlists": "playlists", "": "mixed", "folders": "mixed", "Movies": "mixed",
	} {
		lib, ok := PeerLibraryOf(jfID, "Films", collection)
		if !ok || lib.Kind != want {
			t.Errorf("%q: %+v %v, want %s", collection, lib, ok, want)
		}
	}
	lib, ok := PeerLibraryOf(jfID, "Films <b>4K</b>", "movies")
	if !ok || lib.ID != LibraryPrefix+jfID || lib.Name != "Films 4K" || lib.Image != nil {
		t.Errorf("a paired server's library: %+v", lib)
	}
	for _, id := range []string{"", "../x", jfID + "0", "zz" + jfID[2:]} {
		if _, ok := PeerLibraryOf(id, "Films", "movies"); ok {
			t.Errorf("%q taken", id)
		}
	}
}
