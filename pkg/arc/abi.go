package arc

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"golang.org/x/crypto/sha3"
)

// keccak256 returns the legacy Keccak-256 hash (Ethereum flavor, NOT NIST
// SHA3) of data.
func keccak256(data ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, b := range data {
		_, _ = h.Write(b)
	}
	return h.Sum(nil)
}

// Keccak256 is the exported Keccak-256 (Ethereum flavor) for consumers that
// compute event topic0 hashes (2.6 indexer). Same construction as keccak256.
func Keccak256(data ...[]byte) []byte {
	return keccak256(data...)
}

// ParseUSDC converts a human decimal amount ("1.50") into ERC-20 base units
// (1.50 -> 1500000). It rejects malformed input, negatives, and precision
// beyond 6 decimals with an error — never panics, never floats.
//
// Decimal warning: this targets the ERC-20 interface (6 decimals, see
// ERC20Decimals), NOT the native gas token (18 decimals). Mixing them moves
// 1e12x the intended value — the regression tests pin this.
func ParseUSDC(s string) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty amount")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("invalid amount %q", s)
	}
	if r.Sign() < 0 {
		return nil, fmt.Errorf("negative amount %q", s)
	}
	mult := new(big.Int).Exp(big.NewInt(10), big.NewInt(ERC20Decimals), nil)
	r.Mul(r, new(big.Rat).SetInt(mult))
	if !r.IsInt() {
		return nil, fmt.Errorf("amount %q exceeds %d decimals", s, ERC20Decimals)
	}
	return r.Num(), nil
}

// FloorToERC20 truncates a human decimal amount toward zero to 6 decimals,
// returning the canonical string actually sent on-chain. Dust below $0.000001
// stays in the sender's wallet; callers must record/audit the RETURNED value,
// never the input. Use it at every float/7-decimal boundary (Money is 7dp).
func FloorToERC20(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty amount")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "", fmt.Errorf("invalid amount %q", s)
	}
	if r.Sign() <= 0 {
		return "", fmt.Errorf("amount must be positive")
	}
	mult := new(big.Int).Exp(big.NewInt(10), big.NewInt(ERC20Decimals), nil)
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(mult))
	floored := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	return FormatUSDC(floored), nil
}

// USDCFloatToBaseUnits converts a human USDC float to 6-decimal base units,
// flooring dust. Exact decimal math (no binary-float drift): the shortest
// decimal rendering parses exactly. For coverage thresholds — never for
// settlement amounts, which stay exact via ParseUSDC/FloorToERC20.
func USDCFloatToBaseUnits(f float64) *big.Int {
	if f <= 0 || f != f || f > 1e15 {
		return big.NewInt(0)
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'f', -1, 64))
	if !ok {
		return big.NewInt(0)
	}
	mult := new(big.Int).Exp(big.NewInt(10), big.NewInt(ERC20Decimals), nil)
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(mult))
	return new(big.Int).Quo(scaled.Num(), scaled.Denom())
}

// FormatUSDC converts ERC-20 base units back to a human decimal string.
func FormatUSDC(wei *big.Int) string {
	if wei == nil {
		return "0"
	}
	mult := new(big.Int).Exp(big.NewInt(10), big.NewInt(ERC20Decimals), nil)
	r := new(big.Rat).SetFrac(wei, mult)
	return r.FloatString(ERC20Decimals)
}

// MustHexToAddress validates a 0x address and returns its lowercase bytes.
func MustHexToAddress(s string) ([20]byte, error) {
	var out [20]byte
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return out, fmt.Errorf("address %q missing 0x prefix", s)
	}
	raw, err := hex.DecodeString(s[2:])
	if err != nil || len(raw) != 20 {
		return out, fmt.Errorf("invalid address %q", s)
	}
	copy(out[:], raw)
	return out, nil
}
