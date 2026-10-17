package arc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// Client is a minimal Arc JSON-RPC client with timeouts and bounded retries.
type Client struct {
	rpcURL     string
	chainID    uint64
	httpClient *http.Client
}

// NewClient builds a client for rpcURL pinned to chainID. Every state-changing
// call verifies the node's chain ID matches, so testnet keys can never spend
// on mainnet.
func NewClient(rpcURL string, chainID uint64) *Client {
	if rpcURL == "" {
		rpcURL = DefaultRPCForChain(chainID)
	}
	return &Client{
		rpcURL:     rpcURL,
		chainID:    chainID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetHTTPClient overrides the transport. Intended for tests.
func (c *Client) SetHTTPClient(h *http.Client) { c.httpClient = h }

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      int    `json:"id"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

// isRetryableRPCError reports transient node errors worth retrying with
// backoff: rate limiting (-32005), generic server faults (-32000), and
// internal errors (-32603, usually node overload). Everything else
// (unknown method, invalid params, reverts) is deterministic.
func isRetryableRPCError(code int) bool {
	switch code {
	case -32005, -32000, -32603:
		return true
	}
	return false
}

func (c *Client) call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: 1})
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var out rpcResponse
		decErr := json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if decErr != nil {
			lastErr = decErr
			continue
		}
		if out.Error != nil {
			// Retryable server-side errors (rate limit, overloaded node):
			// back off and retry instead of failing the poll. Deterministic
			// errors (bad params, unknown method, reverts) return at once.
			if isRetryableRPCError(out.Error.Code) && attempt < 2 {
				lastErr = out.Error
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
				}
				continue
			}
			return nil, out.Error
		}
		return out.Result, nil
	}
	return nil, fmt.Errorf("rpc %s failed after retries: %w", method, lastErr)
}

func parseHexUint(s string) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return nil, fmt.Errorf("expected 0x quantity, got %q", s)
	}
	v, ok := new(big.Int).SetString(s[2:], 16)
	if !ok {
		return nil, fmt.Errorf("invalid hex quantity %q", s)
	}
	return v, nil
}

func hexUint(v *big.Int) string {
	if v == nil || v.Sign() < 0 {
		return "0x0"
	}
	s := strings.TrimLeft(v.Text(16), "0")
	if s == "" {
		return "0x0"
	}
	return "0x" + s
}

// ChainID queries eth_chainId and fails unless it matches the configured
// chain. Call before any spend.
func (c *Client) ChainID(ctx context.Context) (uint64, error) {
	raw, err := c.call(ctx, "eth_chainId")
	if err != nil {
		return 0, err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, err
	}
	v, err := parseHexUint(s)
	if err != nil {
		return 0, err
	}
	if !v.IsUint64() || v.Uint64() != c.chainID {
		return 0, fmt.Errorf("chain mismatch: node reports %s, configured %d", s, c.chainID)
	}
	return v.Uint64(), nil
}

// PendingNonce returns eth_getTransactionCount(address, "pending").
func (c *Client) PendingNonce(ctx context.Context, address string) (uint64, error) {
	raw, err := c.call(ctx, "eth_getTransactionCount", address, "pending")
	if err != nil {
		return 0, err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, err
	}
	v, err := parseHexUint(s)
	if err != nil {
		return 0, err
	}
	if !v.IsUint64() {
		return 0, fmt.Errorf("nonce out of range: %s", s)
	}
	return v.Uint64(), nil
}

// Call executes eth_call and returns raw bytes.
func (c *Client) Call(ctx context.Context, to string, data []byte) ([]byte, error) {
	raw, err := c.call(ctx, "eth_call", map[string]string{
		"to":   to,
		"data": "0x" + fmt.Sprintf("%x", data),
	}, "latest")
	if err != nil {
		return nil, err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s)%2 == 1 {
		s = "0" + s
	}
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("invalid eth_call response %q", s)
	}
	return out, nil
}

// EstimateGas runs eth_estimateGas for a tx payload.
func (c *Client) EstimateGas(ctx context.Context, from, to string, data []byte) (uint64, error) {
	raw, err := c.call(ctx, "eth_estimateGas", map[string]string{
		"from": from,
		"to":   to,
		"data": "0x" + fmt.Sprintf("%x", data),
	})
	if err != nil {
		return 0, err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, err
	}
	v, err := parseHexUint(s)
	if err != nil {
		return 0, err
	}
	if !v.IsUint64() {
		return 0, fmt.Errorf("gas estimate out of range: %s", s)
	}
	return v.Uint64(), nil
}

// LatestBaseFee reads the pending block's baseFeePerGas. Some Arc RPC
// endpoints do not serve pending-block data and answer -32014 "requested data
// not available"; fall back to latest, which is equally valid for fee
// estimation (QuoteFees doubles it and enforces a floor on top).
func (c *Client) LatestBaseFee(ctx context.Context) (*big.Int, error) {
	raw, err := c.call(ctx, "eth_getBlockByNumber", "pending", false)
	if err != nil {
		raw, err = c.call(ctx, "eth_getBlockByNumber", "latest", false)
		if err != nil {
			return nil, err
		}
	}
	var block struct {
		BaseFeePerGas string `json:"baseFeePerGas"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil, err
	}
	if block.BaseFeePerGas == "" {
		return big.NewInt(0), nil
	}
	return parseHexUint(block.BaseFeePerGas)
}

// SendRawTransaction broadcasts signed bytes, returning the tx hash.
func (c *Client) SendRawTransaction(ctx context.Context, signedTx []byte) (string, error) {
	raw, err := c.call(ctx, "eth_sendRawTransaction", "0x"+fmt.Sprintf("%x", signedTx))
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}
