-- Federation: listings read paired servers' films and series by kind
-- (store.listedRows), each with its library, beside this server's own: an
-- index for that, so a peer's large index doesn't make every listing go
-- through all of it.
CREATE INDEX peer_titles_listing ON peer_titles (kind, peer_id, library_id);
