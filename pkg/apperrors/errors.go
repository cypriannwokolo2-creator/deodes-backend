package apperrors

import "errors"

var (
	ErrNotFound                 = errors.New("resource not found")
	ErrUnauthorized             = errors.New("unauthorized")
	ErrForbidden                = errors.New("forbidden")
	ErrInvalidInput             = errors.New("invalid input")
	ErrConflict                 = errors.New("resource already exists")
	ErrInternal                 = errors.New("internal server error")
	ErrWalletNotVerified        = errors.New("wallet signature verification failed")
	ErrTokenExpired             = errors.New("token expired")
	ErrNonceExpired             = errors.New("nonce expired")
	ErrCircleFull               = errors.New("circle is full")
	ErrCircleNotActive          = errors.New("circle is not active")
	ErrAlreadyMember            = errors.New("already a member of this circle")
	ErrNotOrganizer             = errors.New("only the organizer can perform this action")
	ErrMoiScoreTooLow           = errors.New("MoiScore is too low for this circle")
	ErrInvalidInvite            = errors.New("invalid or expired invite code")
	ErrMaxStrikes               = errors.New("maximum strikes reached")
	ErrDuplicateFile            = errors.New("file with this name already exists")
	ErrLateContributionRejected = errors.New("contributions rejected: payout scheduling has already begun for this round")
	ErrDuplicateContribution    = errors.New("contribution for this round already recorded")
	ErrCircleEnded              = errors.New("contributions rejected: circle has ended")
	// ErrCircleSuspended marks an action on a paused or banned circle (admin
	// moderation). Handlers map it to 422 like ErrCircleEnded, with a
	// distinct code so clients can tell "paused" from "ended".
	ErrCircleSuspended = errors.New("circle is paused or banned by moderation")
	// ErrCircleContractMismatch marks a record attempt for a circle bound to
	// a different on-chain contract than the configured circle (2.5).
	// Handlers map it to 422 (wrong rail/circle).
	ErrCircleContractMismatch = errors.New("circle contract mismatch: circle is bound to a different contract than configured")
	// ErrFeeMismatch marks a payout whose client-supplied fee differs from
	// the server-computed 50bps split (2.8). The fee is protocol fixed, so
	// a mismatch is a client bug, not a negotiation. Handlers map it to 422.
	ErrFeeMismatch           = errors.New("fee mismatch: payout fee must equal the protocol 50bps split")
	ErrRequestEntityTooLarge = errors.New("request body exceeds the maximum allowed size for this endpoint")
	// ErrChainDisabled marks on-chain escrow operations attempted while no
	// chain escrow client exists (purged rail). Handlers map it to 503.
	ErrChainDisabled = errors.New("on-chain escrow unavailable")
)

type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string { return "validation failed" }
