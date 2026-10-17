package circle_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/deodes/backend/internal/domain/circle"
	circleMocks "github.com/deodes/backend/internal/domain/circle/mocks"
	"github.com/deodes/backend/pkg/apperrors"
)

func validCreateInput() circle.CreateCircleInput {
	return circle.CreateCircleInput{
		Name:               "Test Circle",
		Description:        "A valid description",
		CircleType:         circle.CircleTypePublic,
		PayoutType:         circle.PayoutTypeRandom,
		ContributionAmount: 100,
		Currency:           circle.CurrencyUSDC,
		Frequency:          circle.FrequencyWeekly,
		MaxMembers:         10,
		MinMoiScore:        0,
		CollateralPercent:  10,
		LateFeePercent:     5,
		GracePeriodHours:   24,
		MaxStrikes:         3,
	}
}

func futureStartDate() string {
	return time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
}

func pastStartDate() string {
	return time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
}

func TestCircleService_Create_Validation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*circle.CreateCircleInput)
	}{
		{"empty name", func(i *circle.CreateCircleInput) { i.Name = "" }},
		{"whitespace name", func(i *circle.CreateCircleInput) { i.Name = "   " }},
		{"short name", func(i *circle.CreateCircleInput) { i.Name = "AB" }},
		{"long name", func(i *circle.CreateCircleInput) { i.Name = strings.Repeat("a", 101) }},
		{"long description", func(i *circle.CreateCircleInput) { i.Description = strings.Repeat("a", 501) }},
		{"zero contribution", func(i *circle.CreateCircleInput) { i.ContributionAmount = 0 }},
		{"negative contribution", func(i *circle.CreateCircleInput) { i.ContributionAmount = -5 }},
		{"over-max contribution", func(i *circle.CreateCircleInput) { i.ContributionAmount = 1_000_001 }},
		{"non-USDC currency rejected", func(i *circle.CreateCircleInput) { i.Currency = circle.CircleCurrency("XLM") }},
		{"bad frequency", func(i *circle.CreateCircleInput) { i.Frequency = "yearly" }},
		{"bad circle type", func(i *circle.CreateCircleInput) { i.CircleType = "secret" }},
		{"bad payout type", func(i *circle.CreateCircleInput) { i.PayoutType = "lottery" }},
		{"one member", func(i *circle.CreateCircleInput) { i.MaxMembers = 1 }},
		{"too many members", func(i *circle.CreateCircleInput) { i.MaxMembers = 101 }},
		{"negative MoiScore", func(i *circle.CreateCircleInput) { i.MinMoiScore = -1 }},
		{"MoiScore over max", func(i *circle.CreateCircleInput) { i.MinMoiScore = 1001 }},
		{"negative collateral", func(i *circle.CreateCircleInput) { i.CollateralPercent = -1 }},
		{"collateral over max", func(i *circle.CreateCircleInput) { i.CollateralPercent = 101 }},
		{"late fee over max", func(i *circle.CreateCircleInput) { i.LateFeePercent = 51 }},
		{"negative late fee", func(i *circle.CreateCircleInput) { i.LateFeePercent = -1 }},
		// NOTE: GracePeriodHours/MaxStrikes arrive as 0 when omitted, and
		// Create defaults them (24/3) like the wizard — so only over-max is
		// testable here; explicit 0 is rejected on the Update path instead.
		{"grace period over max", func(i *circle.CreateCircleInput) { i.GracePeriodHours = 169 }},
		{"strikes over max", func(i *circle.CreateCircleInput) { i.MaxStrikes = 11 }},
		{"past start date", func(i *circle.CreateCircleInput) { i.StartDate = pastStartDate() }},
		{"garbage start date", func(i *circle.CreateCircleInput) { i.StartDate = "next friday" }},
		{"bad community id", func(i *circle.CreateCircleInput) { i.CommunityID = "not-a-uuid" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := new(circleMocks.Repository)
			svc := circle.NewService(repo, nil, circle.Dependencies{})
			input := validCreateInput()
			tc.mutate(&input)

			c, err := svc.Create(context.Background(), uuid.New().String(), input)

			assert.Error(t, err)
			assert.ErrorIs(t, err, apperrors.ErrInvalidInput)
			assert.Nil(t, c)
			repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
		})
	}
}

func TestCircleService_Create_AcceptsFutureStartDate(t *testing.T) {
	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	ctx := context.Background()

	var captured *circle.Circle
	repo.On("Create", ctx, mock.AnythingOfType("*circle.Circle")).Return(nil).Run(func(args mock.Arguments) {
		captured = args.Get(1).(*circle.Circle)
	})
	repo.On("CreateMember", ctx, mock.AnythingOfType("*circle.CircleMember")).Return(nil)

	input := validCreateInput()
	input.StartDate = futureStartDate()
	c, err := svc.Create(ctx, uuid.New().String(), input)

	assert.NoError(t, err)
	assert.NotNil(t, c)
	assert.True(t, captured.StartDate.Valid, "future start date must persist")
	repo.AssertExpectations(t)
}

func TestCircleService_Create_PremiumFloor_IsValidationError(t *testing.T) {
	repo := new(circleMocks.Repository)
	userRepo := new(mockUserRepo)
	svc := circle.NewService(repo, userRepo, circle.Dependencies{})
	userRepo.score = 500

	input := validCreateInput()
	input.CircleType = circle.CircleTypePremium
	input.ContributionAmount = 10

	c, err := svc.Create(context.Background(), uuid.New().String(), input)

	assert.Error(t, err)
	assert.ErrorIs(t, err, apperrors.ErrInvalidInput)
	assert.Nil(t, c)
	repo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
}

func TestCircleService_Update_Validation(t *testing.T) {
	orgID := uuid.New()
	cid := uuid.New()
	stored := &circle.Circle{
		ID: cid, Name: "Stored", Status: circle.CircleStatusPending,
		OrganizerID: orgID, MaxMembers: 10,
	}

	shortName := "AB"
	longName := strings.Repeat("a", 101)
	zero := 0.0
	overMax := 1_000_001.0
	badCurrency := circle.CircleCurrency("XLM")
	badFreq := circle.CircleFrequency("yearly")
	oneMember := 1
	manyMembers := 101
	badMoi := 1001
	badCollateral := 101.0
	badLateFee := 51.0
	badGrace := 0
	badStrikes := 11
	past := pastStartDate()
	garbage := "whenever"

	cases := []struct {
		name  string
		input circle.UpdateCircleInput
	}{
		{"short name", circle.UpdateCircleInput{Name: &shortName}},
		{"long name", circle.UpdateCircleInput{Name: &longName}},
		{"zero contribution", circle.UpdateCircleInput{ContributionAmount: &zero}},
		{"over-max contribution", circle.UpdateCircleInput{ContributionAmount: &overMax}},
		{"non-USDC currency", circle.UpdateCircleInput{Currency: &badCurrency}},
		{"bad frequency", circle.UpdateCircleInput{Frequency: &badFreq}},
		{"one member", circle.UpdateCircleInput{MaxMembers: &oneMember}},
		{"too many members", circle.UpdateCircleInput{MaxMembers: &manyMembers}},
		{"MoiScore over max", circle.UpdateCircleInput{MinMoiScore: &badMoi}},
		{"collateral over max", circle.UpdateCircleInput{CollateralPercent: &badCollateral}},
		{"late fee over max", circle.UpdateCircleInput{LateFeePercent: &badLateFee}},
		{"zero grace", circle.UpdateCircleInput{GracePeriodHours: &badGrace}},
		{"strikes over max", circle.UpdateCircleInput{MaxStrikes: &badStrikes}},
		{"past start date", circle.UpdateCircleInput{StartDate: &past}},
		{"garbage start date", circle.UpdateCircleInput{StartDate: &garbage}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := new(circleMocks.Repository)
			svc := circle.NewService(repo, nil, circle.Dependencies{})
			repo.On("FindByID", mock.Anything, cid).Return(stored, nil)

			updated, err := svc.Update(context.Background(), cid.String(), orgID.String(), tc.input)

			assert.Error(t, err)
			assert.ErrorIs(t, err, apperrors.ErrInvalidInput)
			assert.Nil(t, updated)
			repo.AssertNotCalled(t, "Update", mock.Anything, mock.Anything)
		})
	}
}

func TestCircleService_Update_AcceptsValidBounds(t *testing.T) {
	orgID := uuid.New()
	cid := uuid.New()
	stored := &circle.Circle{
		ID: cid, Name: "Stored", Status: circle.CircleStatusPending,
		OrganizerID: orgID, MaxMembers: 10,
	}

	repo := new(circleMocks.Repository)
	svc := circle.NewService(repo, nil, circle.Dependencies{})
	repo.On("FindByID", mock.Anything, cid).Return(stored, nil)
	repo.On("Update", mock.Anything, mock.AnythingOfType("*circle.Circle")).Return(nil)

	name := "  Renamed Circle  "
	desc := "  Fresh description  "
	members := 50
	moi := 1000
	collateral := 100.0
	lateFee := 50.0
	grace := 168
	strikes := 10
	start := futureStartDate()

	updated, err := svc.Update(context.Background(), cid.String(), orgID.String(), circle.UpdateCircleInput{
		Name: &name, Description: &desc, MaxMembers: &members,
		MinMoiScore: &moi, CollateralPercent: &collateral,
		LateFeePercent: &lateFee, GracePeriodHours: &grace,
		MaxStrikes: &strikes, StartDate: &start,
	})

	assert.NoError(t, err)
	assert.Equal(t, "Renamed Circle", updated.Name)
	assert.Equal(t, sql.NullString{String: "Fresh description", Valid: true}, updated.Description)
	assert.True(t, updated.StartDate.Valid)
	repo.AssertExpectations(t)
}

// mockUserRepo satisfies circle.UserMOIFetcher for premium-gate tests.
type mockUserRepo struct {
	score int
}

func (m *mockUserRepo) FindByID(_ context.Context, _ uuid.UUID) (*circle.UserMOIData, error) {
	return &circle.UserMOIData{MoiScore: m.score}, nil
}
