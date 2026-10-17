package community_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/deodes/backend/internal/domain/community"
	communityMocks "github.com/deodes/backend/internal/domain/community/mocks"
	"github.com/deodes/backend/pkg/apperrors"
)

func ctx() context.Context { return context.Background() }

// mockBroadcaster satisfies the community.Broadcaster interface.
type mockBroadcaster struct{ mock.Mock }

func (m *mockBroadcaster) CommunityJoined(ctx context.Context, communityID, userID string) {
	m.Called(ctx, communityID, userID)
}

func newCommunity(ownerID uuid.UUID) *community.Community {
	return &community.Community{
		ID: uuid.New(), Name: "Test", Slug: "test", OwnerID: ownerID,
	}
}

func TestCommunityService_Create_AddsOwnerAsAdmin(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	uid := uuid.New()

	repo.On("Create", mock.Anything, mock.AnythingOfType("*community.Community")).Return(nil)
	repo.On("AddMember", mock.Anything, mock.AnythingOfType("*community.CommunityMember")).Return(nil)

	c, err := svc.Create(ctx(), uid.String(), community.CreateCommunityInput{Name: "Savings Group", Slug: "savings"})

	assert.NoError(t, err)
	assert.NotNil(t, c)
	assert.Equal(t, uid, c.OwnerID)
	assert.Equal(t, "community", c.Category)
	repo.AssertExpectations(t)
}

func TestCommunityService_Create_NilTagsDefaultToEmpty(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	uid := uuid.New()

	var created *community.Community
	repo.On("Create", mock.Anything, mock.AnythingOfType("*community.Community")).
		Run(func(args mock.Arguments) {
			created = args.Get(1).(*community.Community)
		}).
		Return(nil)
	repo.On("AddMember", mock.Anything, mock.AnythingOfType("*community.CommunityMember")).Return(nil)

	c, err := svc.Create(ctx(), uid.String(), community.CreateCommunityInput{Name: "No Tags", Slug: "no-tags"})

	assert.NoError(t, err)
	assert.NotNil(t, c)
	assert.NotNil(t, created.Tags, "nil tags must default to empty, the column is NOT NULL")
	assert.Empty(t, created.Tags)
	repo.AssertExpectations(t)
}

func TestCommunityService_Create_SlugConflict(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	uid := uuid.New()

	repo.On("Create", mock.Anything, mock.AnythingOfType("*community.Community")).Return(apperrors.ErrConflict)

	c, err := svc.Create(ctx(), uid.String(), community.CreateCommunityInput{Name: "X", Slug: "taken"})

	assert.Error(t, err)
	assert.Nil(t, c)
	assert.Contains(t, err.Error(), "already exists")
	repo.AssertExpectations(t)
}

func TestCommunityService_Create_MemberFailsRollsBack(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	uid := uuid.New()

	repo.On("Create", mock.Anything, mock.AnythingOfType("*community.Community")).Return(nil)
	repo.On("AddMember", mock.Anything, mock.AnythingOfType("*community.CommunityMember")).Return(assert.AnError)
	repo.On("Delete", mock.Anything, mock.AnythingOfType("uuid.UUID")).Return(nil)

	c, err := svc.Create(ctx(), uid.String(), community.CreateCommunityInput{Name: "X", Slug: "x"})

	assert.Error(t, err)
	assert.Nil(t, c)
	repo.AssertExpectations(t)
}

func TestCommunityService_Get(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	c := newCommunity(uuid.New())

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	got, err := svc.Get(ctx(), c.ID.String())

	assert.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
	repo.AssertExpectations(t)
}

func TestCommunityService_Get_NotFound(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	id := uuid.New()

	repo.On("FindByID", mock.Anything, id).Return(nil, apperrors.ErrNotFound)

	got, err := svc.Get(ctx(), id.String())

	assert.Error(t, err)
	assert.Equal(t, apperrors.ErrNotFound, err)
	assert.Nil(t, got)
	repo.AssertExpectations(t)
}

func TestCommunityService_Update_OwnerAllows(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	ownerID := uuid.New()
	c := newCommunity(ownerID)

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)
	repo.On("Update", mock.Anything, mock.AnythingOfType("*community.Community")).Return(nil)

	name := "Renamed"
	got, err := svc.Update(ctx(), c.ID.String(), ownerID.String(), community.UpdateCommunityInput{Name: &name})

	assert.NoError(t, err)
	assert.Equal(t, "Renamed", got.Name)
	repo.AssertExpectations(t)
}

func TestCommunityService_Update_NonOwnerForbidden(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	c := newCommunity(uuid.New())

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	name := "Renamed"
	got, err := svc.Update(ctx(), c.ID.String(), uuid.New().String(), community.UpdateCommunityInput{Name: &name})

	assert.Error(t, err)
	assert.Equal(t, apperrors.ErrForbidden, err)
	assert.Nil(t, got)
	repo.AssertExpectations(t)
}

func TestCommunityService_Delete_OwnerAllows(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	ownerID := uuid.New()
	c := newCommunity(ownerID)

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)
	repo.On("Delete", mock.Anything, c.ID).Return(nil)

	err := svc.Delete(ctx(), c.ID.String(), ownerID.String())

	assert.NoError(t, err)
	repo.AssertExpectations(t)
}

func TestCommunityService_Delete_NonOwnerForbidden(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	c := newCommunity(uuid.New())

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	err := svc.Delete(ctx(), c.ID.String(), uuid.New().String())

	assert.Error(t, err)
	assert.Equal(t, apperrors.ErrForbidden, err)
	repo.AssertExpectations(t)
}

func TestCommunityService_Join_Broadcasts(t *testing.T) {
	repo := new(communityMocks.Repository)
	bc := new(mockBroadcaster)
	svc := community.NewService(repo, bc)
	cid, uid := uuid.New(), uuid.New()

	repo.On("FindByID", mock.Anything, cid).Return(&community.Community{}, nil)
	repo.On("IsMember", mock.Anything, cid, uid).Return(false, nil)
	repo.On("AddMember", mock.Anything, mock.AnythingOfType("*community.CommunityMember")).Return(nil)
	bc.On("CommunityJoined", mock.Anything, cid.String(), uid.String()).Return()

	err := svc.Join(ctx(), cid.String(), uid.String())

	assert.NoError(t, err)
	repo.AssertExpectations(t)
	bc.AssertExpectations(t)
}

func TestCommunityService_Leave_OwnerCannotLeave(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	ownerID := uuid.New()
	c := newCommunity(ownerID)

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	err := svc.Leave(ctx(), c.ID.String(), ownerID.String())

	assert.Error(t, err)
	repo.AssertNotCalled(t, "RemoveMember", mock.Anything, mock.Anything, mock.Anything)
	repo.AssertExpectations(t)
}

func TestCommunityService_Leave_Member(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	ownerID := uuid.New()
	c := newCommunity(ownerID)
	uid := uuid.New()

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)
	repo.On("RemoveMember", mock.Anything, c.ID, uid).Return(nil)

	err := svc.Leave(ctx(), c.ID.String(), uid.String())

	assert.NoError(t, err)
	repo.AssertExpectations(t)
}

func TestCommunityService_IsMember(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	cid, uid := uuid.New(), uuid.New()

	repo.On("IsMember", mock.Anything, cid, uid).Return(true, nil)

	ok, err := svc.IsMember(ctx(), cid.String(), uid.String())

	assert.NoError(t, err)
	assert.True(t, ok)
	repo.AssertExpectations(t)
}

func TestCommunityService_CreateAnnouncement(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	cid, uid := uuid.New(), uuid.New()

	repo.On("IsMember", mock.Anything, cid, uid).Return(true, nil)
	repo.On("CreateAnnouncement", mock.Anything, mock.AnythingOfType("*community.Announcement")).Return(nil)

	a, err := svc.CreateAnnouncement(ctx(), cid.String(), uid.String(), "hello world")

	assert.NoError(t, err)
	assert.Equal(t, cid, a.CommunityID)
	assert.Equal(t, uid, a.AuthorID)
	assert.False(t, a.IsPinned)
	repo.AssertExpectations(t)
}

func TestCommunityService_DeleteAnnouncement(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	id := uuid.New()

	// allow delete by author
	ann := &community.Announcement{ID: id, CommunityID: uuid.New(), AuthorID: uuid.New()}
	repo.On("GetAnnouncementByID", mock.Anything, id).Return(ann, nil)
	repo.On("DeleteAnnouncement", mock.Anything, id).Return(nil)

	err := svc.DeleteAnnouncement(ctx(), id.String(), ann.AuthorID.String())

	assert.NoError(t, err)
	repo.AssertExpectations(t)
}

func TestCommunityService_LikeAnnouncement(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	id := uuid.New()

	ann := &community.Announcement{ID: id, CommunityID: uuid.New(), AuthorID: uuid.New()}
	repo.On("GetAnnouncementByID", mock.Anything, id).Return(ann, nil)
	repo.On("IsMember", mock.Anything, ann.CommunityID, mock.Anything).Return(true, nil)
	repo.On("LikeAnnouncement", mock.Anything, id).Return(nil)

	err := svc.LikeAnnouncement(ctx(), id.String(), uuid.New().String())

	assert.NoError(t, err)
	repo.AssertExpectations(t)
}

func TestCommunityService_PinAnnouncement(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	id := uuid.New()

	commID := uuid.New()
	ann := &community.Announcement{ID: id, CommunityID: commID, AuthorID: uuid.New()}
	owner := uuid.New()
	comm := newCommunity(owner)
	comm.ID = commID

	repo.On("GetAnnouncementByID", mock.Anything, id).Return(ann, nil)
	repo.On("FindByID", mock.Anything, commID).Return(comm, nil)
	repo.On("SetAnnouncementPin", mock.Anything, id, true).Return(nil)

	err := svc.PinAnnouncement(ctx(), id.String(), owner.String(), true)

	assert.NoError(t, err)
	repo.AssertExpectations(t)
}

func TestCommunityService_RemoveMember_OwnerAllows(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	ownerID := uuid.New()
	c := newCommunity(ownerID)
	targetID := uuid.New()

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)
	repo.On("RemoveMember", mock.Anything, c.ID, targetID).Return(nil)

	err := svc.RemoveMember(ctx(), c.ID.String(), ownerID.String(), targetID.String())

	assert.NoError(t, err)
	repo.AssertExpectations(t)
}

func TestCommunityService_RemoveMember_NonOwnerForbidden(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	ownerID := uuid.New()
	c := newCommunity(ownerID)

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	err := svc.RemoveMember(ctx(), c.ID.String(), uuid.New().String(), uuid.New().String())

	assert.Error(t, err)
	assert.Equal(t, apperrors.ErrForbidden, err)
	repo.AssertExpectations(t)
}

func TestCommunityService_TransferOwnership(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	ownerID := uuid.New()
	c := newCommunity(ownerID)
	newOwner := uuid.New()

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)
	repo.On("IsMember", mock.Anything, c.ID, newOwner).Return(true, nil)
	repo.On("UpdateOwner", mock.Anything, c.ID, newOwner).Return(nil)
	repo.On("UpdateMemberRole", mock.Anything, c.ID, ownerID, "member").Return(nil)

	err := svc.TransferOwnership(ctx(), c.ID.String(), ownerID.String(), newOwner.String())

	assert.NoError(t, err)
	repo.AssertExpectations(t)
}

func TestCommunityService_TransferOwnership_NonOwnerForbidden(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	c := newCommunity(uuid.New())

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	err := svc.TransferOwnership(ctx(), c.ID.String(), uuid.New().String(), uuid.New().String())

	assert.Error(t, err)
	assert.Equal(t, apperrors.ErrForbidden, err)
	repo.AssertExpectations(t)
}

func TestCommunityService_GetMyCommunities(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	uid := uuid.New()

	repo.On("FindByUserID", mock.Anything, uid).Return([]community.Community{*newCommunity(uid)}, nil)

	communities, err := svc.GetMyCommunities(ctx(), uid.String())

	assert.NoError(t, err)
	assert.Len(t, communities, 1)
	repo.AssertExpectations(t)
}

func TestCommunityService_GetMyCommunities_Empty(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	uid := uuid.New()

	repo.On("FindByUserID", mock.Anything, uid).Return(nil, nil)

	communities, err := svc.GetMyCommunities(ctx(), uid.String())

	assert.NoError(t, err)
	assert.NotNil(t, communities)
	assert.Len(t, communities, 0)
	repo.AssertExpectations(t)
}
