package mocks

import (
	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"

	"github.com/deodes/backend/internal/domain/community"
)

type Repository struct {
	mock.Mock
}

func (m *Repository) Create(ctx context.Context, c *community.Community) error {
	return m.Called(ctx, c).Error(0)
}

func (m *Repository) FindByID(ctx context.Context, id uuid.UUID) (*community.Community, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*community.Community), args.Error(1)
}

func (m *Repository) FindBySlug(ctx context.Context, slug string) (*community.Community, error) {
	args := m.Called(ctx, slug)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*community.Community), args.Error(1)
}

func (m *Repository) List(ctx context.Context, filter community.CommunityFilter) ([]community.Community, int, error) {
	args := m.Called(ctx, filter)
	return args.Get(0).([]community.Community), args.Int(1), args.Error(2)
}

func (m *Repository) Update(ctx context.Context, c *community.Community) error {
	return m.Called(ctx, c).Error(0)
}

func (m *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

func (m *Repository) Suspend(ctx context.Context, id uuid.UUID, reason string) error {
	return m.Called(ctx, id, reason).Error(0)
}

func (m *Repository) Unsuspend(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

func (m *Repository) AddMember(ctx context.Context, mm *community.CommunityMember) error {
	return m.Called(ctx, mm).Error(0)
}

func (m *Repository) RemoveMember(ctx context.Context, communityID, userID uuid.UUID) error {
	return m.Called(ctx, communityID, userID).Error(0)
}

func (m *Repository) UpdateMemberRole(ctx context.Context, communityID, userID uuid.UUID, role string) error {
	return m.Called(ctx, communityID, userID, role).Error(0)
}

func (m *Repository) GetMembers(ctx context.Context, communityID uuid.UUID) ([]community.CommunityMember, error) {
	args := m.Called(ctx, communityID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]community.CommunityMember), args.Error(1)
}

func (m *Repository) SearchMembers(ctx context.Context, communityID uuid.UUID, search string) ([]community.CommunityMember, error) {
	args := m.Called(ctx, communityID, search)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]community.CommunityMember), args.Error(1)
}

func (m *Repository) IsMember(ctx context.Context, communityID, userID uuid.UUID) (bool, error) {
	args := m.Called(ctx, communityID, userID)
	return args.Bool(0), args.Error(1)
}

func (m *Repository) GetMemberCount(ctx context.Context, communityID uuid.UUID) (int, error) {
	args := m.Called(ctx, communityID)
	return args.Int(0), args.Error(1)
}

func (m *Repository) CreateAnnouncement(ctx context.Context, a *community.Announcement) error {
	return m.Called(ctx, a).Error(0)
}

func (m *Repository) GetAnnouncements(ctx context.Context, communityID uuid.UUID, pinned bool) ([]community.Announcement, error) {
	args := m.Called(ctx, communityID, pinned)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]community.Announcement), args.Error(1)
}

func (m *Repository) GetAnnouncementByID(ctx context.Context, id uuid.UUID) (*community.Announcement, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*community.Announcement), args.Error(1)
}

func (m *Repository) DeleteAnnouncement(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

func (m *Repository) LikeAnnouncement(ctx context.Context, id uuid.UUID) error {
	return m.Called(ctx, id).Error(0)
}

func (m *Repository) SetAnnouncementPin(ctx context.Context, id uuid.UUID, pinned bool) error {
	return m.Called(ctx, id, pinned).Error(0)
}

func (m *Repository) RecordActivity(ctx context.Context, e *community.ActivityEvent) error {
	return m.Called(ctx, e).Error(0)
}

func (m *Repository) GetActivity(ctx context.Context, communityID uuid.UUID, limit int) ([]community.ActivityEvent, error) {
	args := m.Called(ctx, communityID, limit)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]community.ActivityEvent), args.Error(1)
}

func (m *Repository) UpdateTotalSaved(ctx context.Context, communityID uuid.UUID) error {
	return m.Called(ctx, communityID).Error(0)
}

func (m *Repository) FindByUserID(ctx context.Context, userID uuid.UUID) ([]community.Community, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]community.Community), args.Error(1)
}

func (m *Repository) UpdateOwner(ctx context.Context, communityID, newOwnerID uuid.UUID) error {
	return m.Called(ctx, communityID, newOwnerID).Error(0)
}
