package wallet

import (
	"fmt"

	"github.com/deodes/backend/pkg/crypto"
)

type WalletType string

const (
	WalletTypeAuto      WalletType = "auto"
	WalletTypeFreighter WalletType = "freighter"
	WalletTypePasskey   WalletType = "passkey"
	// WalletTypeHosted is a custodial per-user wallet under us (Arc 0x first):
	// the backend derives, encrypts, stores and signs with the secret.
	// UNIQUE(user_id, wallet_type) allows one hosted wallet per user.
	WalletTypeHosted WalletType = "hosted"
	// WalletTypeLinked is a self-custody Arc 0x wallet the user proved
	// ownership of via a signed nonce (POST /v1/wallets/link). No secret is
	// ever stored (EncryptedSecretKey/Nonce stay empty) and it is never
	// primary. UNIQUE(user_id, wallet_type) allows one linked wallet per
	// user; the address itself is globally unique via UNIQUE(public_key).
	WalletTypeLinked WalletType = "linked"
)

// Balance is the available USDC balance (Arc EVM) for a wallet.
type Balance struct {
	USDC float64 `json:"usdc"`
}

type Wallet struct {
	ID                 string     `json:"id" db:"id"`
	UserID             string     `json:"userId" db:"user_id"`
	PublicKey          string     `json:"publicKey" db:"public_key"`
	EncryptedSecretKey []byte     `json:"-" db:"encrypted_secret_key"`
	EncryptionNonce    []byte     `json:"-" db:"encryption_nonce"`
	WalletType         WalletType `json:"walletType" db:"wallet_type"`
	IsPrimary          bool       `json:"isPrimary" db:"is_primary"`
	// Frozen is admin moderation state: a frozen wallet cannot send (all
	// outbound flows reject), while inbound deposits are unaffected.
	Frozen       bool   `json:"isFrozen" db:"frozen"`
	FrozenReason string `json:"frozenReason,omitempty" db:"frozen_reason"`
	CreatedAt    string `json:"createdAt" db:"created_at"`
	UpdatedAt    string `json:"updatedAt" db:"updated_at"`
}

// DecryptSecret decrypts the wallet's AES-256-GCM encrypted secret key using
// the provided encryption keys (primary configured key, rotated keys, or a
// legacy passkey seed). It delegates to pkg/crypto so the wallet domain stays
// free of cryptographic primitives and returns the first successful decryption.
func (w *Wallet) DecryptSecret(keys ...[]byte) (string, error) {
	if len(w.EncryptedSecretKey) == 0 || len(w.EncryptionNonce) == 0 {
		return "", fmt.Errorf("wallet has no encrypted secret key")
	}
	plaintext, err := crypto.Decrypt(w.EncryptedSecretKey, w.EncryptionNonce, keys...)
	if err != nil {
		return "", fmt.Errorf("decrypting secret key: %w", err)
	}
	return string(plaintext), nil
}
