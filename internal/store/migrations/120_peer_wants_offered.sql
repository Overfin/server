-- Whether a library we want of a paired server was still in its offer at
-- our last reading of its libraries: one that isn't (it stopped offering
-- it, or changed its library IDs) is for our operator to choose again.
ALTER TABLE peer_wants ADD COLUMN offered INTEGER NOT NULL DEFAULT 1;
