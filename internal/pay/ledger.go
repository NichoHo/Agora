// Package pay: double-entry ledger with escrow semantics.
//
// Money truths this file maintains:
//   - every transfer writes two signed entries that sum to zero;
//   - account.balance always equals the sum of that account's entries;
//   - a user balance can never go negative (checked here AND by a DB constraint);
//   - an idempotency key executes at most once; replays return the original.
package pay

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"agora/internal/tracing"
)

var (
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrAlreadySettled    = errors.New("order already released or refunded")
)

// FeeDivisor: platform takes 1/10 of the price on release, floor division.
const FeeDivisor = 10

type Ledger struct {
	Pool *pgxpool.Pool
}

type Transfer struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotency_key"`
	FromAccount    string    `json:"from_account"`
	ToAccount      string    `json:"to_account"`
	AmountMinor    int64     `json:"amount_minor"`
	Kind           string    `json:"kind"`
	Reference      string    `json:"reference"`
	CreatedAt      time.Time `json:"created_at"`
}

// AccountFor returns (creating if needed) the account for an owner.
// ownerID is nil for the escrow/platform/external singletons.
func (l *Ledger) AccountFor(ctx context.Context, ownerType string, ownerID *string) (string, error) {
	const sel = `SELECT id FROM pay.accounts WHERE owner_type = $1 AND owner_id IS NOT DISTINCT FROM $2`
	var id string
	if err := l.Pool.QueryRow(ctx, sel, ownerType, ownerID).Scan(&id); err == nil {
		return id, nil
	}
	err := l.Pool.QueryRow(ctx,
		`INSERT INTO pay.accounts (owner_type, owner_id) VALUES ($1, $2) RETURNING id`,
		ownerType, ownerID).Scan(&id)
	if isUniqueViolation(err, "accounts_owner_idx") {
		// lost a creation race, the row exists now
		if err2 := l.Pool.QueryRow(ctx, sel, ownerType, ownerID).Scan(&id); err2 == nil {
			return id, nil
		}
	}
	return id, err
}

type transferArgs struct {
	key, kind, reference string
	from, to             string // account ids
	amount               int64
}

// transferInTx moves money inside an existing transaction. Both account rows
// are locked FOR UPDATE in id order (lock ordering, so concurrent transfers
// touching the same accounts serialize instead of deadlocking).
func transferInTx(ctx context.Context, tx pgx.Tx, a transferArgs) (Transfer, error) {
	if a.amount <= 0 {
		return Transfer{}, fmt.Errorf("amount must be positive")
	}
	first, second := a.from, a.to
	if second < first {
		first, second = second, first
	}
	for _, acct := range []string{first, second} {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM pay.accounts WHERE id = $1 FOR UPDATE`, acct); err != nil {
			return Transfer{}, err
		}
	}
	var fromType string
	var fromBalance int64
	if err := tx.QueryRow(ctx,
		`SELECT owner_type, balance FROM pay.accounts WHERE id = $1`, a.from).Scan(&fromType, &fromBalance); err != nil {
		return Transfer{}, err
	}
	if fromType != "external" && fromBalance < a.amount {
		return Transfer{}, ErrInsufficientFunds
	}
	var t Transfer
	err := tx.QueryRow(ctx,
		`INSERT INTO pay.transfers (idempotency_key, from_account, to_account, amount_minor, kind, reference)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, idempotency_key, from_account, to_account, amount_minor, kind, reference, created_at`,
		a.key, a.from, a.to, a.amount, a.kind, a.reference).
		Scan(&t.ID, &t.IdempotencyKey, &t.FromAccount, &t.ToAccount, &t.AmountMinor, &t.Kind, &t.Reference, &t.CreatedAt)
	if err != nil {
		return Transfer{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO pay.entries (transfer_id, account_id, amount_minor) VALUES ($1, $2, $3), ($1, $4, $5)`,
		t.ID, a.from, -a.amount, a.to, a.amount); err != nil {
		return Transfer{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE pay.accounts SET balance = balance - $2 WHERE id = $1`, a.from, a.amount); err != nil {
		return Transfer{}, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE pay.accounts SET balance = balance + $2 WHERE id = $1`, a.to, a.amount); err != nil {
		return Transfer{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO pay.outbox (topic, payload) VALUES ('transfer.created',
		   jsonb_build_object('transfer_id', $1::text, 'kind', $2::text, 'reference', $3::text,
		                      'amount_minor', $4::bigint, '_trace', $5::text))`,
		t.ID, t.Kind, t.Reference, t.AmountMinor, tracing.Traceparent(ctx)); err != nil {
		return Transfer{}, err
	}
	return t, nil
}

func (l *Ledger) existing(ctx context.Context, key string) (Transfer, error) {
	var t Transfer
	err := l.Pool.QueryRow(ctx,
		`SELECT id, idempotency_key, from_account, to_account, amount_minor, kind, reference, created_at
		 FROM pay.transfers WHERE idempotency_key = $1`, key).
		Scan(&t.ID, &t.IdempotencyKey, &t.FromAccount, &t.ToAccount, &t.AmountMinor, &t.Kind, &t.Reference, &t.CreatedAt)
	return t, err
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// transfer runs one transfer in its own transaction with idempotent replay.
func (l *Ledger) transfer(ctx context.Context, a transferArgs) (Transfer, error) {
	// fast path: replay
	if t, err := l.existing(ctx, a.key); err == nil {
		return t, nil
	}
	tx, err := l.Pool.Begin(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback(ctx)
	t, err := transferInTx(ctx, tx, a)
	if isUniqueViolation(err, "transfers_idempotency_key_key") {
		// lost the race with a concurrent identical request, return theirs
		return l.existing(ctx, a.key)
	}
	if err != nil {
		return Transfer{}, err
	}
	return t, tx.Commit(ctx)
}

func (l *Ledger) Deposit(ctx context.Context, key, userID string, amount int64) (Transfer, error) {
	ext, err := l.AccountFor(ctx, "external", nil)
	if err != nil {
		return Transfer{}, err
	}
	user, err := l.AccountFor(ctx, "user", &userID)
	if err != nil {
		return Transfer{}, err
	}
	return l.transfer(ctx, transferArgs{key: key, kind: "deposit", from: ext, to: user, amount: amount})
}

// ponytail: every order funds into this one shared escrow account, so
// transferInTx's FOR UPDATE row lock on it serializes all concurrent
// FundEscrow calls system-wide. Correct and simple; measured ceiling is
// ~150-200 fundings/sec on this dev machine before queueing dominates
// latency (load/results/05-ledger-write.log). Shard escrow the way
// internal/sale already shards Redis drop units if real throughput needs
// to clear that.
func (l *Ledger) FundEscrow(ctx context.Context, orderID, buyerID string, amount int64) (Transfer, error) {
	escrow, err := l.AccountFor(ctx, "escrow", nil)
	if err != nil {
		return Transfer{}, err
	}
	buyer, err := l.AccountFor(ctx, "user", &buyerID)
	if err != nil {
		return Transfer{}, err
	}
	return l.transfer(ctx, transferArgs{
		key: "fund:" + orderID, kind: "escrow_fund", reference: "order:" + orderID,
		from: buyer, to: escrow, amount: amount,
	})
}

// settle serializes release-vs-refund per order by locking the fund transfer
// row, then guards against the opposite settlement already existing.
func (l *Ledger) settle(ctx context.Context, orderID string, do func(tx pgx.Tx, fund Transfer) error) error {
	tx, err := l.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var fund Transfer
	err = tx.QueryRow(ctx,
		`SELECT id, idempotency_key, from_account, to_account, amount_minor, kind, reference, created_at
		 FROM pay.transfers WHERE idempotency_key = $1 FOR UPDATE`, "fund:"+orderID).
		Scan(&fund.ID, &fund.IdempotencyKey, &fund.FromAccount, &fund.ToAccount,
			&fund.AmountMinor, &fund.Kind, &fund.Reference, &fund.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("order %s was never funded", orderID)
	}
	if err != nil {
		return err
	}
	if err := do(tx, fund); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (l *Ledger) settled(ctx context.Context, tx pgx.Tx, orderID, otherKind string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pay.transfers WHERE reference = $1 AND kind = $2)`,
		"order:"+orderID, otherKind).Scan(&exists)
	return exists, err
}

func (l *Ledger) ReleaseEscrow(ctx context.Context, orderID, sellerID string) (Transfer, error) {
	if t, err := l.existing(ctx, "release:"+orderID); err == nil {
		return t, nil // idempotent replay
	}
	var out Transfer
	err := l.settle(ctx, orderID, func(tx pgx.Tx, fund Transfer) error {
		if refunded, err := l.settled(ctx, tx, orderID, "escrow_refund"); err != nil {
			return err
		} else if refunded {
			return ErrAlreadySettled
		}
		escrow := fund.ToAccount
		seller, err := l.AccountFor(ctx, "user", &sellerID)
		if err != nil {
			return err
		}
		platform, err := l.AccountFor(ctx, "platform", nil)
		if err != nil {
			return err
		}
		fee := fund.AmountMinor / FeeDivisor
		out, err = transferInTx(ctx, tx, transferArgs{
			key: "release:" + orderID, kind: "escrow_release", reference: "order:" + orderID,
			from: escrow, to: seller, amount: fund.AmountMinor - fee,
		})
		if err != nil {
			return err
		}
		if fee > 0 {
			if _, err := transferInTx(ctx, tx, transferArgs{
				key: "release_fee:" + orderID, kind: "fee", reference: "order:" + orderID,
				from: escrow, to: platform, amount: fee,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if isUniqueViolation(err, "transfers_idempotency_key_key") {
		return l.existing(ctx, "release:"+orderID)
	}
	if err != nil {
		return Transfer{}, err
	}
	return out, nil
}

func (l *Ledger) RefundEscrow(ctx context.Context, orderID, buyerID string) (Transfer, error) {
	if t, err := l.existing(ctx, "refund:"+orderID); err == nil {
		return t, nil
	}
	var out Transfer
	err := l.settle(ctx, orderID, func(tx pgx.Tx, fund Transfer) error {
		if released, err := l.settled(ctx, tx, orderID, "escrow_release"); err != nil {
			return err
		} else if released {
			return ErrAlreadySettled
		}
		out2, err := transferInTx(ctx, tx, transferArgs{
			key: "refund:" + orderID, kind: "escrow_refund", reference: "order:" + orderID,
			from: fund.ToAccount, to: fund.FromAccount, amount: fund.AmountMinor,
		})
		out = out2
		return err
	})
	if isUniqueViolation(err, "transfers_idempotency_key_key") {
		return l.existing(ctx, "refund:"+orderID)
	}
	if err != nil {
		return Transfer{}, err
	}
	return out, nil
}

type WalletEntry struct {
	AmountMinor int64     `json:"amount_minor"`
	Kind        string    `json:"kind"`
	Reference   string    `json:"reference"`
	CreatedAt   time.Time `json:"created_at"`
}

type Wallet struct {
	BalanceMinor int64         `json:"balance_minor"`
	Entries      []WalletEntry `json:"entries"`
}

func (l *Ledger) Wallet(ctx context.Context, userID string) (Wallet, error) {
	w := Wallet{Entries: []WalletEntry{}}
	acct, err := l.AccountFor(ctx, "user", &userID)
	if err != nil {
		return w, err
	}
	if err := l.Pool.QueryRow(ctx,
		`SELECT balance FROM pay.accounts WHERE id = $1`, acct).Scan(&w.BalanceMinor); err != nil {
		return w, err
	}
	rows, err := l.Pool.Query(ctx,
		`SELECT e.amount_minor, t.kind, t.reference, e.created_at
		 FROM pay.entries e JOIN pay.transfers t ON t.id = e.transfer_id
		 WHERE e.account_id = $1 ORDER BY e.id DESC LIMIT 50`, acct)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	for rows.Next() {
		var e WalletEntry
		if err := rows.Scan(&e.AmountMinor, &e.Kind, &e.Reference, &e.CreatedAt); err != nil {
			return w, err
		}
		w.Entries = append(w.Entries, e)
	}
	return w, rows.Err()
}
