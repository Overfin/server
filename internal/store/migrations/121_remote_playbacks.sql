-- This server's own people's playbacks on paired servers, by the paired
-- server and its playback ID only (no title, no profile): what a restart
-- tells each paired server is over, since playbacks live in memory and a
-- paired server would otherwise hold them, and count them against the
-- pairing's streams, until its idle timeout.
CREATE TABLE remote_playbacks (
    peer_id   TEXT NOT NULL REFERENCES peers (id) ON DELETE CASCADE,
    remote_id TEXT NOT NULL,
    PRIMARY KEY (peer_id, remote_id)
) STRICT;
