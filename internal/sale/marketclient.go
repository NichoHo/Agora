package sale

// ponytail: like market's own PayClient, one struct with plain HTTP calls,
// no interface, no client-gen. Market has no service-to-service auth for
// orders (only user JWTs), so sale forwards the caller's own bearer token
// instead of inventing a new internal endpoint on market. See
// docs/adr/0002-drop-units-as-market-listings.md.

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
	ErrListingUnavailable = errors.New("listing unavailable")
	ErrPaymentFailed       = errors.New("payment failed")
)

type MarketClient struct {
	BaseURL string
}

var marketHTTPClient = tracing.Client()

func (m *MarketClient) do(ctx context.Context, method, path, bearer string, body any) (*http.Response, error) {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.BaseURL+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	return marketHTTPClient.Do(req)
}

// CreateListing mints one sellable unit for a drop, called with the seller's
// own bearer token at drop-open time.
func (m *MarketClient) CreateListing(ctx context.Context, sellerBearer string, title, description, imageURL string, priceMinor int64) (string, error) {
	resp, err := m.do(ctx, "POST", "/listings", sellerBearer, map[string]any{
		"title": title, "description": description, "image_url": imageURL, "price_minor": priceMinor,
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return "", fmt.Errorf("market create listing: status %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// CreateOrder claims a drop unit into a real market order, called with the
// buyer's own bearer token. Returns ErrListingUnavailable if the unit was
// already claimed in market (sale's Redis release and market's own 15-minute
// reservation sweep aren't perfectly synchronized; see the ADR).
func (m *MarketClient) CreateOrder(ctx context.Context, buyerBearer, listingID string) (string, error) {
	resp, err := m.do(ctx, "POST", "/orders", buyerBearer, map[string]any{"listing_id": listingID})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 409 {
		return "", ErrListingUnavailable
	}
	if resp.StatusCode != 201 {
		return "", fmt.Errorf("market create order: status %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// PayOrder settles a just-created order immediately, called with the
// buyer's own bearer token right after CreateOrder in the same checkout
// request. A drop is time-pressured; there's no reason to make the buyer
// come back for a second click the way a browse-and-decide purchase would.
// Surfaces market's existing 402/409/502 outcomes (insufficient funds,
// already settled, or pay unreachable/still resolving an indeterminate
// card charge) as ErrPaymentFailed; the reservation stays payment_pending
// either way; see docs/adr/0003 and docs/writeups/04.
func (m *MarketClient) PayOrder(ctx context.Context, buyerBearer, orderID string) error {
	resp, err := m.do(ctx, "POST", "/orders/"+orderID+"/pay", buyerBearer, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ErrPaymentFailed
	}
	return nil
}
