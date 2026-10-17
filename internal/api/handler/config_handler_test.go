package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func servePublicConfig(t *testing.T, cfg PublicConfig) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	NewConfigHandler(cfg).PublicConfig(c)

	var env struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	return w.Code, env.Data
}

// Phase 2.3: empty GOOGLE_CLIENT_ID must surface as disabled (fail-closed
// backend) so the UI can hide the button instead of serving a 503 on click.
func TestConfigHandler_GoogleDisabledWhenNoClientID(t *testing.T) {
	code, data := servePublicConfig(t, PublicConfig{})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, data["google_enabled"])
}

func TestConfigHandler_GoogleEnabledWhenConfigured(t *testing.T) {
	code, data := servePublicConfig(t, PublicConfig{GoogleClientID: "abc.apps.googleusercontent.com"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, data["google_enabled"])
}

func TestConfigHandler_ArcAndFiatPassthrough(t *testing.T) {
	code, data := servePublicConfig(t, PublicConfig{
		ArcChainID:         5042002,
		ArcCircleAddress:   "0x70A7CCB7b5fe856384Aab3aAF553E09A10AF14A4",
		ArcTreasuryAddress: "0x2222222222222222222222222222222222222222",
	})
	require.Equal(t, http.StatusOK, code)
	arc := data["arc"].(map[string]any)
	assert.Equal(t, float64(5042002), arc["chain_id"])
	assert.Equal(t, "0x70A7CCB7b5fe856384Aab3aAF553E09A10AF14A4", arc["circle_address"])
	assert.Equal(t, false, arc["usdc_configured"])
	fiat := data["fiat"].(map[string]any)
	assert.Equal(t, "mock", fiat["provider"])
}

func TestConfigHandler_EmailConfiguredFlag(t *testing.T) {
	code, data := servePublicConfig(t, PublicConfig{EmailConfigured: true})
	require.Equal(t, http.StatusOK, code)
	email := data["email"].(map[string]any)
	assert.Equal(t, true, email["configured"])

	code, data = servePublicConfig(t, PublicConfig{})
	require.Equal(t, http.StatusOK, code)
	email = data["email"].(map[string]any)
	assert.Equal(t, false, email["configured"])
}
