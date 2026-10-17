package bankaccount

import (
	"time"

	"github.com/google/uuid"
)

// BankAccount is a saved fiat payout rail for one user.
type BankAccount struct {
	ID            uuid.UUID `json:"id" db:"id"`
	UserID        uuid.UUID `json:"-" db:"user_id"`
	BankCode      string    `json:"bankCode" db:"bank_code"`
	BankName      string    `json:"bankName" db:"bank_name"`
	AccountNumber string    `json:"accountNumber" db:"account_number"`
	AccountName   string    `json:"accountName" db:"account_name"`
	IsDefault     bool      `json:"isDefault" db:"is_default"`
	CreatedAt     time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt     time.Time `json:"updatedAt" db:"updated_at"`
}

// CreateAccountRequest mirrors the settings-screen add form.
type CreateAccountRequest struct {
	BankCode      string `json:"bankCode" binding:"required"`
	BankName      string `json:"bankName"`
	AccountNumber string `json:"accountNumber" binding:"required"`
	AccountName   string `json:"accountName" binding:"required"`
}
