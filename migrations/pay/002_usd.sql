-- Single currency is USD (cents), matching switch.
ALTER TABLE transfers ALTER COLUMN currency SET DEFAULT 'USD';
UPDATE transfers SET currency = 'USD' WHERE currency <> 'USD';
