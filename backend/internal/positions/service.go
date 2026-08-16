// Package positions manages manually recorded user positions. The application
// never opens or closes anything by itself: every fill here was entered by the
// user after they traded on the exchange themselves.
package positions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/logging"
	"github.com/crypto-market-advisor/advisor/internal/pnl"
	"github.com/crypto-market-advisor/advisor/internal/repository"
)

// Errors returned by the service.
var (
	ErrPositionClosed = errors.New("position is already closed")
	ErrNoRemaining    = errors.New("position has no remaining quantity")
	ErrUnknownAsset   = errors.New("unknown asset")
)

// Service owns position lifecycle and accounting.
type Service struct {
	repos *repository.Repositories
	fees  config.FeesConfig
	log   *slog.Logger
}

// NewService builds the positions service.
func NewService(repos *repository.Repositories, fees config.FeesConfig, logger *slog.Logger) *Service {
	return &Service{repos: repos, fees: fees, log: logging.For(logger, logging.CategoryPosition)}
}

// SetFees updates the fee profile at runtime (from the settings screen).
func (s *Service) SetFees(fees config.FeesConfig) { s.fees = fees }

// Fees exposes the active fee profile.
func (s *Service) Fees() config.FeesConfig { return s.fees }

// OpenRequest is what the user enters after opening a trade manually.
type OpenRequest struct {
	Symbol           string
	Direction        domain.Direction
	EntryPrice       decimal.Decimal
	Leverage         decimal.Decimal
	OpenedAt         time.Time
	Quantity         *decimal.Decimal
	Notional         *decimal.Decimal
	Margin           *decimal.Decimal
	FeeType          domain.FeeType
	ActualFee        *decimal.Decimal
	RecommendationID *uuid.UUID
	TakeProfit       []domain.PriceTarget
	StopLoss         []domain.PriceTarget
	Note             string
}

// Open records a new position together with its opening fill.
func (s *Service) Open(ctx context.Context, req OpenRequest) (domain.Position, error) {
	asset, err := s.repos.Assets.GetBySymbol(ctx, req.Symbol)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return domain.Position{}, fmt.Errorf("%w: %s", ErrUnknownAsset, req.Symbol)
		}
		return domain.Position{}, err
	}
	if req.FeeType == "" {
		req.FeeType = domain.FeeTaker
	}
	if req.OpenedAt.IsZero() {
		req.OpenedAt = time.Now().UTC()
	}

	quantity, notional, margin, sizeKnown := pnl.DeriveSize(req.EntryPrice, req.Leverage, req.Quantity, req.Notional, req.Margin)

	now := time.Now().UTC()
	plan := &domain.TradePlan{TakeProfit: req.TakeProfit, StopLoss: req.StopLoss, UpdatedAt: now}
	planCopy := *plan

	position := domain.Position{
		ID:               uuid.New(),
		AssetID:          asset.ID,
		Symbol:           asset.Symbol,
		Direction:        req.Direction,
		Status:           domain.PositionOpen,
		EntryPrice:       req.EntryPrice,
		Leverage:         req.Leverage,
		SizeKnown:        sizeKnown,
		OpenedAt:         req.OpenedAt.UTC(),
		RecommendationID: req.RecommendationID,
		FeeType:          req.FeeType,
		OriginalPlan:     &planCopy,
		CurrentPlan:      plan,
		Note:             req.Note,
	}
	if sizeKnown {
		position.InitialQuantity = &quantity
		position.RemainingQuantity = &quantity
		position.InitialNotional = &notional
		position.InitialMargin = &margin
	}

	fee, estimated := pnl.OpenFee(req.EntryPrice, quantity, s.fees.MakerPct, s.fees.TakerPct, req.FeeType, req.ActualFee, s.fees.Configured)
	fill := domain.Fill{
		ID:           uuid.New(),
		PositionID:   position.ID,
		Kind:         domain.FillOpen,
		Price:        req.EntryPrice,
		Fee:          fee,
		FeeType:      req.FeeType,
		FeeEstimated: estimated,
		ExecutedAt:   position.OpenedAt,
		Note:         "position opened",
	}
	if sizeKnown {
		fill.Quantity = &quantity
	}

	if err := s.repos.Positions.Create(ctx, position, fill); err != nil {
		return domain.Position{}, fmt.Errorf("create position: %w", err)
	}

	if req.RecommendationID != nil {
		decision := domain.Decision{
			RecommendationID: *req.RecommendationID,
			Decision:         domain.DecisionOpened,
			LinkedPositionID: &position.ID,
			DecidedAt:        now,
		}
		if err := s.repos.Recommendations.SetDecision(ctx, decision); err != nil {
			s.log.Warn("link recommendation failed", slog.String("error", err.Error()))
		}
	}

	s.log.Info("position opened",
		slog.String("symbol", position.Symbol),
		slog.String("direction", string(position.Direction)),
		slog.Bool("size_known", sizeKnown))
	return position, nil
}

// CloseRequest is a manual (partial or full) close.
type CloseRequest struct {
	PositionID     uuid.UUID
	ExecutionPrice decimal.Decimal
	ClosePct       *decimal.Decimal
	Quantity       *decimal.Decimal
	FeeType        domain.FeeType
	ActualFee      *decimal.Decimal
	ExecutedAt     time.Time
	Full           bool
	Note           string
}

// Close applies a (partial) close. The fill is appended; nothing is rewritten.
func (s *Service) Close(ctx context.Context, req CloseRequest) (domain.PositionView, error) {
	position, err := s.repos.Positions.Get(ctx, req.PositionID)
	if err != nil {
		return domain.PositionView{}, err
	}
	if position.Status == domain.PositionClosed {
		return domain.PositionView{}, ErrPositionClosed
	}
	if req.FeeType == "" {
		req.FeeType = position.FeeType
	}
	if req.ExecutedAt.IsZero() {
		req.ExecutedAt = time.Now().UTC()
	}
	if req.Full {
		full := decimal.NewFromInt(100)
		req.ClosePct = &full
		req.Quantity = nil
	}
	if !position.SizeKnown && req.Quantity == nil && req.ClosePct == nil {
		full := decimal.NewFromInt(100)
		req.ClosePct = &full
	}

	result, err := pnl.ComputeClose(pnl.CloseRequest{
		Position:       position,
		ExecutionPrice: req.ExecutionPrice,
		ClosePct:       req.ClosePct,
		Quantity:       req.Quantity,
		FeeType:        req.FeeType,
		ActualFee:      req.ActualFee,
		MakerFeePct:    s.fees.MakerPct,
		TakerFeePct:    s.fees.TakerPct,
		FeesConfigured: s.fees.Configured,
	})
	if err != nil {
		return domain.PositionView{}, err
	}

	fill := domain.Fill{
		ID:           uuid.New(),
		PositionID:   position.ID,
		Kind:         domain.FillClose,
		Price:        req.ExecutionPrice,
		Fee:          result.Fee,
		FeeType:      req.FeeType,
		FeeEstimated: result.FeeEstimated,
		RealizedPnL:  result.RealizedPnL,
		ExecutedAt:   req.ExecutedAt.UTC(),
		Note:         req.Note,
	}
	if position.SizeKnown {
		q := result.Quantity
		fill.Quantity = &q
	}
	pct := result.ClosePct
	fill.ClosePct = &pct

	updated := position
	eventType := domain.EventPartialClose

	fullyClosed := req.Full || result.FullyClosed
	if position.SizeKnown {
		remaining := result.Remaining
		updated.RemainingQuantity = &remaining
		fullyClosed = req.Full || remaining.IsZero()
	}
	if fullyClosed {
		updated.Status = domain.PositionClosed
		closedAt := req.ExecutedAt.UTC()
		updated.ClosedAt = &closedAt
		eventType = domain.EventFullClose
		if position.SizeKnown {
			zero := decimal.Zero
			updated.RemainingQuantity = &zero
		}
	} else {
		updated.Status = domain.PositionPartiallyClosed
	}

	payload := map[string]any{
		"price":        req.ExecutionPrice.String(),
		"close_pct":    result.ClosePct.String(),
		"realized_pnl": result.RealizedPnL.String(),
		"fee":          result.Fee.String(),
		"fee_type":     string(req.FeeType),
	}
	if err := s.repos.Positions.ApplyClose(ctx, updated, fill, eventType, payload); err != nil {
		return domain.PositionView{}, fmt.Errorf("apply close: %w", err)
	}

	s.log.Info("position close recorded",
		slog.String("symbol", position.Symbol),
		slog.String("close_pct", result.ClosePct.String()),
		slog.Bool("full", fullyClosed))

	return s.View(ctx, position.ID)
}

// UpdatePlan changes the current TP/SL plan, keeping the original one intact.
func (s *Service) UpdatePlan(ctx context.Context, id uuid.UUID, takeProfit, stopLoss []domain.PriceTarget, note string) (domain.PositionView, error) {
	position, err := s.repos.Positions.Get(ctx, id)
	if err != nil {
		return domain.PositionView{}, err
	}
	if position.Status == domain.PositionClosed {
		return domain.PositionView{}, ErrPositionClosed
	}

	plan := domain.TradePlan{TakeProfit: takeProfit, StopLoss: stopLoss, UpdatedAt: time.Now().UTC(), Note: note}
	payload := map[string]any{"note": note, "take_profit": takeProfit, "stop_loss": stopLoss}

	if err := s.repos.Positions.UpdatePlan(ctx, id, plan, payload); err != nil {
		return domain.PositionView{}, err
	}
	return s.View(ctx, id)
}

// AddFee records a manually entered fee.
func (s *Service) AddFee(ctx context.Context, id uuid.UUID, amount decimal.Decimal, feeType domain.FeeType, occurredAt time.Time, note string) error {
	if feeType == "" {
		feeType = domain.FeeCustom
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	event := domain.CashEvent{
		ID: uuid.New(), PositionID: id, Amount: amount, FeeType: feeType,
		OccurredAt: occurredAt.UTC(), Note: note,
	}
	return s.repos.Positions.AddCashEvent(ctx, "fee_events", event, domain.EventFeeAdded)
}

// AddFunding records a funding payment. There is no exchange integration, so
// funding is always entered by the user.
func (s *Service) AddFunding(ctx context.Context, id uuid.UUID, amount decimal.Decimal, occurredAt time.Time, note string) error {
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	event := domain.CashEvent{
		ID: uuid.New(), PositionID: id, Amount: amount,
		OccurredAt: occurredAt.UTC(), Note: note,
	}
	return s.repos.Positions.AddCashEvent(ctx, "funding_events", event, domain.EventFundingAdded)
}

// View assembles a position with its history and computed P&L.
func (s *Service) View(ctx context.Context, id uuid.UUID) (domain.PositionView, error) {
	position, err := s.repos.Positions.Get(ctx, id)
	if err != nil {
		return domain.PositionView{}, err
	}
	return s.buildView(ctx, position, nil)
}

// List returns position views, optionally only the open ones.
func (s *Service) List(ctx context.Context, onlyOpen bool) ([]domain.PositionView, error) {
	positions, err := s.repos.Positions.List(ctx, onlyOpen, nil)
	if err != nil {
		return nil, err
	}
	prices, err := s.repos.Market.LatestForAll(ctx)
	if err != nil {
		s.log.Warn("load prices failed", slog.String("error", err.Error()))
	}

	out := make([]domain.PositionView, 0, len(positions))
	for _, p := range positions {
		var price *decimal.Decimal
		if info, ok := prices[p.AssetID]; ok && info.Price > 0 {
			v := decimal.NewFromFloat(info.Price)
			price = &v
		}
		view, err := s.buildView(ctx, p, price)
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, nil
}

// OpenContexts projects open positions into the compact form given to the LLM.
func (s *Service) OpenContexts(ctx context.Context, assetID int64) ([]domain.PositionContext, error) {
	positions, err := s.repos.Positions.List(ctx, true, &assetID)
	if err != nil {
		return nil, err
	}
	if len(positions) == 0 {
		return nil, nil
	}

	var price *decimal.Decimal
	if info, err := s.repos.Market.Latest(ctx, assetID); err == nil && info.Price > 0 {
		v := decimal.NewFromFloat(info.Price)
		price = &v
	}

	out := make([]domain.PositionContext, 0, len(positions))
	for _, p := range positions {
		view, err := s.buildView(ctx, p, price)
		if err != nil {
			return nil, err
		}
		pc := domain.PositionContext{
			PositionID:    p.ID,
			Direction:     p.Direction,
			EntryPrice:    p.EntryPrice.InexactFloat64(),
			Leverage:      p.Leverage.InexactFloat64(),
			RemainingPct:  view.PnL.RemainingPct.InexactFloat64(),
			UnrealizedPct: view.PnL.LeveragedROIPct.InexactFloat64(),
			AgeMinutes:    view.AgeMinutes,
			SizeKnown:     p.SizeKnown,
		}
		if p.CurrentPlan != nil {
			for _, tp := range p.CurrentPlan.TakeProfit {
				pc.CurrentTargets = append(pc.CurrentTargets, tp.Price)
			}
			for _, sl := range p.CurrentPlan.StopLoss {
				pc.CurrentStops = append(pc.CurrentStops, sl.Price)
			}
		}
		out = append(out, pc)
	}
	return out, nil
}

func (s *Service) buildView(ctx context.Context, position domain.Position, price *decimal.Decimal) (domain.PositionView, error) {
	fills, err := s.repos.Positions.Fills(ctx, position.ID)
	if err != nil {
		return domain.PositionView{}, err
	}
	events, err := s.repos.Positions.Events(ctx, position.ID)
	if err != nil {
		return domain.PositionView{}, err
	}
	feeEvents, err := s.repos.Positions.CashEvents(ctx, "fee_events", position.ID)
	if err != nil {
		return domain.PositionView{}, err
	}
	fundingEvents, err := s.repos.Positions.CashEvents(ctx, "funding_events", position.ID)
	if err != nil {
		return domain.PositionView{}, err
	}

	if price == nil {
		if info, err := s.repos.Market.Latest(ctx, position.AssetID); err == nil && info.Price > 0 {
			v := decimal.NewFromFloat(info.Price)
			price = &v
		}
	}

	view := domain.PositionView{
		Position: position,
		Fills:    fills,
		Events:   events,
		Fees:     feeEvents,
		Funding:  fundingEvents,
	}
	if price != nil {
		f := price.InexactFloat64()
		view.CurrentPrice = &f
	}

	view.PnL = pnl.Compute(pnl.Inputs{
		Position:       position,
		Fills:          fills,
		Fees:           feeEvents,
		Funding:        fundingEvents,
		CurrentPrice:   price,
		FeesConfigured: s.fees.Configured,
	})

	end := time.Now().UTC()
	if position.ClosedAt != nil {
		end = *position.ClosedAt
	}
	view.AgeMinutes = int(end.Sub(position.OpenedAt).Minutes())

	view.Result = domain.ResultOpen
	if position.Status == domain.PositionClosed {
		view.Result = pnl.Classify(view.PnL.NetRealized)
	}
	return view, nil
}

// Delete removes a position and its entire history.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.repos.Positions.Delete(ctx, id)
}
