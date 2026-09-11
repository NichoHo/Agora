-- Demo sold-history moved to USD cents with new items. Empty the table so
-- migrate() reseeds it from COMPARABLES on this boot.
DELETE FROM comparables;
