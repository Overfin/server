-- A profile may be limited to some of the operator's categories (its
-- account's owner chooses, beside its libraries and hidden tags): it sees
-- the titles of any of them, and no title that no library has. NULL: no
-- such limit.
ALTER TABLE profiles ADD COLUMN categories TEXT;
