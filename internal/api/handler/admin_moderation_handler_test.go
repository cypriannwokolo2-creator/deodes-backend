package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/circle"
	circleMocks "github.com/deodes/backend/internal/domain/circle/mocks"
	"github.com/deodes/backend/internal/domain/community"
	communityMocks "github.com/deodes/backend/internal/domain/community/mocks"
	"github.com/deodes/backend/internal/domain/user"
	userMocks "github.com/deodes/backend/internal/domain/user/mocks"
	"github.com/deodes/backend/internal/domain/wallet"
	"github.com/deodes/backend/pkg/apperrors"
)

// fakeModWalletRepo embeds wallet.Repository so only freeze methods need
// overrides; any other call nil-panics loudly.
type fakeModWalletRepo struct {
	wallet.Repository
	frozen map[string]*wallet.Wallet
	// hasWallet marks users known to own a hosted wallet; others 404.
	hasWallet map[string]bool
}

func (f *fakeModWalletRepo) FreezeByUserID(_ context.Context, userID uuid.UUID, frozen bool, reason string) (*wallet.Wallet, error) {
	if f.frozen == nil {
		f.frozen = map[string]*wallet.Wallet{}
	}
	if existing, ok := f.frozen[userID.String()]; ok {
		if !frozen {
			existing.Frozen = false
			existing.FrozenReason = ""
		}
		return existing, nil
	}
	if !frozen || !f.hasWallet[userID.String()] {
		return nil, nil
	}
	w := &wallet.Wallet{
		ID: uuid.New().String(), UserID: userID.String(),
		PublicKey:  "0x81e877e2d21ec187994ed4ae1fafb36d529bacb1",
		WalletType: wallet.WalletTypeHosted, Frozen: true, FrozenReason: reason,
	}
	f.frozen[userID.String()] = w
	return w, nil
}

func (f *fakeModWalletRepo) ListFrozen(_ context.Context) ([]wallet.Wallet, error) {
	var out []wallet.Wallet
	for _, w := range f.frozen {
		if w.Frozen {
			out = append(out, *w)
		}
	}
	if out == nil {
		out = []wallet.Wallet{}
	}
	return out, nil
}

func (f *fakeModWalletRepo) FindByUserID(_ context.Context, userID string) ([]wallet.Wallet, error) {
	var out []wallet.Wallet
	for _, w := range f.frozen {
		if w.UserID == userID {
			out = append(out, *w)
		}
	}
	if out == nil {
		out = []wallet.Wallet{}
	}
	return out, nil
}

func (f *fakeModWalletRepo) ListWithdrawalsByUser(_ context.Context, _ uuid.UUID, _ int) ([]wallet.WithdrawalRecord, error) {
	return []wallet.WithdrawalRecord{}, nil
}

func setupModRouter(
	t *testing.T,
	circleRepo *circleMocks.Repository,
	communityRepo *communityMocks.Repository,
	walletRepo *fakeModWalletRepo,
) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	circleSvc := circle.NewService(circleRepo, nil, circle.Dependencies{})
	communitySvc := community.NewService(communityRepo, nil)
	h := handler.NewAdminHandler(nil, nil, circleSvc, nil, nil, nil, nil).
		WithCommunityService(communitySvc).
		WithWalletRepository(walletRepo)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "00000000-0000-0000-0000-000000000001")
		c.Set("role", "admin")
		c.Next()
	})
	r.POST("/admin/circles/:id/pause", h.PauseCircle)
	r.POST("/admin/circles/:id/resume", h.ResumeCircle)
	r.POST("/admin/circles/:id/ban", h.BanCircle)
	r.GET("/admin/communities", h.ListCommunities)
	r.POST("/admin/communities/:id/suspend", h.SuspendCommunity)
	r.POST("/admin/communities/:id/unsuspend", h.UnsuspendCommunity)
	r.POST("/admin/communities/:id/ban", h.BanCommunity)
	r.POST("/admin/users/:id/freeze", h.FreezeUserFunds)
	r.POST("/admin/users/:id/unfreeze", h.UnfreezeUserFunds)
	r.GET("/admin/frozen-wallets", h.ListFrozenWallets)
	return r
}

func callMod(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		raw, _ := json.Marshal(body)
		buf = bytes.NewBuffer(raw)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestAdminHandler_PauseResumeBanCircle(t *testing.T) {
	newCircle := func(status circle.CircleStatus) *circle.Circle {
		return &circle.Circle{ID: uuid.New(), Status: status}
	}

	t.Run("pauses an active circle", func(t *testing.T) {
		crepo := new(circleMocks.Repository)
		id := uuid.New()
		crepo.On("FindByID", mock.Anything, id).Return(newCircle(circle.CircleStatusActive), nil)
		crepo.On("Update", mock.Anything, mock.MatchedBy(func(c *circle.Circle) bool {
			return c.Status == circle.CircleStatusPaused
		})).Return(nil)

		w := callMod(t, setupModRouter(t, crepo, new(communityMocks.Repository), &fakeModWalletRepo{}),
			"POST", "/admin/circles/"+id.String()+"/pause", map[string]any{"reason": "spam"})
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "paused")
		crepo.AssertExpectations(t)
	})

	t.Run("cannot pause a completed circle", func(t *testing.T) {
		crepo := new(circleMocks.Repository)
		id := uuid.New()
		crepo.On("FindByID", mock.Anything, id).Return(newCircle(circle.CircleStatusCompleted), nil)

		w := callMod(t, setupModRouter(t, crepo, new(communityMocks.Repository), &fakeModWalletRepo{}),
			"POST", "/admin/circles/"+id.String()+"/pause", nil)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("resumes a paused circle", func(t *testing.T) {
		crepo := new(circleMocks.Repository)
		id := uuid.New()
		crepo.On("FindByID", mock.Anything, id).Return(newCircle(circle.CircleStatusPaused), nil)
		crepo.On("Update", mock.Anything, mock.MatchedBy(func(c *circle.Circle) bool {
			return c.Status == circle.CircleStatusActive
		})).Return(nil)

		w := callMod(t, setupModRouter(t, crepo, new(communityMocks.Repository), &fakeModWalletRepo{}),
			"POST", "/admin/circles/"+id.String()+"/resume", nil)
		assert.Equal(t, http.StatusOK, w.Code)
		crepo.AssertExpectations(t)
	})

	t.Run("bans an active circle", func(t *testing.T) {
		crepo := new(circleMocks.Repository)
		id := uuid.New()
		crepo.On("FindByID", mock.Anything, id).Return(newCircle(circle.CircleStatusActive), nil)
		crepo.On("Update", mock.Anything, mock.MatchedBy(func(c *circle.Circle) bool {
			return c.Status == circle.CircleStatusBanned
		})).Return(nil)

		w := callMod(t, setupModRouter(t, crepo, new(communityMocks.Repository), &fakeModWalletRepo{}),
			"POST", "/admin/circles/"+id.String()+"/ban", map[string]any{"reason": "fraud"})
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "banned")
		crepo.AssertExpectations(t)
	})

	t.Run("invalid circle id is 400", func(t *testing.T) {
		w := callMod(t, setupModRouter(t, new(circleMocks.Repository), new(communityMocks.Repository), &fakeModWalletRepo{}),
			"POST", "/admin/circles/not-a-uuid/pause", nil)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestAdminHandler_CommunityModeration(t *testing.T) {
	newCommunity := func() *community.Community {
		return &community.Community{ID: uuid.New(), Name: "C", Slug: "c", OwnerID: uuid.New()}
	}

	t.Run("lists communities including suspended", func(t *testing.T) {
		mrepo := new(communityMocks.Repository)
		mrepo.On("List", mock.Anything, mock.MatchedBy(func(f community.CommunityFilter) bool {
			return f.IncludeSuspended
		})).Return([]community.Community{*newCommunity()}, 1, nil)

		w := callMod(t, setupModRouter(t, new(circleMocks.Repository), mrepo, &fakeModWalletRepo{}),
			"GET", "/admin/communities?page=1&limit=20", nil)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"communities"`)
		mrepo.AssertExpectations(t)
	})

	t.Run("suspends and unsuspends", func(t *testing.T) {
		mrepo := new(communityMocks.Repository)
		id := uuid.New()
		mrepo.On("FindByID", mock.Anything, id).Return(newCommunity(), nil).Twice()
		mrepo.On("Suspend", mock.Anything, id, "spam").Return(nil)
		mrepo.On("Unsuspend", mock.Anything, id).Return(nil)
		r := setupModRouter(t, new(circleMocks.Repository), mrepo, &fakeModWalletRepo{})

		w := callMod(t, r, "POST", "/admin/communities/"+id.String()+"/suspend", map[string]any{"reason": "spam"})
		assert.Equal(t, http.StatusOK, w.Code)
		w = callMod(t, r, "POST", "/admin/communities/"+id.String()+"/unsuspend", nil)
		assert.Equal(t, http.StatusOK, w.Code)
		mrepo.AssertExpectations(t)
	})

	t.Run("bans a community", func(t *testing.T) {
		mrepo := new(communityMocks.Repository)
		id := uuid.New()
		mrepo.On("FindByID", mock.Anything, id).Return(newCommunity(), nil)
		mrepo.On("Delete", mock.Anything, id).Return(nil)

		w := callMod(t, setupModRouter(t, new(circleMocks.Repository), mrepo, &fakeModWalletRepo{}),
			"POST", "/admin/communities/"+id.String()+"/ban", map[string]any{"reason": "fraud"})
		assert.Equal(t, http.StatusOK, w.Code)
		mrepo.AssertExpectations(t)
	})
}

func TestAdminHandler_FundFreeze(t *testing.T) {	userID := "11111111-1111-1111-1111-111111111111"

	t.Run("freezes and unfreezes a user wallet", func(t *testing.T) {
		wrepo := &fakeModWalletRepo{hasWallet: map[string]bool{userID: true}}
		r := setupModRouter(t, new(circleMocks.Repository), new(communityMocks.Repository), wrepo)

		w := callMod(t, r, "POST", "/admin/users/"+userID+"/freeze", map[string]any{"reason": "suspicious"})
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"frozen":true`)

		w = callMod(t, r, "GET", "/admin/frozen-wallets", nil)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "suspicious")

		w = callMod(t, r, "POST", "/admin/users/"+userID+"/unfreeze", nil)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"frozen":false`)

		w = callMod(t, r, "GET", "/admin/frozen-wallets", nil)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotContains(t, w.Body.String(), "suspicious")
	})

	t.Run("freezing a user without hosted wallet is 404", func(t *testing.T) {
		w := callMod(t, setupModRouter(t, new(circleMocks.Repository), new(communityMocks.Repository), &fakeModWalletRepo{}),
			"POST", "/admin/users/"+userID+"/freeze", nil)
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("invalid user id is 400", func(t *testing.T) {
		w := callMod(t, setupModRouter(t, new(circleMocks.Repository), new(communityMocks.Repository), &fakeModWalletRepo{}),
			"POST", "/admin/users/not-a-uuid/freeze", nil)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestAdminHandler_GetUserDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	uid := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	newRouter := func(urepo *userMocks.Repository, wrepo *fakeModWalletRepo) *gin.Engine {
		circleRepo := new(circleMocks.Repository)
		circleRepo.On("FindCirclesByUserID", mock.Anything, mock.Anything).Return([]circle.Circle{
			{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Name: "Savers", Status: circle.CircleStatusActive},
		}, nil)
		circleSvc := circle.NewService(circleRepo, nil, circle.Dependencies{})
		communityRepo := new(communityMocks.Repository)
		communityRepo.On("FindByUserID", mock.Anything, mock.Anything).Return([]community.Community{
			{ID: uuid.MustParse("33333333-3333-3333-3333-333333333333"), Name: "Lagos", Slug: "lagos"},
		}, nil)
		communitySvc := community.NewService(communityRepo, nil)
		h := handler.NewAdminHandler(nil, urepo, circleSvc, nil, nil, nil, nil).
			WithCommunityService(communitySvc).
			WithWalletRepository(wrepo)
		r := gin.New()
		r.Use(func(c *gin.Context) { c.Set("userID", "00000000-0000-0000-0000-000000000001"); c.Next() })
		r.GET("/admin/users/:id", h.GetUserDetail)
		return r
	}
	get := func(r *gin.Engine, id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/admin/users/"+id, nil)
		r.ServeHTTP(w, req)
		return w
	}

	t.Run("returns profile with wallets, circles, communities and sends", func(t *testing.T) {
		urepo := new(userMocks.Repository)
		urepo.On("FindByID", mock.Anything, uid).Return(&user.User{ID: uid, Role: user.RoleUser}, nil)
		wrepo := &fakeModWalletRepo{hasWallet: map[string]bool{uid.String(): true}}
		r := newRouter(urepo, wrepo)

		w := get(r, uid.String())
		assert.Equal(t, http.StatusOK, w.Code)
		var body struct {
			Success bool `json:"success"`
			Data    struct {
				User       map[string]any   `json:"user"`
				Wallets    []any            `json:"wallets"`
				Circles    []any            `json:"circles"`
				RecentSends []any           `json:"recentSends"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		require.True(t, body.Success)
		assert.NotNil(t, body.Data.User)
		assert.NotNil(t, body.Data.Wallets)
		assert.NotNil(t, body.Data.Circles)
		assert.NotNil(t, body.Data.RecentSends)
	})

	t.Run("unknown user is 404", func(t *testing.T) {
		urepo := new(userMocks.Repository)
		urepo.On("FindByID", mock.Anything, mock.Anything).Return(nil, apperrors.ErrNotFound)
		r := newRouter(urepo, &fakeModWalletRepo{})

		w := get(r, uid.String())
		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("invalid id is 400", func(t *testing.T) {
		r := newRouter(new(userMocks.Repository), &fakeModWalletRepo{})
		w := get(r, "not-a-uuid")
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}
