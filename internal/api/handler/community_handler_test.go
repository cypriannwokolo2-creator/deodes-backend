package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/community"
	communityMocks "github.com/deodes/backend/internal/domain/community/mocks"
	"github.com/deodes/backend/pkg/apperrors"
	"github.com/deodes/backend/pkg/validator"
)

func init() {
	validator.Init()
}

func setupCommunityRouter(h *handler.CommunityHandler, userID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", userID); c.Next() })
	r.POST("/communities", h.Create)
	r.GET("/communities", h.List)
	r.GET("/communities/:id", h.Get)
	r.PATCH("/communities/:id", h.Update)
	r.DELETE("/communities/:id", h.Delete)
	r.POST("/communities/:id/join", h.Join)
	r.POST("/communities/:id/leave", h.Leave)
	r.GET("/communities/:id/members", h.GetMembers)
	r.GET("/communities/:id/membership", h.IsMember)
	r.POST("/communities/:id/announcements", h.CreateAnnouncement)
	r.POST("/communities/:id/announcements/:announcementId/like", h.LikeAnnouncement)
	r.PATCH("/communities/:id/announcements/:announcementId/pin", h.PinAnnouncement)
	r.DELETE("/communities/:id/members/:memberId", h.RemoveMember)
	r.POST("/communities/:id/transfer-ownership", h.TransferOwnership)
	return r
}

func TestCommunityHandler_Create_Valid(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	uid := uuid.New()

	repo.On("Create", mock.Anything, mock.AnythingOfType("*community.Community")).Return(nil)
	repo.On("AddMember", mock.Anything, mock.AnythingOfType("*community.CommunityMember")).Return(nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, uid.String())

	body, _ := json.Marshal(map[string]interface{}{
		"name": "Savings Group", "slug": "savings", "category": "finance",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/communities", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Body.String(), "Savings Group")
	repo.AssertExpectations(t)
}

func TestCommunityHandler_Create_InvalidCategory(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, uuid.New().String())

	body, _ := json.Marshal(map[string]interface{}{
		"name": "X", "slug": "x", "category": "invalid-cat",
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/communities", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

func TestCommunityHandler_Get(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	c := &community.Community{ID: uuid.New(), Name: "My Community"}

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, uuid.New().String())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/communities/"+c.ID.String(), nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "My Community")
	repo.AssertExpectations(t)
}

func TestCommunityHandler_Get_NotFound(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	id := uuid.New()

	repo.On("FindByID", mock.Anything, id).Return(nil, apperrors.ErrNotFound)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, uuid.New().String())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/communities/"+id.String(), nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	repo.AssertExpectations(t)
}

func TestCommunityHandler_Join(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	cid, uid := uuid.New(), uuid.New()

	repo.On("FindByID", mock.Anything, cid).Return(&community.Community{ID: cid}, nil)
	repo.On("IsMember", mock.Anything, cid, uid).Return(false, nil)
	repo.On("AddMember", mock.Anything, mock.AnythingOfType("*community.CommunityMember")).Return(nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, uid.String())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/communities/"+cid.String()+"/join", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	repo.AssertExpectations(t)
}

func TestCommunityHandler_Update_Owner(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	uid := uuid.New()
	c := &community.Community{ID: uuid.New(), Name: "Old", OwnerID: uid}

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)
	repo.On("Update", mock.Anything, mock.AnythingOfType("*community.Community")).Return(nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, uid.String())

	body, _ := json.Marshal(map[string]interface{}{"name": "New Name"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/communities/"+c.ID.String(), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "New Name")
	repo.AssertExpectations(t)
}

func TestCommunityHandler_Update_NonOwner_BadRequest(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	c := &community.Community{ID: uuid.New(), Name: "Old", OwnerID: uuid.New()}

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, uuid.New().String())

	body, _ := json.Marshal(map[string]interface{}{"name": "New Name"})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/communities/"+c.ID.String(), bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	repo.AssertExpectations(t)
}

func TestCommunityHandler_RemoveMember_Owner(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	ownerID := uuid.New()
	c := &community.Community{ID: uuid.New(), OwnerID: ownerID}
	targetID := uuid.New()

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)
	repo.On("RemoveMember", mock.Anything, c.ID, targetID).Return(nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, ownerID.String())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/communities/"+c.ID.String()+"/members/"+targetID.String(), nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	repo.AssertExpectations(t)
}

func TestCommunityHandler_RemoveMember_NonOwner_BadRequest(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	c := &community.Community{ID: uuid.New(), OwnerID: uuid.New()}

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, uuid.New().String())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/communities/"+c.ID.String()+"/members/"+uuid.New().String(), nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	repo.AssertExpectations(t)
}

func TestCommunityHandler_LikeAnnouncement(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	annID := uuid.New()
	// Expect membership check and like
	ann := &community.Announcement{ID: annID, CommunityID: uuid.New(), AuthorID: uuid.New()}
	repo.On("GetAnnouncementByID", mock.Anything, annID).Return(ann, nil)
	repo.On("IsMember", mock.Anything, ann.CommunityID, mock.Anything).Return(true, nil)
	repo.On("LikeAnnouncement", mock.Anything, annID).Return(nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, uuid.New().String())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/communities/"+uuid.New().String()+"/announcements/"+annID.String()+"/like", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	repo.AssertExpectations(t)
}

func TestCommunityHandler_PinAnnouncement(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	annID := uuid.New()
	communityID := uuid.New()
	ownerID := uuid.New()

	ann := &community.Announcement{ID: annID, CommunityID: communityID, AuthorID: uuid.New()}
	comm := &community.Community{ID: communityID, OwnerID: ownerID}
	repo.On("GetAnnouncementByID", mock.Anything, annID).Return(ann, nil)
	repo.On("FindByID", mock.Anything, communityID).Return(comm, nil)
	repo.On("SetAnnouncementPin", mock.Anything, annID, true).Return(nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, ownerID.String())

	body, _ := json.Marshal(map[string]interface{}{"pinned": true})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("PATCH", "/communities/"+communityID.String()+"/announcements/"+annID.String()+"/pin", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	repo.AssertExpectations(t)
}

func TestCommunityHandler_TransferOwnership(t *testing.T) {
	repo := new(communityMocks.Repository)
	svc := community.NewService(repo, nil)
	ownerID := uuid.New()
	c := &community.Community{ID: uuid.New(), OwnerID: ownerID}
	newOwner := uuid.New()

	repo.On("FindByID", mock.Anything, c.ID).Return(c, nil)
	repo.On("IsMember", mock.Anything, c.ID, newOwner).Return(true, nil)
	repo.On("UpdateOwner", mock.Anything, c.ID, newOwner).Return(nil)
	repo.On("UpdateMemberRole", mock.Anything, c.ID, ownerID, "member").Return(nil)

	h := handler.NewCommunityHandler(svc)
	r := setupCommunityRouter(h, ownerID.String())

	body, _ := json.Marshal(map[string]interface{}{"newOwnerId": newOwner.String()})
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/communities/"+c.ID.String()+"/transfer-ownership", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	repo.AssertExpectations(t)
}
