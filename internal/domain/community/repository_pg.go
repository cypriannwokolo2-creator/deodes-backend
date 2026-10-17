package community

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"

	"github.com/deodes/backend/pkg/apperrors"
)

type pgRepo struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) Repository {
	return &pgRepo{db: db}
}

func (r *pgRepo) Create(ctx context.Context, c *Community) error {
	query := `INSERT INTO communities (id, name, slug, description, category, tags, avatar_url, banner_url, owner_id, member_count, total_saved, is_featured, created_at, updated_at, suspended_at, suspended_reason)
		VALUES (:id, :name, :slug, :description, :category, :tags, :avatar_url, :banner_url, :owner_id, :member_count, :total_saved, :is_featured, :created_at, :updated_at)`
	_, err := r.db.NamedExecContext(ctx, query, c)
	if err != nil {
		if isUniqueViolation(err) {
			return apperrors.ErrConflict
		}
		return fmt.Errorf("creating community: %w", err)
	}
	return nil
}

func scanCommunity(row interface{ Scan(...interface{}) error }) (*Community, error) {
	var c Community
	var avatarURL, bannerURL, suspendedReason sql.NullString
	var suspendedAt sql.NullTime
	err := row.Scan(
		&c.ID, &c.Name, &c.Slug, &c.Description, &c.Category,
		&c.Tags, &avatarURL, &bannerURL, &c.OwnerID,
		&c.MemberCount, &c.TotalSaved, &c.IsFeatured,
		&c.CreatedAt, &c.UpdatedAt, &suspendedAt, &suspendedReason,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, apperrors.ErrNotFound
		}
		return nil, fmt.Errorf("scanning community: %w", err)
	}
	c.AvatarURL = &avatarURL.String
	if !avatarURL.Valid {
		c.AvatarURL = nil
	}
	c.BannerURL = &bannerURL.String
	if !bannerURL.Valid {
		c.BannerURL = nil
	}
	if suspendedAt.Valid {
		c.SuspendedAt = &suspendedAt.Time
	}
	if suspendedReason.Valid {
		c.SuspendedReason = &suspendedReason.String
	}
	return &c, nil
}

// communityColumns is the canonical column list for community reads; every
// SELECT feeding scanCommunity must use it in order.
const communityColumns = `id, name, slug, description, category, tags, avatar_url, banner_url, owner_id, member_count, total_saved, is_featured, created_at, updated_at, suspended_at, suspended_reason, suspended_at, suspended_reason`

func (r *pgRepo) FindByID(ctx context.Context, id uuid.UUID) (*Community, error) {
	query := `SELECT id, name, slug, description, category, tags, avatar_url, banner_url, owner_id, member_count, total_saved, is_featured, created_at, updated_at, suspended_at, suspended_reason FROM communities WHERE id = $1 AND deleted_at IS NULL`
	return scanCommunity(r.db.QueryRowxContext(ctx, query, id))
}

func (r *pgRepo) FindBySlug(ctx context.Context, slug string) (*Community, error) {
	query := `SELECT id, name, slug, description, category, tags, avatar_url, banner_url, owner_id, member_count, total_saved, is_featured, created_at, updated_at, suspended_at, suspended_reason FROM communities WHERE slug = $1 AND deleted_at IS NULL`
	return scanCommunity(r.db.QueryRowxContext(ctx, query, slug))
}

func (r *pgRepo) List(ctx context.Context, filter CommunityFilter) ([]Community, int, error) {
	page, limit := 1, 20
	if filter.Page > 0 {
		page = filter.Page
	}
	if filter.Limit > 0 && filter.Limit <= 100 {
		limit = filter.Limit
	}
	offset := (page - 1) * limit

	var conditions []string
	var args []interface{}
	argIdx := 1

	if filter.Search != "" {
		conditions = append(conditions, fmt.Sprintf("(name ILIKE $%d OR description ILIKE $%d)", argIdx, argIdx+1))
		pat := "%" + filter.Search + "%"
		args = append(args, pat, pat)
		argIdx += 2
	}
	if filter.Category != "" {
		conditions = append(conditions, fmt.Sprintf("category = $%d", argIdx))
		args = append(args, filter.Category)
		argIdx++
	}

	conditions = append(conditions, "communities.deleted_at IS NULL")
	// Suspended communities are hidden from public listing; admins pass
	// IncludeSuspended to moderate them.
	if !filter.IncludeSuspended {
		conditions = append(conditions, "communities.suspended_at IS NULL")
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM communities %s", where)
	if err := r.db.QueryRowxContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting communities: %w", err)
	}

	query := fmt.Sprintf(`SELECT id, name, slug, description, category, tags, avatar_url, banner_url, owner_id, member_count, total_saved, is_featured, created_at, updated_at, suspended_at, suspended_reason
		FROM communities %s ORDER BY is_featured DESC, member_count DESC LIMIT $%d OFFSET $%d`, where, argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := r.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing communities: %w", err)
	}
	defer rows.Close()

	var communities []Community
	for rows.Next() {
		c, err := scanCommunity(rows)
		if err != nil {
			return nil, 0, err
		}
		communities = append(communities, *c)
	}
	return communities, total, nil
}

func (r *pgRepo) Update(ctx context.Context, c *Community) error {
	query := `UPDATE communities SET name=:name, description=:description, category=:category, tags=:tags, avatar_url=:avatar_url, banner_url=:banner_url, updated_at=NOW() WHERE id=:id`
	result, err := r.db.NamedExecContext(ctx, query, c)
	if err != nil {
		return fmt.Errorf("updating community: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return apperrors.ErrNotFound
	}
	return nil
}

func (r *pgRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE communities SET deleted_at = NOW() WHERE id = $1`, id)
	return err
}

func (r *pgRepo) Suspend(ctx context.Context, id uuid.UUID, reason string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE communities SET suspended_at = NOW(), suspended_reason = $2 WHERE id = $1 AND deleted_at IS NULL`, id, reason)
	return err
}

func (r *pgRepo) Unsuspend(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE communities SET suspended_at = NULL, suspended_reason = NULL WHERE id = $1`, id)
	return err
}

func (r *pgRepo) AddMember(ctx context.Context, m *CommunityMember) error {
	_, err := r.db.NamedExecContext(ctx, `INSERT INTO community_members (community_id, user_id, role, joined_at) VALUES (:community_id, :user_id, :role, :joined_at)`, m)
	if err != nil {
		if isUniqueViolation(err) {
			return apperrors.ErrConflict
		}
		return fmt.Errorf("adding member: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `UPDATE communities SET member_count = (SELECT COUNT(*) FROM community_members WHERE community_id = $1) WHERE id = $1`, m.CommunityID)
	return err
}

func (r *pgRepo) RemoveMember(ctx context.Context, communityID, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM community_members WHERE community_id = $1 AND user_id = $2`, communityID, userID)
	if err != nil {
		return fmt.Errorf("removing member: %w", err)
	}
	_, _ = r.db.ExecContext(ctx, `UPDATE communities SET member_count = (SELECT COUNT(*) FROM community_members WHERE community_id = $1) WHERE id = $1`, communityID)
	return nil
}

func (r *pgRepo) UpdateMemberRole(ctx context.Context, communityID, userID uuid.UUID, role string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE community_members SET role = $1 WHERE community_id = $2 AND user_id = $3`, role, communityID, userID)
	return err
}

func (r *pgRepo) GetMembers(ctx context.Context, communityID uuid.UUID) ([]CommunityMember, error) {
	return r.listMembers(ctx, communityID, "")
}

// SearchMembers filters community members by username/display name substring.
func (r *pgRepo) SearchMembers(ctx context.Context, communityID uuid.UUID, search string) ([]CommunityMember, error) {
	return r.listMembers(ctx, communityID, search)
}

func (r *pgRepo) listMembers(ctx context.Context, communityID uuid.UUID, search string) ([]CommunityMember, error) {
	query := `SELECT cm.community_id, cm.user_id, cm.role, cm.joined_at,
		u.username, u.display_name
		FROM community_members cm LEFT JOIN users u ON u.id = cm.user_id
		WHERE cm.community_id = $1`
	args := []interface{}{communityID}
	if search = strings.TrimSpace(search); search != "" {
		args = append(args, "%"+search+"%")
		query += ` AND (u.username ILIKE $2 OR u.display_name ILIKE $2)`
	}
	query += ` ORDER BY cm.joined_at ASC`
	rows, err := r.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting members: %w", err)
	}
	defer rows.Close()
	var members []CommunityMember
	for rows.Next() {
		var m CommunityMember
		var username, displayName sql.NullString
		if err := rows.Scan(&m.CommunityID, &m.UserID, &m.Role, &m.JoinedAt, &username, &displayName); err != nil {
			return nil, err
		}
		if username.Valid {
			m.Username = &username.String
		}
		if displayName.Valid {
			m.DisplayName = &displayName.String
		}
		members = append(members, m)
	}
	return members, nil
}

func (r *pgRepo) IsMember(ctx context.Context, communityID, userID uuid.UUID) (bool, error) {
	var count int
	err := r.db.QueryRowxContext(ctx, `SELECT COUNT(*) FROM community_members WHERE community_id = $1 AND user_id = $2`, communityID, userID).Scan(&count)
	return count > 0, err
}

func (r *pgRepo) GetMemberCount(ctx context.Context, communityID uuid.UUID) (int, error) {
	var count int
	err := r.db.QueryRowxContext(ctx, `SELECT COUNT(*) FROM community_members WHERE community_id = $1`, communityID).Scan(&count)
	return count, err
}

func (r *pgRepo) CreateAnnouncement(ctx context.Context, a *Announcement) error {
	_, err := r.db.NamedExecContext(ctx, `INSERT INTO community_announcements (id, community_id, author_id, content, is_pinned, like_count, created_at, updated_at) VALUES (:id, :community_id, :author_id, :content, :is_pinned, :like_count, :created_at, :updated_at)`, a)
	return err
}

func (r *pgRepo) GetAnnouncements(ctx context.Context, communityID uuid.UUID, pinned bool) ([]Announcement, error) {
	var rows *sqlx.Rows
	var err error
	if pinned {
		rows, err = r.db.QueryxContext(ctx, `SELECT id, community_id, author_id, content, is_pinned, like_count, created_at, updated_at FROM community_announcements WHERE community_id = $1 AND is_pinned = true ORDER BY created_at DESC`, communityID)
	} else {
		rows, err = r.db.QueryxContext(ctx, `SELECT id, community_id, author_id, content, is_pinned, like_count, created_at, updated_at FROM community_announcements WHERE community_id = $1 ORDER BY is_pinned DESC, created_at DESC LIMIT 20`, communityID)
	}
	if err != nil {
		return nil, fmt.Errorf("getting announcements: %w", err)
	}
	defer rows.Close()
	var announcements []Announcement
	for rows.Next() {
		var a Announcement
		if err := rows.Scan(&a.ID, &a.CommunityID, &a.AuthorID, &a.Content, &a.IsPinned, &a.LikeCount, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		announcements = append(announcements, a)
	}
	return announcements, nil
}

func (r *pgRepo) GetAnnouncementByID(ctx context.Context, id uuid.UUID) (*Announcement, error) {
	query := `SELECT id, community_id, author_id, content, is_pinned, like_count, created_at, updated_at FROM community_announcements WHERE id = $1`
	var a Announcement
	err := r.db.GetContext(ctx, &a, query, id)
	if err != nil {
		return nil, fmt.Errorf("getting announcement: %w", err)
	}
	return &a, nil
}

func (r *pgRepo) DeleteAnnouncement(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM community_announcements WHERE id = $1`, id)
	return err
}

func (r *pgRepo) LikeAnnouncement(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE community_announcements SET like_count = like_count + 1 WHERE id = $1`, id)
	return err
}

func (r *pgRepo) SetAnnouncementPin(ctx context.Context, id uuid.UUID, pinned bool) error {
	_, err := r.db.ExecContext(ctx, `UPDATE community_announcements SET is_pinned = $1 WHERE id = $2`, pinned, id)
	return err
}

func (r *pgRepo) RecordActivity(ctx context.Context, e *ActivityEvent) error {
	_, err := r.db.NamedExecContext(ctx, `INSERT INTO community_activity_events (id, community_id, event_type, actor_id, target_id, metadata, created_at) VALUES (:id, :community_id, :event_type, :actor_id, :target_id, :metadata, :created_at)`, e)
	return err
}

func (r *pgRepo) GetActivity(ctx context.Context, communityID uuid.UUID, limit int) ([]ActivityEvent, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := r.db.QueryxContext(ctx, `SELECT id, community_id, event_type, actor_id, target_id, metadata, created_at FROM community_activity_events WHERE community_id = $1 ORDER BY created_at DESC LIMIT $2`, communityID, limit)
	if err != nil {
		return nil, fmt.Errorf("getting activity: %w", err)
	}
	defer rows.Close()
	var events []ActivityEvent
	for rows.Next() {
		var e ActivityEvent
		if err := rows.Scan(&e.ID, &e.CommunityID, &e.EventType, &e.ActorID, &e.TargetID, &e.Metadata, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

func (r *pgRepo) UpdateTotalSaved(ctx context.Context, communityID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE communities SET total_saved = COALESCE((SELECT SUM(c.total_contributions) FROM circles c WHERE c.deleted_at IS NULL AND c.community_id = $1), 0) WHERE id = $1`, communityID)
	return err
}

func (r *pgRepo) FindByUserID(ctx context.Context, userID uuid.UUID) ([]Community, error) {
	rows, err := r.db.QueryxContext(ctx, `SELECT c.id, c.name, c.slug, c.description, c.category, c.tags, c.avatar_url, c.banner_url, c.owner_id, c.member_count, c.total_saved, c.is_featured, c.created_at, c.updated_at, c.suspended_at, c.suspended_reason FROM communities c INNER JOIN community_members cm ON cm.community_id = c.id WHERE c.deleted_at IS NULL AND cm.user_id = $1 ORDER BY c.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("finding communities by user: %w", err)
	}
	defer rows.Close()
	var communities []Community
	for rows.Next() {
		c, err := scanCommunity(rows)
		if err != nil {
			return nil, err
		}
		communities = append(communities, *c)
	}
	return communities, nil
}

func (r *pgRepo) UpdateOwner(ctx context.Context, communityID, newOwnerID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `UPDATE communities SET owner_id = $1 WHERE id = $2`, newOwnerID, communityID)
	return err
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if pqErr, ok := err.(*pq.Error); ok {
		return pqErr.Code == pq.ErrorCode("23505")
	}
	return false
}
