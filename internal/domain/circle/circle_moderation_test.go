package circle_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/domain/circle"
	circleMocks "github.com/deodes/backend/internal/domain/circle/mocks"
)

func moderationCircle(status circle.CircleStatus) *circle.Circle {
	return &circle.Circle{ID: uuid.New(), Status: status}
}

func TestCircleService_PauseCircle(t *testing.T) {
	ctx := context.Background()

	t.Run("active pauses", func(t *testing.T) {
		repo := new(circleMocks.Repository)
		svc := circle.NewService(repo, nil, circle.Dependencies{})
		id := uuid.New()
		repo.On("FindByID", ctx, id).Return(moderationCircle(circle.CircleStatusActive), nil)
		repo.On("Update", ctx, mock.MatchedBy(func(c *circle.Circle) bool {
			return c.Status == circle.CircleStatusPaused
		})).Return(nil)

		require.NoError(t, svc.PauseCircle(ctx, id.String()))
		repo.AssertExpectations(t)
	})

	for _, status := range []circle.CircleStatus{
		circle.CircleStatusPending, circle.CircleStatusPaused,
		circle.CircleStatusBanned, circle.CircleStatusCompleted, circle.CircleStatusCancelled,
	} {
		t.Run("cannot pause "+string(status), func(t *testing.T) {
			repo := new(circleMocks.Repository)
			svc := circle.NewService(repo, nil, circle.Dependencies{})
			id := uuid.New()
			repo.On("FindByID", ctx, id).Return(moderationCircle(status), nil)

			require.Error(t, svc.PauseCircle(ctx, id.String()))
			repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
		})
	}

	t.Run("invalid id fails", func(t *testing.T) {
		repo := new(circleMocks.Repository)
		svc := circle.NewService(repo, nil, circle.Dependencies{})
		require.Error(t, svc.PauseCircle(ctx, "not-a-uuid"))
	})
}

func TestCircleService_ResumeCircle(t *testing.T) {
	ctx := context.Background()

	t.Run("paused resumes to active", func(t *testing.T) {
		repo := new(circleMocks.Repository)
		svc := circle.NewService(repo, nil, circle.Dependencies{})
		id := uuid.New()
		repo.On("FindByID", ctx, id).Return(moderationCircle(circle.CircleStatusPaused), nil)
		repo.On("Update", ctx, mock.MatchedBy(func(c *circle.Circle) bool {
			return c.Status == circle.CircleStatusActive
		})).Return(nil)

		require.NoError(t, svc.ResumeCircle(ctx, id.String()))
		repo.AssertExpectations(t)
	})

	for _, status := range []circle.CircleStatus{
		circle.CircleStatusPending, circle.CircleStatusActive,
		circle.CircleStatusBanned, circle.CircleStatusCompleted,
	} {
		t.Run("cannot resume "+string(status), func(t *testing.T) {
			repo := new(circleMocks.Repository)
			svc := circle.NewService(repo, nil, circle.Dependencies{})
			id := uuid.New()
			repo.On("FindByID", ctx, id).Return(moderationCircle(status), nil)

			require.Error(t, svc.ResumeCircle(ctx, id.String()))
			repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
		})
	}
}

func TestCircleService_BanCircle(t *testing.T) {
	ctx := context.Background()

	for _, status := range []circle.CircleStatus{
		circle.CircleStatusPending, circle.CircleStatusActive, circle.CircleStatusPaused,
	} {
		t.Run("bans "+string(status), func(t *testing.T) {
			repo := new(circleMocks.Repository)
			svc := circle.NewService(repo, nil, circle.Dependencies{})
			id := uuid.New()
			repo.On("FindByID", ctx, id).Return(moderationCircle(status), nil)
			repo.On("Update", ctx, mock.MatchedBy(func(c *circle.Circle) bool {
				return c.Status == circle.CircleStatusBanned
			})).Return(nil)

			require.NoError(t, svc.BanCircle(ctx, id.String()))
			repo.AssertExpectations(t)
		})
	}

	for _, status := range []circle.CircleStatus{
		circle.CircleStatusBanned, circle.CircleStatusCompleted, circle.CircleStatusCancelled,
	} {
		t.Run("cannot ban terminal "+string(status), func(t *testing.T) {
			repo := new(circleMocks.Repository)
			svc := circle.NewService(repo, nil, circle.Dependencies{})
			id := uuid.New()
			repo.On("FindByID", ctx, id).Return(moderationCircle(status), nil)

			require.Error(t, svc.BanCircle(ctx, id.String()))
			repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
		})
	}

	t.Run("unknown circle fails", func(t *testing.T) {
		repo := new(circleMocks.Repository)
		svc := circle.NewService(repo, nil, circle.Dependencies{})
		id := uuid.New()
		repo.On("FindByID", ctx, id).Return(nil, assert.AnError)

		require.Error(t, svc.BanCircle(ctx, id.String()))
	})
}
