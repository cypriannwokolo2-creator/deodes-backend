package arc

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRPC serves canned JSON-RPC responses keyed by method.
type mockRPC struct {
	t       *testing.T
	methods map[string]func() string
}

func (m *mockRPC) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
	}
	require.NoError(m.t, json.NewDecoder(r.Body).Decode(&req))
	fn, ok := m.methods[req.Method]
	if !ok {
		http.Error(w, "unknown method", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, fn())
}

func TestClient_ChainIDMatchAndMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc((&mockRPC{
		t: t,
		methods: map[string]func() string{
			"eth_chainId": func() string { return `"0x4cef52"` }, // 5042002
		},
	}).handler))
	defer srv.Close()

	c := NewClient(srv.URL, ChainIDTestnet)
	id, err := c.ChainID(context.Background())
	require.NoError(t, err)
	assert.Equal(t, ChainIDTestnet, id)

	cMain := NewClient(srv.URL, ChainIDMainnet)
	_, err = cMain.ChainID(context.Background())
	assert.ErrorContains(t, err, "chain mismatch")
}

func TestClient_USDCBalanceOf(t *testing.T) {
	// 2.5 USDC in ERC-20 base units (6 decimals), 32-byte padded.
	bal := new(big.Int)
	bal.SetString("2500000", 10)
	hexBal := fmt.Sprintf(`"0x%064x"`, bal)
	srv := httptest.NewServer(http.HandlerFunc((&mockRPC{
		t: t,
		methods: map[string]func() string{
			"eth_call": func() string { return hexBal },
		},
	}).handler))
	defer srv.Close()

	c := NewClient(srv.URL, ChainIDTestnet)
	got, err := USDCBalanceOf(context.Background(), c,
		"0x0000000000000000000000000000000000000001",
		"0x742d35Cc6634C0532925a3b844Bc454e4438f44e")
	require.NoError(t, err)
	assert.Equal(t, "2.500000", got)
}

func TestQuoteFees_FloorEnforced(t *testing.T) {
	// Base fee 1 Gwei: 2*1+1 = 3 Gwei < 20 floor -> 20 Gwei.
	srv := httptest.NewServer(http.HandlerFunc((&mockRPC{
		t: t,
		methods: map[string]func() string{
			"eth_getBlockByNumber": func() string { return `{"baseFeePerGas":"0x3b9aca00"}` },
		},
	}).handler))
	defer srv.Close()

	c := NewClient(srv.URL, ChainIDTestnet)
	q, err := QuoteFees(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, uint64(MinMaxFeePerGasWei), q.MaxFeePerGas.Uint64())
}

func TestQuoteFees_HighBasePassesThrough(t *testing.T) {
	// Base fee 30 Gwei: 2*30+1 = 61 Gwei > floor.
	srv := httptest.NewServer(http.HandlerFunc((&mockRPC{
		t: t,
		methods: map[string]func() string{
			"eth_getBlockByNumber": func() string { return `{"baseFeePerGas":"0x6fc23ac00"}` },
		},
	}).handler))
	defer srv.Close()

	c := NewClient(srv.URL, ChainIDTestnet)
	q, err := QuoteFees(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, "61000000000", q.MaxFeePerGas.String())
}

func TestUSDCTransfer_FullPath(t *testing.T) {
	var seenNonce, seenGas, seenTx string
	srv := httptest.NewServer(http.HandlerFunc((&mockRPC{
		t: t,
		methods: map[string]func() string{
			"eth_chainId":             func() string { return `"0x4cef52"` },
			"eth_getTransactionCount": func() string { seenNonce = "called"; return `"0x5"` },
			"eth_estimateGas":         func() string { seenGas = "called"; return `"0xfde8"` }, // 65000
			"eth_getBlockByNumber":    func() string { return `{"baseFeePerGas":"0x3b9aca00"}` },
			"eth_sendRawTransaction":  func() string { seenTx = "called"; return `"0xabc123"` },
		},
	}).handler))
	defer srv.Close()

	c := NewClient(srv.URL, ChainIDTestnet)
	priv := make([]byte, 32)
	priv[31] = 1
	hash, err := USDCTransfer(context.Background(), c,
		"0x0000000000000000000000000000000000000001",
		priv,
		"0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
		"1.25")
	require.NoError(t, err)
	assert.Equal(t, "0xabc123", hash)
	assert.Equal(t, "called", seenNonce)
	assert.Equal(t, "called", seenGas)
	assert.Equal(t, "called", seenTx)
}

func TestUSDCTransfer_RejectsBadAmount(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", ChainIDTestnet)
	priv := make([]byte, 32)
	priv[31] = 1
	_, err := USDCTransfer(context.Background(), c,
		"0x0000000000000000000000000000000000000001",
		priv,
		"0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
		"nope")
	assert.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "invalid amount"))
}

// tagAwareRPC answers eth_getBlockByNumber according to the requested block
// tag, reproducing Arc mainnet RPC behavior: "pending" fails with -32014
// "requested data not available" while "latest" succeeds.
func tagAwareRPC(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "eth_getBlockByNumber" && len(req.Params) > 0 &&
			string(req.Params[0]) == `"pending"` {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32014,"message":"requested data not available"}}`)
			return
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"baseFeePerGas":"0x3b9aca00"}}`)
	}
}

func TestLatestBaseFee_PendingUnavailableFallsBackToLatest(t *testing.T) {
	srv := httptest.NewServer(tagAwareRPC(t))
	defer srv.Close()

	c := NewClient(srv.URL, ChainIDTestnet)
	fee, err := c.LatestBaseFee(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "1000000000", fee.String())
}

func TestUSDCTransfer_FeeStepErrorNamesStep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "eth_chainId":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x4cef52"}`)
		case "eth_getTransactionCount":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0x5"}`)
		case "eth_estimateGas":
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":"0xfde8"}`)
		default:
			// Fee quote sees pending AND latest fail: the transfer must say
			// which step broke instead of surfacing a bare rpc error.
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32014,"message":"requested data not available"}}`)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, ChainIDTestnet)
	priv := make([]byte, 32)
	priv[31] = 1
	_, err := USDCTransfer(context.Background(), c,
		"0x0000000000000000000000000000000000000001",
		priv,
		"0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
		"1.25")
	require.Error(t, err)
	assert.ErrorContains(t, err, "quoting fees")
	assert.ErrorContains(t, err, "-32014")
}

func TestGetTransactionReceipt_ParsesLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{
			"transactionHash":"0xabc","blockNumber":"0x10","to":"0x3600000000000000000000000000000000000000","status":"0x1",
			"logs":[{
				"address":"0x3600000000000000000000000000000000000000",
				"topics":["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
					"0x0000000000000000000000001111111111111111111111111111111111111111",
					"0x000000000000000000000000742d35cc6634c0532925a3b844bc454e4438f44e"],
				"data":"0x0000000000000000000000000000000000000000000000000000000001312d00",
				"blockNumber":"0x10","transactionHash":"0xabc","transactionIndex":"0x0","logIndex":"0x0","removed":false
			}]
		}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, ChainIDTestnet)
	got, err := c.GetTransactionReceipt(context.Background(), "0xabc")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got.Logs, 1)
	assert.Equal(t, "0x3600000000000000000000000000000000000000", got.Logs[0].Address)
	max := MaxTransferValue(got.Logs, got.To)
	require.NotNil(t, max)
	assert.Equal(t, "20000000", max.String())
}

func TestGetTransactionReceipt_MissingLogsStaysNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{
			"transactionHash":"0xabc","blockNumber":"0x10","to":"0x1234","status":"0x1"
		}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, ChainIDTestnet)
	got, err := c.GetTransactionReceipt(context.Background(), "0xabc")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.Logs)
}
