package community_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/community"
	communityMocks "github.com/deodes/backend/internal/domain/community/mocks"
	"github.com/deodes/backend/pkg/apperrors"
)

func suspendedCommunity() *community.Community {
	at := newCommunity(uuid.New())
	ts := time.Now().UTC()
	at.SuspendedAt = &ts
	reason := "spam"
	at.SuspendedReason = &reason
	return at
}

func TestCommunityService_SuspendCommunity(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	id := uuid.New()
	repo.On("FindByID", mock.Anything, id).Return(newCommunity(uuid.New()), nil)
	repo.On("Suspend", mock.Anything, id, "spam").Return(nil)

	require.NoError(t, svc.SuspendCommunity(ctx(), id.String(), "spam"))
	repo.AssertExpectations(t)
}

func TestCommunityService_UnsuspendCommunity(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	id := uuid.New()
	repo.On("FindByID", mock.Anything, id).Return(suspendedCommunity(), nil)
	repo.On("Unsuspend", mock.Anything, id).Return(nil)

	require.NoError(t, svc.UnsuspendCommunity(ctx(), id.String()))
	repo.AssertExpectations(t)
}

func TestCommunityService_AdminRemoveCommunity(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	id := uuid.New()
	repo.On("FindByID", mock.Anything, id).Return(newCommunity(uuid.New()), nil)
	repo.On("Delete", mock.Anything, id).Return(nil)

	require.NoError(t, svc.AdminRemoveCommunity(ctx(), id.String()))
	repo.AssertExpectations(t)
}

func TestCommunityService_JoinBlockedWhenSuspended(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	c := suspendedCommunity()
	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	err := svc.Join(ctx(), c.ID.String(), uuid.New().String())
	assert.ErrorIs(t, err, apperrors.ErrForbidden)
	repo.AssertNotCalled(t, "AddMember", mock.Anything, mock.Anything)
}

func TestCommunityService_UpdateBlockedWhenSuspended(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	c := suspendedCommunity()
	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	_, err := svc.Update(ctx(), c.ID.String(), uuid.New().String(), community.UpdateCommunityInput{})
	assert.ErrorIs(t, err, apperrors.ErrForbidden)
	repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
}
