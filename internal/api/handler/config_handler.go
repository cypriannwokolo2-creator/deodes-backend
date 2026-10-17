package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/deodes/backend/pkg/response"
)

// PublicConfig carries the non-secret client configuration the frontend
// needs BEFORE login (Phase 2.3). Nothing here is secret: addresses and
// chain IDs are public on-chain data, and the flags only describe which
// buttons to render. Secrets (keys, peppers, DSNs) must never enter this
// struct — adding a field here is a security review.
type PublicConfig struct {
	GoogleClientID     string
	ArcChainID         uint64
	ArcCircleAddress   string
	ArcTreasuryAddress string
	ArcUSDCContract    string
	// FiatProvider names the active fiat rail. Only "mock" exists until
	// Phase 2.18 lands the pluggable provider switch.
	FiatProvider string
	// EmailConfigured reports whether a Brevo API key is set, so the UI can
	// banner email-delivery outages instead of stranding users on OTP
	// screens. Boolean only — never a key, address, or provider detail.
	EmailConfigured bool
}

// ConfigHandler serves GET /v1/config: public, pre-auth feature flags so
// the UI never offers a flow the backend has disabled (e.g. Google sign-in
// without GOOGLE_CLIENT_ID, which would otherwise 503 on click).
type ConfigHandler struct {
	cfg PublicConfig
}

func NewConfigHandler(cfg PublicConfig) *ConfigHandler {
	if cfg.FiatProvider == "" {
		cfg.FiatProvider = "mock"
	}
	return &ConfigHandler{cfg: cfg}
}

// @Summary Public client configuration
// @Description Non-secret feature flags and chain addresses for pre-login UI gating. No authentication required.
// @Tags Config
// @Produce json
// @Success 200 {object} response.Envelope{data=object}
// @Router /config [get]
func (h *ConfigHandler) PublicConfig(c *gin.Context) {
	response.OK(c, gin.H{
		"google_enabled": strings.TrimSpace(h.cfg.GoogleClientID) != "",
		"arc": gin.H{
			"chain_id":         h.cfg.ArcChainID,
			"circle_address":   h.cfg.ArcCircleAddress,
			"treasury_address": h.cfg.ArcTreasuryAddress,
			"usdc_configured":  strings.TrimSpace(h.cfg.ArcUSDCContract) != "",
		},
		"fiat": gin.H{
			"provider": h.cfg.FiatProvider,
		},
		"email": gin.H{
			"configured": h.cfg.EmailConfigured,
		},
	})
}
