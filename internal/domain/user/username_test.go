package user_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/user"
	userMocks "github.com/deodes/backend/internal/domain/user/mocks"
	"github.com/deodes/backend/pkg/apperrors"
)

func TestDeriveUsername(t *testing.T) {
	assert.Equal(t, "ada", user.DeriveUsername("Ada@Example.COM"))
	assert.Equal(t, "ada", user.DeriveUsername("ada+promo@gmail.com"))
	assert.Equal(t, "ada_osei", user.DeriveUsername("Ada_Osei@x.io"))
	assert.Equal(t, "user", user.DeriveUsername("!!!@x.io"))
	assert.Equal(t, "user", user.DeriveUsername("@x.io"))
}

func TestValidateUsername(t *testing.T) {
	assert.NoError(t, user.ValidateUsername("ada"))
	assert.NoError(t, user.ValidateUsername("ada_osei9"))
	assert.Error(t, user.ValidateUsername("A"))
	assert.Error(t, user.ValidateUsername("Ada"))
	assert.Error(t, user.ValidateUsername("a-!"))
}

func TestEnsureUsername_FreeBase(t *testing.T) {
	repo := new(userMocks.Repository)
	ctx := context.Background()
	repo.On("FindByUsername", ctx, "ada").Return(nil, apperrors.ErrNotFound)

	name, err := user.EnsureUsername(ctx, repo, "ada@example.com")
	require.NoError(t, err)
	assert.Equal(t, "ada", name)
	repo.AssertExpectations(t)
}

func TestEnsureUsername_TakenBaseGetsSuffix(t *testing.T) {
	repo := new(userMocks.Repository)
	ctx := context.Background()
	existing := &user.User{ID: uuid.New()}
	repo.On("FindByUsername", ctx, "ada").Return(existing, nil)
	repo.On("FindByUsername", ctx, mock.AnythingOfType("string")).Return(nil, apperrors.ErrNotFound)

	name, err := user.EnsureUsername(ctx, repo, "ada@yahoo.com")
	require.NoError(t, err)
	assert.True(t, len(name) > 4 && name[:4] == "ada-")
	assert.NoError(t, user.ValidateUsername(name))
}

func TestUpdateProfile_Username(t *testing.T) {
	repo := new(userMocks.Repository)
	svc := user.NewService(repo, nil)
	ctx := context.Background()
	uid := uuid.New()

	existing := &user.User{ID: uid, WalletAddress: "GABC..."}
	repo.On("FindByID", ctx, uid).Return(existing, nil)
	repo.On("FindByUsername", ctx, "ada").Return(nil, apperrors.ErrNotFound)
	repo.On("Update", ctx, mock.AnythingOfType("*user.User")).Return(nil)

	name := "Ada"
	u, err := svc.UpdateProfile(ctx, uid.String(), user.UpdateProfileInput{Username: &name})

	require.NoError(t, err)
	require.NotNil(t, u.Username)
	assert.Equal(t, "ada", *u.Username)
	repo.AssertExpectations(t)
}

func TestUpdateProfile_UsernameTaken(t *testing.T) {
	repo := new(userMocks.Repository)
	svc := user.NewService(repo, nil)
	ctx := context.Background()
	uid := uuid.New()

	existing := &user.User{ID: uid, WalletAddress: "GABC..."}
	repo.On("FindByID", ctx, uid).Return(existing, nil)
	repo.On("FindByUsername", ctx, "ada").Return(&user.User{ID: uuid.New()}, nil)

	name := "ada"
	_, err := svc.UpdateProfile(ctx, uid.String(), user.UpdateProfileInput{Username: &name})
	assert.ErrorIs(t, err, apperrors.ErrConflict)
}
