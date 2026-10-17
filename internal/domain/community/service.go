package community

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/deodes/backend/pkg/apperrors"
)

type Service interface {
	Create(ctx context.Context, userID string, input CreateCommunityInput) (*Community, error)
	Get(ctx context.Context, id string) (*Community, error)
	GetBySlug(ctx context.Context, slug string) (*Community, error)
	List(ctx context.Context, filter CommunityFilter) ([]Community, int, error)
	Update(ctx context.Context, id, userID string, input UpdateCommunityInput) (*Community, error)
	Delete(ctx context.Context, id, userID string) error
	// SuspendCommunity freezes a community (admin moderation); UnsuspendCommunity
	// clears it. AdminRemoveCommunity deletes without the owner check (ban).
	SuspendCommunity(ctx context.Context, id, reason string) error
	UnsuspendCommunity(ctx context.Context, id string) error
	AdminRemoveCommunity(ctx context.Context, id string) error

	Join(ctx context.Context, communityID, userID string) error
	Leave(ctx context.Context, communityID, userID string) error
	GetMembers(ctx context.Context, communityID string) ([]CommunityMember, error)
	SearchMembers(ctx context.Context, communityID, search string) ([]CommunityMember, error)
	IsMember(ctx context.Context, communityID, userID string) (bool, error)

	CreateAnnouncement(ctx context.Context, communityID, userID, content string) (*Announcement, error)
	GetAnnouncements(ctx context.Context, communityID string) ([]Announcement, error)
	DeleteAnnouncement(ctx context.Context, id, userID string) error
	LikeAnnouncement(ctx context.Context, id string, userID string) error

	PinAnnouncement(ctx context.Context, id, userID string, pinned bool) error
	RemoveMember(ctx context.Context, communityID, userID, targetID string) error
	TransferOwnership(ctx context.Context, communityID, userID, newOwnerID string) error
	GetActivity(ctx context.Context, communityID string, limit int) ([]ActivityEvent, error)
	GetMyCommunities(ctx context.Context, userID string) ([]Community, error)
	RecordActivity(ctx context.Context, communityID, eventType string, actorID string, targetID string, metadata map[string]interface{}) error
	UpdateTotalSaved(ctx context.Context, communityID string) error
}

type Broadcaster interface {
	CommunityJoined(ctx context.Context, communityID, userID string)
}

type communityService struct {
	repo        Repository
	broadcaster Broadcaster
}

func NewService(repo Repository, broadcaster Broadcaster) Service {
	return &communityService{repo: repo, broadcaster: broadcaster}
}

func parseUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid UUID: %w", err)
	}
	return id, nil
}

func (s *communityService) Create(ctx context.Context, userID string, input CreateCommunityInput) (*Community, error) {
	ownerID, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	tags := input.Tags
	if tags == nil {
		// pq serializes a nil StringArray as NULL, but tags is NOT NULL —
		// default to empty so tag-less communities don't 500.
		tags = pq.StringArray{}
	}
	c := &Community{
		ID:          uuid.New(),
		Name:        input.Name,
		Slug:        input.Slug,
		Description: input.Description,
		Category:    input.Category,
		Tags:        tags,
		OwnerID:     ownerID,
		MemberCount: 1,
		IsFeatured:  false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if c.Category == "" {
		c.Category = "community"
	}

	if err := s.repo.Create(ctx, c); err != nil {
		if err == apperrors.ErrConflict {
			return nil, fmt.Errorf("a community with this slug already exists")
		}
		return nil, fmt.Errorf("creating community: %w", err)
	}

	member := &CommunityMember{
		CommunityID: c.ID,
		UserID:      ownerID,
		Role:        "admin",
		JoinedAt:    now,
	}
	if err := s.repo.AddMember(ctx, member); err != nil {
		_ = s.repo.Delete(ctx, c.ID)
		return nil, fmt.Errorf("adding owner as member: %w", err)
	}

	return c, nil
}

func (s *communityService) Get(ctx context.Context, id string) (*Community, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	c, err := s.repo.FindByID(ctx, uid)
	if err != nil {
		if err == apperrors.ErrNotFound {
			return nil, apperrors.ErrNotFound
		}
		return nil, fmt.Errorf("getting community: %w", err)
	}
	return c, nil
}

func (s *communityService) GetBySlug(ctx context.Context, slug string) (*Community, error) {
	return s.repo.FindBySlug(ctx, slug)
}

func (s *communityService) List(ctx context.Context, filter CommunityFilter) ([]Community, int, error) {
	communities, total, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("listing communities: %w", err)
	}
	if communities == nil {
		communities = []Community{}
	}
	return communities, total, nil
}

func (s *communityService) Update(ctx context.Context, id, userID string, input UpdateCommunityInput) (*Community, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return nil, err
	}

	c, err := s.repo.FindByID(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("finding community: %w", err)
	}

	ownerID, _ := parseUUID(userID)
	if c.OwnerID != ownerID {
		return nil, apperrors.ErrForbidden
	}
	if c.SuspendedAt != nil {
		return nil, fmt.Errorf("community is suspended by moderation: %w", apperrors.ErrForbidden)
	}

	if input.Name != nil {
		c.Name = *input.Name
	}
	if input.Description != nil {
		c.Description = *input.Description
	}
	if input.Category != nil {
		c.Category = *input.Category
	}
	if input.Tags != nil {
		c.Tags = *input.Tags
	}
	c.AvatarURL = input.AvatarURL
	c.BannerURL = input.BannerURL

	if err := s.repo.Update(ctx, c); err != nil {
		return nil, fmt.Errorf("updating community: %w", err)
	}
	return c, nil
}

func (s *communityService) Delete(ctx context.Context, id, userID string) error {
	uid, err := parseUUID(id)
	if err != nil {
		return err
	}

	c, err := s.repo.FindByID(ctx, uid)
	if err != nil {
		return fmt.Errorf("finding community: %w", err)
	}

	ownerID, _ := parseUUID(userID)
	if c.OwnerID != ownerID {
		return apperrors.ErrForbidden
	}

	return s.repo.Delete(ctx, uid)
}

// SuspendCommunity freezes a community (admin moderation): hidden from
// public listing, no joins/edits/new circles until unsuspended.
func (s *communityService) SuspendCommunity(ctx context.Context, id, reason string) error {
	uid, err := parseUUID(id)
	if err != nil {
		return err
	}
	if _, err := s.repo.FindByID(ctx, uid); err != nil {
		return fmt.Errorf("finding community: %w", err)
	}
	return s.repo.Suspend(ctx, uid, reason)
}

// UnsuspendCommunity clears a moderation freeze.
func (s *communityService) UnsuspendCommunity(ctx context.Context, id string) error {
	uid, err := parseUUID(id)
	if err != nil {
		return err
	}
	if _, err := s.repo.FindByID(ctx, uid); err != nil {
		return fmt.Errorf("finding community: %w", err)
	}
	return s.repo.Unsuspend(ctx, uid)
}

// AdminRemoveCommunity deletes a community without the owner check (ban).
func (s *communityService) AdminRemoveCommunity(ctx context.Context, id string) error {
	uid, err := parseUUID(id)
	if err != nil {
		return err
	}
	if _, err := s.repo.FindByID(ctx, uid); err != nil {
		return fmt.Errorf("finding community: %w", err)
	}
	return s.repo.Delete(ctx, uid)
}

func (s *communityService) Join(ctx context.Context, communityID, userID string) error {
	cid, err := parseUUID(communityID)
	if err != nil {
		return err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}

	member := &CommunityMember{
		CommunityID: cid,
		UserID:      uid,
		Role:        "member",
		JoinedAt:    time.Now().UTC(),
	}
	if c, err := s.repo.FindByID(ctx, cid); err != nil {
		return fmt.Errorf("finding community for join: %w", err)
	} else if c.SuspendedAt != nil {
		return fmt.Errorf("community is suspended by moderation: %w", apperrors.ErrForbidden)
	}
	if ok, err := s.repo.IsMember(ctx, cid, uid); err != nil {
		return fmt.Errorf("checking community membership: %w", err)
	} else if ok {
		return apperrors.ErrConflict
	}
	if err := s.repo.AddMember(ctx, member); err != nil {
		return err
	}
	if s.broadcaster != nil {
		s.broadcaster.CommunityJoined(ctx, communityID, userID)
	}
	return nil
}

func (s *communityService) Leave(ctx context.Context, communityID, userID string) error {
	cid, err := parseUUID(communityID)
	if err != nil {
		return err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return err
	}

	ownerUUID, _ := parseUUID(userID)
	if c.OwnerID == ownerUUID {
		return fmt.Errorf("owner cannot leave; transfer ownership or delete the community")
	}

	return s.repo.RemoveMember(ctx, cid, uid)
}

func (s *communityService) GetMembers(ctx context.Context, communityID string) ([]CommunityMember, error) {
	cid, err := parseUUID(communityID)
	if err != nil {
		return nil, err
	}
	return s.repo.GetMembers(ctx, cid)
}

// SearchMembers filters community members by username/display name substring.
func (s *communityService) SearchMembers(ctx context.Context, communityID, search string) ([]CommunityMember, error) {
	cid, err := parseUUID(communityID)
	if err != nil {
		return nil, err
	}
	return s.repo.SearchMembers(ctx, cid, search)
}

func (s *communityService) IsMember(ctx context.Context, communityID, userID string) (bool, error) {
	cid, err := parseUUID(communityID)
	if err != nil {
		return false, err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return false, err
	}
	return s.repo.IsMember(ctx, cid, uid)
}

func (s *communityService) CreateAnnouncement(ctx context.Context, communityID, userID, content string) (*Announcement, error) {
	cid, err := parseUUID(communityID)
	if err != nil {
		return nil, err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}

	// Require membership to create announcements
	isMember, err := s.repo.IsMember(ctx, cid, uid)
	if err != nil {
		return nil, fmt.Errorf("checking membership: %w", err)
	}
	if !isMember {
		return nil, apperrors.ErrForbidden
	}

	now := time.Now().UTC()
	a := &Announcement{
		ID:          uuid.New(),
		CommunityID: cid,
		AuthorID:    uid,
		Content:     content,
		IsPinned:    false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.CreateAnnouncement(ctx, a); err != nil {
		return nil, fmt.Errorf("creating announcement: %w", err)
	}
	return a, nil
}

func (s *communityService) GetAnnouncements(ctx context.Context, communityID string) ([]Announcement, error) {
	cid, err := parseUUID(communityID)
	if err != nil {
		return nil, err
	}
	return s.repo.GetAnnouncements(ctx, cid, false)
}

func (s *communityService) DeleteAnnouncement(ctx context.Context, id, userID string) error {
	uid, err := parseUUID(id)
	if err != nil {
		return err
	}
	requesterID, err := parseUUID(userID)
	if err != nil {
		return err
	}

	ann, err := s.repo.GetAnnouncementByID(ctx, uid)
	if err != nil {
		return err
	}

	// Allow the author or community admin/owner to delete
	if ann.AuthorID == requesterID {
		return s.repo.DeleteAnnouncement(ctx, uid)
	}

	// Fetch community to check owner
	comm, err := s.repo.FindByID(ctx, ann.CommunityID)
	if err != nil {
		return err
	}
	if comm.OwnerID == requesterID {
		return s.repo.DeleteAnnouncement(ctx, uid)
	}

	// Check member role
	members, err := s.repo.GetMembers(ctx, ann.CommunityID)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.UserID == requesterID && m.Role == "admin" {
			return s.repo.DeleteAnnouncement(ctx, uid)
		}
	}

	return apperrors.ErrForbidden
}

func (s *communityService) LikeAnnouncement(ctx context.Context, id string, userID string) error {
	annID, err := parseUUID(id)
	if err != nil {
		return err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}

	ann, err := s.repo.GetAnnouncementByID(ctx, annID)
	if err != nil {
		return err
	}

	// require membership to like
	isMember, err := s.repo.IsMember(ctx, ann.CommunityID, uid)
	if err != nil {
		return fmt.Errorf("checking membership: %w", err)
	}
	if !isMember {
		return apperrors.ErrForbidden
	}
	return s.repo.LikeAnnouncement(ctx, annID)
}

func (s *communityService) PinAnnouncement(ctx context.Context, id, userID string, pinned bool) error {
	annID, err := parseUUID(id)
	if err != nil {
		return err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}

	ann, err := s.repo.GetAnnouncementByID(ctx, annID)
	if err != nil {
		return err
	}

	// Only admins/owner can pin
	comm, err := s.repo.FindByID(ctx, ann.CommunityID)
	if err != nil {
		return err
	}
	if comm.OwnerID == uid {
		return s.repo.SetAnnouncementPin(ctx, annID, pinned)
	}
	members, err := s.repo.GetMembers(ctx, ann.CommunityID)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.UserID == uid && m.Role == "admin" {
			return s.repo.SetAnnouncementPin(ctx, annID, pinned)
		}
	}
	return apperrors.ErrForbidden
}

func (s *communityService) RemoveMember(ctx context.Context, communityID, userID, targetID string) error {
	cid, err := parseUUID(communityID)
	if err != nil {
		return err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	tid, err := parseUUID(targetID)
	if err != nil {
		return err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return err
	}
	if c.OwnerID != uid {
		return apperrors.ErrForbidden
	}

	return s.repo.RemoveMember(ctx, cid, tid)
}

func (s *communityService) TransferOwnership(ctx context.Context, communityID, userID, newOwnerID string) error {
	cid, err := parseUUID(communityID)
	if err != nil {
		return err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	nid, err := parseUUID(newOwnerID)
	if err != nil {
		return err
	}

	c, err := s.repo.FindByID(ctx, cid)
	if err != nil {
		return err
	}
	if c.OwnerID != uid {
		return apperrors.ErrForbidden
	}

	// Verify new owner is a member
	isMember, err := s.repo.IsMember(ctx, cid, nid)
	if err != nil {
		return fmt.Errorf("checking new owner membership: %w", err)
	}
	if !isMember {
		return apperrors.ErrInvalidInput
	}

	if err := s.repo.UpdateOwner(ctx, cid, nid); err != nil {
		return fmt.Errorf("transferring ownership: %w", err)
	}

	// demote previous owner to member
	return s.repo.UpdateMemberRole(ctx, cid, uid, "member")
}

func (s *communityService) GetActivity(ctx context.Context, communityID string, limit int) ([]ActivityEvent, error) {
	cid, err := parseUUID(communityID)
	if err != nil {
		return nil, err
	}
	events, err := s.repo.GetActivity(ctx, cid, limit)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []ActivityEvent{}
	}
	return events, nil
}

func (s *communityService) GetMyCommunities(ctx context.Context, userID string) ([]Community, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, err
	}
	communities, err := s.repo.FindByUserID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if communities == nil {
		communities = []Community{}
	}
	return communities, nil
}

func (s *communityService) RecordActivity(ctx context.Context, communityID, eventType string, actorID string, targetID string, metadata map[string]interface{}) error {
	cid, err := parseUUID(communityID)
	if err != nil {
		return err
	}
	var actorUUID, targetUUID *uuid.UUID
	if actorID != "" {
		id, _ := parseUUID(actorID)
		actorUUID = &id
	}
	if targetID != "" {
		id, _ := parseUUID(targetID)
		targetUUID = &id
	}
	e := &ActivityEvent{
		ID:          uuid.New(),
		CommunityID: cid,
		EventType:   eventType,
		ActorID:     actorUUID,
		TargetID:    targetUUID,
		Metadata:    metadata,
		CreatedAt:   time.Now().UTC(),
	}
	return s.repo.RecordActivity(ctx, e)
}

func (s *communityService) UpdateTotalSaved(ctx context.Context, communityID string) error {
	cid, err := parseUUID(communityID)
	if err != nil {
		return err
	}
	return s.repo.UpdateTotalSaved(ctx, cid)
}
