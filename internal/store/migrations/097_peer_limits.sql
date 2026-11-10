-- Federation: what each pairing may use of this server at once (ROADMAP.md
-- milestone 6), set by this server's operator: streams, transcodes (apart:
-- they're what costs) and a bitrate cap (0: none). And what the peer says
-- we may use of it, so we never offer what it would refuse.
ALTER TABLE peers ADD COLUMN max_streams INTEGER NOT NULL DEFAULT 2;
ALTER TABLE peers ADD COLUMN max_transcodes INTEGER NOT NULL DEFAULT 1;
ALTER TABLE peers ADD COLUMN max_bitrate_kbps INTEGER NOT NULL DEFAULT 0;
ALTER TABLE peers ADD COLUMN their_streams INTEGER;
ALTER TABLE peers ADD COLUMN their_transcodes INTEGER;
ALTER TABLE peers ADD COLUMN their_bitrate_kbps INTEGER;
