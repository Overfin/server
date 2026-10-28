-- An account template may limit its accounts' profiles to some categories
-- (the operator's), the owner profile's included, on top of what each
-- profile's own limits say. NULL: no such limit.
ALTER TABLE account_templates ADD COLUMN categories TEXT;
