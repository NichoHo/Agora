package market

// ponytail: one struct with three POSTs, no interface, no gRPC. Market is the
// only caller and the payment path must be synchronous anyway (the buyer waits).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"agora/internal/tracing"
)

var (
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrAlreadySettled    = errors.New("already settled")
)

type PayClient struct {
	BaseURL string
	Token   string // PAY_INTERNAL_TOKEN
}

var httpClient = tracing.Client()

func (p *PayClient) post(ctx context.Context, path string, body map[string]any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", p.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", p.Token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		return nil
	case 402:
		return ErrInsufficientFunds
	case 409:
		return ErrAlreadySettled
	default:
		return fmt.Errorf("pay %s: status %d", path, resp.StatusCode)
	}
}

func (p *PayClient) Fund(ctx context.Context, orderID, buyerID string, amount int64) error {
	return p.post(ctx, "/internal/escrow/fund",
		map[string]any{"order_id": orderID, "buyer_id": buyerID, "amount_minor": amount})
}

func (p *PayClient) Release(ctx context.Context, orderID, sellerID string) error {
	return p.post(ctx, "/internal/escrow/release",
		map[string]any{"order_id": orderID, "seller_id": sellerID})
}

func (p *PayClient) Refund(ctx context.Context, orderID, buyerID string) error {
	return p.post(ctx, "/internal/escrow/refund",
		map[string]any{"order_id": orderID, "buyer_id": buyerID})
}
