package arc

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixed test key (secp256k1 private key 1, left-padded to 32 bytes).
const testPrivHex = "0000000000000000000000000000000000000000000000000000000000000001"

func TestKeccak256_EmptyVector(t *testing.T) {
	assert.Equal(t,
		"c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
		hex.EncodeToString(keccak256(nil)))
}

func TestRLP_Vectors(t *testing.T) {
	assert.Equal(t, "80", hex.EncodeToString(rlpUint(nil)))
	assert.Equal(t, "80", hex.EncodeToString(rlpUint(big.NewInt(0))))
	assert.Equal(t, "01", hex.EncodeToString(rlpUint(big.NewInt(1))))
	assert.Equal(t, "820400", hex.EncodeToString(rlpUint(big.NewInt(1024))))
	assert.Equal(t, "83646f67", hex.EncodeToString(rlpEncode([]byte("dog"), false)))
	assert.Equal(t, "c0", hex.EncodeToString(rlpEncode(nil, true)))
}

func TestSelectors(t *testing.T) {
	assert.Equal(t, "a9059cbb", hex.EncodeToString(erc20TransferSelector[:]))
	assert.Equal(t, "70a08231", hex.EncodeToString(erc20BalanceOfSelector[:]))
}

func TestParseFormatUSDC_RoundTrip(t *testing.T) {
	v, err := ParseUSDC("1.50")
	require.NoError(t, err)
	assert.Equal(t, "1500000", v.String())
	assert.Equal(t, "1.500000", FormatUSDC(v))

	for _, bad := range []string{"", "abc", "-1", "0.0000001", "1.2.3"} {
		_, err := ParseUSDC(bad)
		assert.Error(t, err, bad)
	}
}

// Regression: the ERC-20 interface uses 6 decimals while native gas uses 18.
// Parsing $1.50 as 1.5e18 would move a million dollars per dollar intended.
func TestParseUSDC_SixNotEighteen(t *testing.T) {
	v, err := ParseUSDC("1")
	require.NoError(t, err)
	assert.Equal(t, "1000000", v.String())
	assert.NotEqual(t, "1000000000000000000", v.String())
}

func TestFloorToERC20(t *testing.T) {
	got, err := FloorToERC20("100.0000000") // 7dp Money string, zero dust
	require.NoError(t, err)
	assert.Equal(t, "100.000000", got)

	got, err = FloorToERC20("10.123456789") // dust truncated toward zero
	require.NoError(t, err)
	assert.Equal(t, "10.123456", got)

	v, err := ParseUSDC(got) // floored output always parses exactly
	require.NoError(t, err)
	assert.Equal(t, "10123456", v.String())

	for _, bad := range []string{"", "0", "-5", "abc"} {
		_, err := FloorToERC20(bad)
		assert.Error(t, err, bad)
	}
}

func TestAddressFromPrivKey_Deterministic(t *testing.T) {
	raw, err := hex.DecodeString(testPrivHex)
	require.NoError(t, err)
	a1, err := addressFromPrivKey(raw)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(a1, "0x"))
	assert.Len(t, a1, 42)
	a2, err := addressFromPrivKey(raw)
	require.NoError(t, err)
	assert.Equal(t, a1, a2)

	_, err = addressFromPrivKey([]byte{1, 2, 3})
	assert.Error(t, err)
}

func TestSign_RecoverRoundTrip(t *testing.T) {
	raw, err := hex.DecodeString(testPrivHex)
	require.NoError(t, err)
	self, err := addressFromPrivKey(raw)
	require.NoError(t, err)
	to, err := MustHexToAddress(self)
	require.NoError(t, err)
	amount, err := ParseUSDC("10")
	require.NoError(t, err)

	tx := &EIP1559Tx{
		ChainID:              big.NewInt(int64(ChainIDTestnet)),
		Nonce:                7,
		MaxPriorityFeePerGas: big.NewInt(1_000_000_000),
		MaxFeePerGas:         big.NewInt(20_000_000_000),
		GasLimit:             65000,
		To:                   to,
		Value:                big.NewInt(0),
		Data:                 encodeTransfer(to, amount),
	}
	signed, hash, err := tx.Sign(raw)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "0x"))
	assert.Equal(t, byte(0x02), signed[0])

	// Independent check: (r,s) must verify under the derived pubkey.
	sender, err := RecoverSender(signed)
	require.NoError(t, err)
	assert.Equal(t, self, sender)

	// Encoder/decoder symmetry: 12 fields, nonce and chain round-trip.
	fields, err := rlpDecodeList(signed[1:])
	require.NoError(t, err)
	require.Len(t, fields, 12)
	assert.Equal(t, uint64(7), new(big.Int).SetBytes(fields[1]).Uint64())
	assert.Equal(t, uint64(ChainIDTestnet), new(big.Int).SetBytes(fields[0]).Uint64())
}

func TestSign_RejectsBadKey(t *testing.T) {
	tx := &EIP1559Tx{ChainID: big.NewInt(1)}
	_, _, err := tx.Sign([]byte{1, 2, 3})
	assert.Error(t, err)
}

func TestMustHexToAddress(t *testing.T) {
	_, err := MustHexToAddress("0x742d35Cc6634C0532925a3b844Bc454e4438f44e")
	require.NoError(t, err)
	for _, bad := range []string{"", "f39f", "0x123", "1234567890123456789012345678901234567890"} {
		_, err := MustHexToAddress(bad)
		assert.Error(t, err, bad)
	}
}

func transferFixture(t *testing.T, token, to string, baseUnits uint64) Log {
	t.Helper()
	pad := func(addr string) string {
		h := strings.TrimPrefix(strings.ToLower(addr), "0x")
		return "0x" + strings.Repeat("0", 64-len(h)) + h
	}
	return Log{
		Address: token,
		Topics: []string{
			TransferEventSig,
			pad("0x1111111111111111111111111111111111111111"),
			pad(to),
		},
		Data: fmt.Sprintf("0x%064x", baseUnits),
	}
}

func TestMaxTransferValue(t *testing.T) {
	token := "0x3600000000000000000000000000000000000000"
	to := "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"

	assert.Nil(t, MaxTransferValue(nil, token), "nil logs match nothing")
	assert.Nil(t, MaxTransferValue([]Log{}, token), "empty logs match nothing")
	assert.Nil(t, MaxTransferValue([]Log{transferFixture(t, token, to, 100)}, ""), "empty token matches nothing")

	logs := []Log{
		transferFixture(t, token, to, 100),
		transferFixture(t, token, to, 500),
		transferFixture(t, "0x0000000000000000000000000000000000000001", to, 999999),
	}
	assert.Equal(t, "500", MaxTransferValue(logs, token).String(), "picks max from the token contract only")

	// Malformed entries are skipped, never fatal.
	bad := transferFixture(t, token, to, 700)
	bad.Data = "0xzzzz"
	assert.Equal(t, "500", MaxTransferValue(append(logs, bad), token).String())
	short := transferFixture(t, token, to, 800)
	short.Topics = short.Topics[:2]
	assert.Equal(t, "500", MaxTransferValue(append(logs, short), token).String())
	assert.Nil(t, MaxTransferValue([]Log{bad, short}, token))
}

func TestUSDCFloatToBaseUnits(t *testing.T) {
	assert.Equal(t, "100000000", USDCFloatToBaseUnits(100.0).String())
	assert.Equal(t, "100000", USDCFloatToBaseUnits(0.1).String())
	assert.Equal(t, "1", USDCFloatToBaseUnits(0.000001).String())
	assert.Equal(t, "0", USDCFloatToBaseUnits(0).String())
	assert.Equal(t, "0", USDCFloatToBaseUnits(-5).String())
}
