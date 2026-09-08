package pay

// ponytail: one struct, plain HTTP, no generated client, matching market's
// own PayClient/MarketClient style. See docs/adr/0003-pay-switch-integration.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"agora/internal/tracing"
)

var switchHTTPClient = tracing.Client()

// demoCardToken: agora collects no real card details anywhere in the
// storefront (Section 4: simulation, synthetic data, no real payment
// provider). Every escrow funding authorizes against the same switch test
// card, tokenized once at startup. "4242424242424242" is Luhn-valid and
// matches switch's own BinDirectory test-BIN allowlist (prefix 42424242,
// see Switch/gateway/.../vault/BinDirectory.java); the 411111 BIN named in
// Switch/README.md isn't actually in that allowlist.
const demoCardPAN = "4242424242424242"

type SwitchClient struct {
	BaseURL string
	APIKey  string
	token   string // set by EnsureToken
}

func (c *SwitchClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	return switchHTTPClient.Do(req)
}

// EnsureToken tokenizes the demo test card once and caches the token. Safe
// to call repeatedly; switch's vault returns the same token for a PAN it has
// already tokenized for this merchant.
func (c *SwitchClient) EnsureToken(ctx context.Context) error {
	if c.token != "" {
		return nil
	}
	resp, err := c.do(ctx, "POST", "/v1/tokens", map[string]any{
		"pan": demoCardPAN, "expMonth": 12, "expYear": 2031,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return fmt.Errorf("switch tokenize: status %d", resp.StatusCode)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	c.token = out.Token
	return nil
}

type authResult struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// Authorize creates and (on approval) captures a payment for one order.
// merchantReference is the order id, so switch's own idempotency (keyed on
// merchantReference server-side per its §16 contract... actually keyed on
// the Idempotency-Key header, see below) lines up with a retried Fund call.
func (c *SwitchClient) authorize(ctx context.Context, orderID string, amountMinor int64, currency string) (authResult, error) {
	if err := c.EnsureToken(ctx); err != nil {
		return authResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/payments", jsonBody(map[string]any{
		"merchantReference": orderID,
		"cardToken":         c.token,
		"amount":            map[string]any{"minor": amountMinor, "currency": currency},
		"capture":           true,
	}))
	if err != nil {
		return authResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Idempotency-Key", "escrow-fund:"+orderID)
	resp, err := switchHTTPClient.Do(req)
	if err != nil {
		return authResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 402 {
		return authResult{}, fmt.Errorf("switch authorize: status %d", resp.StatusCode)
	}
	var out authResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return authResult{}, err
	}
	return out, nil
}

func (c *SwitchClient) getPayment(ctx context.Context, id string) (authResult, error) {
	resp, err := c.do(ctx, "GET", "/v1/payments/"+id, nil)
	if err != nil {
		return authResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return authResult{}, fmt.Errorf("switch get payment: status %d", resp.StatusCode)
	}
	var out authResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return authResult{}, err
	}
	return out, nil
}

// AuthorizeAndResolve authorizes a card charge for the order and, if switch
// reports AUTH_UNKNOWN (the acquirer response was lost), polls switch's own
// payment resource until switch's internal StatusProbeJob resolves it.
// switch already runs that resolver every 30s against the acquirer, so pay
// only needs patience, not its own resolver; see the ADR for why a bounded
// synchronous wait was chosen over an async callback here.
func (c *SwitchClient) AuthorizeAndResolve(ctx context.Context, orderID string, amountMinor int64, currency string, maxWait time.Duration) (state string, err error) {
	res, err := c.authorize(ctx, orderID, amountMinor, currency)
	if err != nil {
		return "", err
	}
	if res.State != "AUTH_UNKNOWN" {
		return res.State, nil
	}
	deadline := time.Now().Add(maxWait)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "AUTH_UNKNOWN", ctx.Err()
		case <-ticker.C:
		}
		res, err = c.getPayment(ctx, res.ID)
		if err != nil {
			continue // transient; keep polling until maxWait
		}
		if res.State != "AUTH_UNKNOWN" {
			return res.State, nil
		}
	}
	return "AUTH_UNKNOWN", nil // still unresolved when pay gave up waiting
}

func jsonBody(v any) *bytes.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}
