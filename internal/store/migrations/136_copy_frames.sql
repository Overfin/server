-- A kept probe whose HDR picture's first frames aren't read yet (HDR10+,
-- the alternative transfer SEI): a probe imported from a header read
-- made without them. The copy index reads them last, at its lowest
-- priority, then clears it.
ALTER TABLE copy_probes ADD COLUMN frames_pending INTEGER NOT NULL DEFAULT 0;
