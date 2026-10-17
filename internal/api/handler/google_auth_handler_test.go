package handler_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/auth"
	authgoogle "github.com/deodes/backend/internal/domain/auth/google"
	"github.com/deodes/backend/internal/domain/user"
	"github.com/deodes/backend/internal/domain/wallet"
	"github.com/deodes/backend/pkg/apperrors"
)

type stubGoogleAuthSvc struct {
	auth.Service
	pair *auth.TokenPair
}

func (s *stubGoogleAuthSvc) CreateSession(ctx context.Context, userID uuid.UUID, role string, ttl time.Duration, device string) (*auth.TokenPair, error) {
	return s.pair, nil
}

type stubGoogleUserSvc struct {
	user.Service
	byWallet map[string]*user.User
	created  int
}

func (s *stubGoogleUserSvc) GetByWallet(ctx context.Context, w string) (*user.User, error) {
	if u, ok := s.byWallet[w]; ok {
		return u, nil
	}
	return nil, errGoogleNotFound
}

type notFoundErr struct{}

func (notFoundErr) Error() string { return "not found" }

var errGoogleNotFound error = notFoundErr{}

func (s *stubGoogleUserSvc) Create(ctx context.Context, w string) (*user.User, error) {
	u := &user.User{ID: uuid.New(), WalletAddress: w, Role: user.RoleUser}
	s.byWallet[w] = u
	s.created++
	return u, nil
}

func (s *stubGoogleUserSvc) Update(ctx context.Context, u *user.User) error { return nil }

type stubGoogleUserRepo struct {
	user.Repository
	stored map[string]*user.User
}

func (s *stubGoogleUserRepo) Create(ctx context.Context, u *user.User) error {
	if _, ok := s.stored[u.WalletAddress]; ok {
		return apperrors.ErrConflict
	}
	s.stored[u.WalletAddress] = u
	return nil
}

func (s *stubGoogleUserRepo) FindByWalletAddress(ctx context.Context, w string) (*user.User, error) {
	if u, ok := s.stored[w]; ok {
		return u, nil
	}
	return nil, errGoogleNotFound
}

func (s *stubGoogleUserRepo) FindByUsername(_ context.Context, _ string) (*user.User, error) {
	return nil, apperrors.ErrNotFound
}

type stubGoogleWallet struct {
	wallet.Service
}

func (s *stubGoogleWallet) DeriveWalletSeed(ctx context.Context, email string) (string, error) {
	return "seed", nil
}
func (s *stubGoogleWallet) CreateWallet(ctx context.Context, userID string, seed []byte) (*wallet.Wallet, error) {
	return &wallet.Wallet{}, nil
}

// googleTestKey mints Google-shaped ID tokens against a local JWKS.
type googleTestKey struct {
	t   *testing.T
	key *rsa.PrivateKey
	srv *httptest.Server
}

func newGoogleTestKey(t *testing.T) *googleTestKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[{"kid":"k1","n":"` + n + `","e":"` + e + `"}` + `]}`))
	}))
	t.Cleanup(srv.Close)
	return &googleTestKey{t: t, key: key, srv: srv}
}

func (g *googleTestKey) idToken(email string) string {
	claims := jwt.MapClaims{
		"iss": "https://accounts.google.com", "aud": "web-client-id",
		"sub": "g-sub-1", "email": email, "email_verified": true,
		"name": "G User", "exp": time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "k1"
	signed, err := token.SignedString(g.key)
	require.NoError(g.t, err)
	return signed
}

func (g *googleTestKey) verifier() *authgoogle.Verifier {
	v := authgoogle.NewVerifier("web-client-id")
	v.SetCertsURL(g.srv.URL)
	return v
}

func setupGoogleRouter(h *handler.GoogleAuthHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/auth/google", h.Google)
	return r
}

func postGoogle(t *testing.T, r *gin.Engine, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", "/v1/auth/google", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var decoded map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &decoded)
	return w.Code, decoded
}

func TestGoogleAuth_ProvisionsAndIssuesSession(t *testing.T) {
	g := newGoogleTestKey(t)
	userSvc := &stubGoogleUserSvc{byWallet: map[string]*user.User{}}
	stored := map[string]*user.User{}
	h := handler.NewGoogleAuthHandler(
		&stubGoogleAuthSvc{pair: &auth.TokenPair{AccessToken: "a", RefreshToken: "r", CSRFToken: "c"}},
		userSvc,
		&stubGoogleUserRepo{stored: stored},
		&stubGoogleWallet{}, "web-client-id",
	).WithVerifier(g.verifier())

	code, resp := postGoogle(t, setupGoogleRouter(h), map[string]any{"idToken": g.idToken("new@example.com")})
	require.Equal(t, http.StatusOK, code, resp)
	data := resp["data"].(map[string]any)
	assert.Equal(t, "a", data["token"])
	u, ok := stored["GOOGLE:g-sub-1"]
	require.True(t, ok, "provisioned user must be stored")
	assert.True(t, u.EmailVerified)
	assert.NotNil(t, u.Email)
	assert.Equal(t, 0, userSvc.created, "service Create must not run on the repo path")
}

func TestGoogleAuth_ExistingUserNoDuplicate(t *testing.T) {
	g := newGoogleTestKey(t)
	existing := &user.User{ID: uuid.New(), WalletAddress: "GOOGLE:g-sub-1", Role: user.RoleUser}
	userSvc := &stubGoogleUserSvc{byWallet: map[string]*user.User{"GOOGLE:g-sub-1": existing}}
	h := handler.NewGoogleAuthHandler(
		&stubGoogleAuthSvc{pair: &auth.TokenPair{AccessToken: "a", RefreshToken: "r"}},
		userSvc,
		&stubGoogleUserRepo{stored: map[string]*user.User{}},
		&stubGoogleWallet{}, "web-client-id",
	).WithVerifier(g.verifier())

	code, _ := postGoogle(t, setupGoogleRouter(h), map[string]any{"idToken": g.idToken("x@example.com")})
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, 0, userSvc.created)
}

func TestGoogleAuth_RejectsBadToken(t *testing.T) {
	g := newGoogleTestHandler(t)
	code, _ := postGoogle(t, setupGoogleRouter(g), map[string]any{"idToken": "garbage"})
	assert.Equal(t, http.StatusUnauthorized, code)
}

func newGoogleTestHandler(t *testing.T) *handler.GoogleAuthHandler {
	t.Helper()
	g := newGoogleTestKey(t)
	return handler.NewGoogleAuthHandler(
		&stubGoogleAuthSvc{}, &stubGoogleUserSvc{byWallet: map[string]*user.User{}},
		&stubGoogleUserRepo{stored: map[string]*user.User{}},
		&stubGoogleWallet{}, "web-client-id",
	).WithVerifier(g.verifier())
}
