package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/deodes/backend/internal/api/handler"
	"github.com/deodes/backend/internal/domain/circle"
	"github.com/deodes/backend/internal/domain/payout"
)

// gateCircleStub serves the circle lookups ExitCircle and TriggerPayout need;
// unused circle.Service methods return zero values.
type gateCircleStub struct {
	circle.Service
	circ       *circle.Circle
	members    []circle.CircleMember
	isMember   bool
	exitCalled bool
	exitErr    error
}

func (s *gateCircleStub) Get(context.Context, string) (*circle.Circle, error) {
	return s.circ, nil
}

func (s *gateCircleStub) GetMembers(context.Context, string) ([]circle.CircleMember, error) {
	return s.members, nil
}

func (s *gateCircleStub) SearchMembers(_ context.Context, _ string, search string) ([]circle.CircleMember, error) {
	if search == "" {
		return s.members, nil
	}
	var out []circle.CircleMember
	for _, m := range s.members {
		if m.Username != nil && strings.Contains(strings.ToLower(*m.Username), strings.ToLower(search)) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *gateCircleStub) IsMember(context.Context, string, string) (bool, error) {
	return s.isMember, nil
}

func (s *gateCircleStub) Exit(context.Context, string, string) error {
	s.exitCalled = true
	return s.exitErr
}

func (s *gateCircleStub) List(context.Context, circle.CircleFilter) ([]circle.Circle, int, error) {
	return nil, 0, nil
}

func (s *gateCircleStub) Create(context.Context, string, circle.CreateCircleInput) (*circle.Circle, error) {
	return nil, nil
}

func (s *gateCircleStub) Update(context.Context, string, string, circle.UpdateCircleInput) (*circle.Circle, error) {
	return nil, nil
}

func (s *gateCircleStub) Start(context.Context, string, string) error { return nil }

func (s *gateCircleStub) Close(context.Context, string, string) error { return nil }

func (s *gateCircleStub) Cancel(context.Context, string, string) error { return nil }

func (s *gateCircleStub) Join(context.Context, string, string, string) error { return nil }

func (s *gateCircleStub) RemoveMember(context.Context, string, string, string, string) error {
	return nil
}

func (s *gateCircleStub) ProcessMissedContributions(context.Context, string, int) error {
	return nil
}

func (s *gateCircleStub) RaiseDispute(context.Context, string, string, circle.DisputeInput) (*circle.CircleDispute, error) {
	return nil, nil
}

func (s *gateCircleStub) CastVote(context.Context, string, string, circle.VoteInput) (*circle.CircleVote, bool, string, error) {
	return nil, false, "", nil
}

func (s *gateCircleStub) SubmitAuctionBid(context.Context, string, string, circle.AuctionBidInput) (*circle.CircleAuctionBid, error) {
	return nil, nil
}

func (s *gateCircleStub) QueryRoundConfig(context.Context, string, int) (*circle.RoundConfigSnapshot, error) {
	return nil, nil
}

func (s *gateCircleStub) GetCircleSnapshot(context.Context, string, string) (*circle.CircleSnapshot, error) {
	return nil, nil
}

func (s *gateCircleStub) GetBulkCircleSnapshots(context.Context, string, []string) ([]circle.CircleSnapshot, error) {
	return nil, nil
}

// gatePayoutStub serves the payout lookups the gate and rotation guards need.
type gatePayoutStub struct {
	history     []payout.Payout
	round       *payout.Payout
	recordErr   error
	recordInput *payout.RecordInput
}

func (s *gatePayoutStub) Record(_ context.Context, input payout.RecordInput) (*payout.Payout, error) {
	s.recordInput = &input
	if s.recordErr != nil {
		return nil, s.recordErr
	}
	recipient := uuid.MustParse(input.RecipientID)
	return &payout.Payout{
		ID: uuid.New(), CircleID: uuid.MustParse(input.CircleID),
		RecipientID: recipient, RoundNumber: input.RoundNumber,
		Amount: input.Amount, PayoutType: input.PayoutType,
	}, nil
}

func (s *gatePayoutStub) GetUserHistory(context.Context, string, int, int) ([]payout.Payout, int, error) {
	return nil, 0, nil
}

func (s *gatePayoutStub) GetCircleHistory(_ context.Context, _ string, _, _ int) ([]payout.Payout, int, error) {
	return s.history, len(s.history), nil
}

func (s *gatePayoutStub) GetRoundPayout(_ context.Context, _ string, _ int) (*payout.Payout, error) {
	return s.round, nil
}

func (s *gatePayoutStub) UpdateVerification(context.Context, string, bool, payout.VerificationStatus) error {
	return nil
}

func gateRouter(circleSvc *gateCircleStub, payoutSvc *gatePayoutStub) *gin.Engine {
	h := handler.NewCircleHandler(circleSvc, nil, nil, payoutSvc)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", c.GetHeader("X-Test-User"))
		c.Next()
	})
	r.POST("/circles/:id/exit", h.ExitCircle)
	r.POST("/circles/:id/payout", h.TriggerPayout)
	return r
}

func gateExitRequest(t *testing.T, r *gin.Engine, circleID, userID string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/circles/"+circleID+"/exit", nil)
	req.Header.Set("X-Test-User", userID)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func gateMembers(circleID uuid.UUID, users ...uuid.UUID) []circle.CircleMember {
	members := make([]circle.CircleMember, len(users))
	for i, u := range users {
		members[i] = circle.CircleMember{CircleID: circleID, UserID: u, Position: i + 1, Status: circle.MemberStatusActive}
	}
	return members
}

func gatePayout(circleID, recipient uuid.UUID, round int) payout.Payout {
	return payout.Payout{
		ID: uuid.New(), CircleID: circleID, RecipientID: recipient,
		RoundNumber: round, Amount: 300, PayoutType: payout.PayoutTypeRandom,
	}
}

func TestExitCircle_BlockedUntilEveryMemberPaid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	circleID := uuid.New()
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	organizer := uuid.New()
	circleSvc := &gateCircleStub{
		circ: &circle.Circle{
			ID: circleID, Name: "Gated", Status: circle.CircleStatusActive,
			PayoutType: circle.PayoutTypeRandom, CurrentRound: 2,
			MaxMembers: 4, OrganizerID: organizer,
		},
		members: gateMembers(circleID, organizer, a, b, c),
	}
	// Only 2 of the 3 non-organizer members paid (organizer not yet paid
	// either — 2 of 4 active members paid).
	payoutSvc := &gatePayoutStub{history: []payout.Payout{
		gatePayout(circleID, a, 1),
		gatePayout(circleID, b, 2),
	}}

	code, body := gateExitRequest(t, gateRouter(circleSvc, payoutSvc), circleID.String(), c.String())

	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, body, "PAYOUTS_INCOMPLETE")
	assert.Contains(t, body, "2 of 4")
	assert.False(t, circleSvc.exitCalled, "gated exit must never reach the service")
}

func TestExitCircle_AllowedWhenEveryMemberPaid(t *testing.T) {
	gin.SetMode(gin.TestMode)
	circleID := uuid.New()
	a, b := uuid.New(), uuid.New()
	circleSvc := &gateCircleStub{
		circ: &circle.Circle{
			ID: circleID, Name: "Complete", Status: circle.CircleStatusActive,
			PayoutType: circle.PayoutTypeRandom, CurrentRound: 2,
			MaxMembers: 2, OrganizerID: a,
		},
		members: gateMembers(circleID, a, b),
	}
	payoutSvc := &gatePayoutStub{history: []payout.Payout{
		gatePayout(circleID, a, 1),
		gatePayout(circleID, b, 2),
	}}

	code, body := gateExitRequest(t, gateRouter(circleSvc, payoutSvc), circleID.String(), b.String())

	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"success":true`)
	assert.True(t, circleSvc.exitCalled)
}

func TestExitCircle_PendingCircleNotGated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	circleID := uuid.New()
	a := uuid.New()
	circleSvc := &gateCircleStub{
		circ: &circle.Circle{
			ID: circleID, Name: "Pending", Status: circle.CircleStatusPending,
			PayoutType: circle.PayoutTypeRandom, OrganizerID: uuid.New(),
		},
		members: gateMembers(circleID, a),
	}
	payoutSvc := &gatePayoutStub{}

	code, _ := gateExitRequest(t, gateRouter(circleSvc, payoutSvc), circleID.String(), a.String())

	assert.Equal(t, http.StatusOK, code)
	assert.True(t, circleSvc.exitCalled, "pending circles have no rounds — exit stays open")
}

func TestTriggerPayout_RandomRepeatRecipientConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	circleID := uuid.New()
	organizer, recipient := uuid.New(), uuid.New()
	circleSvc := &gateCircleStub{
		circ: &circle.Circle{
			ID: circleID, Name: "Random", Status: circle.CircleStatusActive,
			PayoutType: circle.PayoutTypeRandom, CurrentRound: 2,
			MaxMembers: 3, OrganizerID: organizer,
		},
		isMember: true,
	}
	payoutSvc := &gatePayoutStub{history: []payout.Payout{
		gatePayout(circleID, recipient, 1),
	}}

	r := gateRouter(circleSvc, payoutSvc)
	body := `{"recipientId":"` + recipient.String() + `","roundNumber":2,"amount":300,"feeAmount":1.5,` +
		`"txnHash":"0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef","payoutType":"random"}`
	req := httptest.NewRequest(http.MethodPost, "/circles/"+circleID.String()+"/payout", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", organizer.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "already received a payout in this rotation")
	assert.Nil(t, payoutSvc.recordInput, "repeat must never reach Record")
}

func TestTriggerPayout_RandomUnpaidRecipientAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	circleID := uuid.New()
	organizer, paid, fresh := uuid.New(), uuid.New(), uuid.New()
	circleSvc := &gateCircleStub{
		circ: &circle.Circle{
			ID: circleID, Name: "Random", Status: circle.CircleStatusActive,
			PayoutType: circle.PayoutTypeRandom, CurrentRound: 2,
			MaxMembers: 3, OrganizerID: organizer,
		},
		isMember: true,
	}
	payoutSvc := &gatePayoutStub{history: []payout.Payout{
		gatePayout(circleID, paid, 1),
	}}

	r := gateRouter(circleSvc, payoutSvc)
	body := `{"recipientId":"` + fresh.String() + `","roundNumber":2,"amount":300,"feeAmount":1.5,` +
		`"txnHash":"0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef","payoutType":"random"}`
	req := httptest.NewRequest(http.MethodPost, "/circles/"+circleID.String()+"/payout", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", organizer.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	require.NotNil(t, payoutSvc.recordInput)
	assert.Equal(t, fresh.String(), payoutSvc.recordInput.RecipientID)
}

func TestTriggerPayout_VerificationFailureIs422(t *testing.T) {
	gin.SetMode(gin.TestMode)
	circleID := uuid.New()
	organizer, fresh := uuid.New(), uuid.New()
	circleSvc := &gateCircleStub{
		circ: &circle.Circle{
			ID: circleID, Name: "Random", Status: circle.CircleStatusActive,
			PayoutType: circle.PayoutTypeFixed, CurrentRound: 2,
			MaxMembers: 3, OrganizerID: organizer,
		},
		isMember: true,
	}
	payoutSvc := &gatePayoutStub{
		recordErr: errors.New("on-chain verification failed: transaction 0xdead reverted on-chain (status 0)"),
	}

	r := gateRouter(circleSvc, payoutSvc)
	body := `{"recipientId":"` + fresh.String() + `","roundNumber":2,"amount":300,"feeAmount":1.5,` +
		`"txnHash":"0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef","payoutType":"fixed"}`
	req := httptest.NewRequest(http.MethodPost, "/circles/"+circleID.String()+"/payout", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", organizer.String())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "ONCHAIN_VERIFICATION_FAILED")
}
