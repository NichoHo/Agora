-- Run after any load scenario touching pay (04, 05) to confirm money was
-- conserved under load, the same invariants internal/pay/ledger_test.go
-- checks in-process. All three must return 0 / true.

-- Every transfer's entries sum to zero individually.
SELECT count(*) AS bad_transfers FROM (
  SELECT transfer_id FROM pay.entries GROUP BY transfer_id HAVING sum(amount_minor) <> 0
) x;

-- The signed sum of every entry, system-wide, is exactly zero.
SELECT coalesce(sum(amount_minor), 0) AS global_sum FROM pay.entries;

-- Every account's cached balance equals the sum of its own entries.
SELECT count(*) AS drifted_accounts FROM pay.accounts a
WHERE a.balance <> coalesce((SELECT sum(e.amount_minor) FROM pay.entries e WHERE e.account_id = a.id), 0);
