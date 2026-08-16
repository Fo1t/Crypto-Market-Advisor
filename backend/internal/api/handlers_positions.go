package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/domain"
	"github.com/crypto-market-advisor/advisor/internal/pnl"
	"github.com/crypto-market-advisor/advisor/internal/positions"
)

// CreatePositionRequest records a trade the user opened manually.
type CreatePositionRequest struct {
	Symbol           string               `json:"symbol"`
	Direction        string               `json:"direction"`
	EntryPrice       string               `json:"entry_price"`
	Leverage         string               `json:"leverage"`
	OpenedAt         *time.Time           `json:"opened_at"`
	Quantity         *string              `json:"quantity"`
	Notional         *string              `json:"notional"`
	Margin           *string              `json:"margin"`
	FeeType          string               `json:"fee_type"`
	ActualFee        *string              `json:"actual_fee"`
	RecommendationID *string              `json:"recommendation_id"`
	TakeProfit       []domain.PriceTarget `json:"take_profit"`
	StopLoss         []domain.PriceTarget `json:"stop_loss"`
	Note             string               `json:"note"`
}

func (s *Server) handleCreatePosition(w http.ResponseWriter, r *http.Request) {
	var req CreatePositionRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, r, s.log, err)
		return
	}

	direction, err := domain.ParseDirection(strings.ToUpper(strings.TrimSpace(req.Direction)))
	if err != nil {
		WriteError(w, r, s.log, ErrValidation("direction must be LONG or SHORT"))
		return
	}
	entryPrice, err := parsePositiveDecimal(req.EntryPrice, "entry_price")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	leverage, err := parsePositiveDecimal(req.Leverage, "leverage")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}

	openReq := positions.OpenRequest{
		Symbol:     strings.ToUpper(strings.TrimSpace(req.Symbol)),
		Direction:  direction,
		EntryPrice: entryPrice,
		Leverage:   leverage,
		FeeType:    domain.FeeType(strings.ToLower(defaultString(req.FeeType, "taker"))),
		TakeProfit: req.TakeProfit,
		StopLoss:   req.StopLoss,
		Note:       req.Note,
	}
	if !openReq.FeeType.Valid() {
		WriteError(w, r, s.log, ErrValidation("fee_type must be maker, taker or custom"))
		return
	}
	if req.OpenedAt != nil {
		openReq.OpenedAt = *req.OpenedAt
	}

	for _, field := range []struct {
		raw  *string
		dst  **decimal.Decimal
		name string
	}{
		{req.Quantity, &openReq.Quantity, "quantity"},
		{req.Notional, &openReq.Notional, "notional"},
		{req.Margin, &openReq.Margin, "margin"},
		{req.ActualFee, &openReq.ActualFee, "actual_fee"},
	} {
		if field.raw == nil || strings.TrimSpace(*field.raw) == "" {
			continue
		}
		value, err := decimal.NewFromString(strings.TrimSpace(*field.raw))
		if err != nil {
			WriteError(w, r, s.log, ErrValidation(field.name+" must be a number"))
			return
		}
		if value.IsNegative() {
			WriteError(w, r, s.log, ErrValidation(field.name+" must not be negative"))
			return
		}
		*field.dst = &value
	}

	if req.RecommendationID != nil && strings.TrimSpace(*req.RecommendationID) != "" {
		id, err := uuid.Parse(strings.TrimSpace(*req.RecommendationID))
		if err != nil {
			WriteError(w, r, s.log, ErrValidation("recommendation_id must be a UUID"))
			return
		}
		openReq.RecommendationID = &id
	}

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	position, err := s.deps.Positions.Open(ctx, openReq)
	if err != nil {
		if errors.Is(err, positions.ErrUnknownAsset) {
			WriteError(w, r, s.log, ErrValidation("unknown symbol: add the market first"))
			return
		}
		WriteError(w, r, s.log, ErrInternal("failed to record the position").WithCause(err))
		return
	}

	view, err := s.deps.Positions.View(ctx, position.ID)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("position stored but could not be read back").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusCreated, view)
}

func (s *Server) handleListPositions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	onlyOpen := r.URL.Query().Get("status") == "open"
	views, err := s.deps.Positions.List(ctx, onlyOpen)
	if err != nil {
		WriteError(w, r, s.log, ErrInternal("failed to list positions").WithCause(err))
		return
	}
	WriteJSON(w, http.StatusOK, Page[domain.PositionView]{Items: views, Total: len(views), Limit: len(views)})
}

func (s *Server) handleGetPosition(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	view, err := s.deps.Positions.View(ctx, id)
	if err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "position not found"))
		return
	}
	WriteJSON(w, http.StatusOK, view)
}

func (s *Server) handleDeletePosition(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	if err := s.deps.Positions.Delete(ctx, id); err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "position not found"))
		return
	}
	WriteJSON(w, http.StatusNoContent, nil)
}

// ClosePositionRequest is a manual (partial) close.
type ClosePositionRequest struct {
	ExecutionPrice string     `json:"execution_price"`
	ClosePct       *string    `json:"close_pct"`
	Quantity       *string    `json:"quantity"`
	FeeType        string     `json:"fee_type"`
	ActualFee      *string    `json:"actual_fee"`
	ExecutedAt     *time.Time `json:"executed_at"`
	Note           string     `json:"note"`
}

func (s *Server) handleClosePosition(w http.ResponseWriter, r *http.Request) {
	s.closePosition(w, r, true)
}

func (s *Server) handlePartialClose(w http.ResponseWriter, r *http.Request) {
	s.closePosition(w, r, false)
}

func (s *Server) closePosition(w http.ResponseWriter, r *http.Request, full bool) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	var req ClosePositionRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, r, s.log, err)
		return
	}

	price, err := parsePositiveDecimal(req.ExecutionPrice, "execution_price")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}

	closeReq := positions.CloseRequest{
		PositionID:     id,
		ExecutionPrice: price,
		FeeType:        domain.FeeType(strings.ToLower(strings.TrimSpace(req.FeeType))),
		Full:           full,
		Note:           req.Note,
	}
	if closeReq.FeeType != "" && !closeReq.FeeType.Valid() {
		WriteError(w, r, s.log, ErrValidation("fee_type must be maker, taker or custom"))
		return
	}
	if req.ExecutedAt != nil {
		closeReq.ExecutedAt = *req.ExecutedAt
	}

	if !full {
		if req.ClosePct != nil && strings.TrimSpace(*req.ClosePct) != "" {
			pct, err := parsePositiveDecimal(*req.ClosePct, "close_pct")
			if err != nil {
				WriteError(w, r, s.log, err)
				return
			}
			if pct.GreaterThan(decimal.NewFromInt(100)) {
				WriteError(w, r, s.log, ErrValidation("close_pct must be at most 100"))
				return
			}
			closeReq.ClosePct = &pct
		}
		if req.Quantity != nil && strings.TrimSpace(*req.Quantity) != "" {
			qty, err := parsePositiveDecimal(*req.Quantity, "quantity")
			if err != nil {
				WriteError(w, r, s.log, err)
				return
			}
			closeReq.Quantity = &qty
		}
		if closeReq.ClosePct == nil && closeReq.Quantity == nil {
			WriteError(w, r, s.log, ErrValidation("a partial close needs close_pct or quantity"))
			return
		}
	}
	if req.ActualFee != nil && strings.TrimSpace(*req.ActualFee) != "" {
		fee, err := decimal.NewFromString(strings.TrimSpace(*req.ActualFee))
		if err != nil || fee.IsNegative() {
			WriteError(w, r, s.log, ErrValidation("actual_fee must be a non-negative number"))
			return
		}
		closeReq.ActualFee = &fee
	}

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	view, err := s.deps.Positions.Close(ctx, closeReq)
	switch {
	case errors.Is(err, positions.ErrPositionClosed):
		WriteError(w, r, s.log, ErrConflict("this position is already closed"))
	case errors.Is(err, pnl.ErrNoCloseSize):
		WriteError(w, r, s.log, ErrValidation(err.Error()))
	case err != nil:
		WriteError(w, r, s.log, notFoundOr(err, "position not found"))
	default:
		WriteJSON(w, http.StatusOK, view)
	}
}

// UpdatePlanRequest changes the active TP/SL plan.
type UpdatePlanRequest struct {
	TakeProfit []domain.PriceTarget `json:"take_profit"`
	StopLoss   []domain.PriceTarget `json:"stop_loss"`
	Note       string               `json:"note"`
}

func (s *Server) handleUpdatePlan(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	var req UpdatePlanRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, r, s.log, err)
		return
	}
	for _, group := range [][]domain.PriceTarget{req.TakeProfit, req.StopLoss} {
		for _, t := range group {
			if t.Price <= 0 {
				WriteError(w, r, s.log, ErrValidation("plan prices must be positive"))
				return
			}
			if t.ClosePct < 0 || t.ClosePct > 100 {
				WriteError(w, r, s.log, ErrValidation("plan close_pct must be in [0,100]"))
				return
			}
		}
	}

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	view, err := s.deps.Positions.UpdatePlan(ctx, id, req.TakeProfit, req.StopLoss, req.Note)
	if err != nil {
		if errors.Is(err, positions.ErrPositionClosed) {
			WriteError(w, r, s.log, ErrConflict("this position is already closed"))
			return
		}
		WriteError(w, r, s.log, notFoundOr(err, "position not found"))
		return
	}
	WriteJSON(w, http.StatusOK, view)
}

// CashEventRequest records a fee or funding entry.
type CashEventRequest struct {
	Amount     string     `json:"amount"`
	FeeType    string     `json:"fee_type"`
	OccurredAt *time.Time `json:"occurred_at"`
	Note       string     `json:"note"`
}

func (s *Server) handleAddFee(w http.ResponseWriter, r *http.Request) {
	id, req, ok := s.parseCashEvent(w, r)
	if !ok {
		return
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(req.Amount))
	if err != nil || amount.IsNegative() {
		WriteError(w, r, s.log, ErrValidation("amount must be a non-negative number"))
		return
	}

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	feeType := domain.FeeType(strings.ToLower(defaultString(req.FeeType, "custom")))
	if !feeType.Valid() {
		WriteError(w, r, s.log, ErrValidation("fee_type must be maker, taker or custom"))
		return
	}
	occurred := time.Time{}
	if req.OccurredAt != nil {
		occurred = *req.OccurredAt
	}
	if err := s.deps.Positions.AddFee(ctx, id, amount, feeType, occurred, req.Note); err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "position not found"))
		return
	}
	s.respondWithPosition(w, r, ctx, id)
}

func (s *Server) handleAddFunding(w http.ResponseWriter, r *http.Request) {
	id, req, ok := s.parseCashEvent(w, r)
	if !ok {
		return
	}
	// Funding is signed: negative means paid, positive means received.
	amount, err := decimal.NewFromString(strings.TrimSpace(req.Amount))
	if err != nil {
		WriteError(w, r, s.log, ErrValidation("amount must be a number"))
		return
	}

	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	occurred := time.Time{}
	if req.OccurredAt != nil {
		occurred = *req.OccurredAt
	}
	if err := s.deps.Positions.AddFunding(ctx, id, amount, occurred, req.Note); err != nil {
		WriteError(w, r, s.log, notFoundOr(err, "position not found"))
		return
	}
	s.respondWithPosition(w, r, ctx, id)
}

func (s *Server) parseCashEvent(w http.ResponseWriter, r *http.Request) (uuid.UUID, CashEventRequest, bool) {
	id, err := parseUUID(r, "id")
	if err != nil {
		WriteError(w, r, s.log, err)
		return uuid.Nil, CashEventRequest{}, false
	}
	var req CashEventRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, r, s.log, err)
		return uuid.Nil, CashEventRequest{}, false
	}
	return id, req, true
}

func parsePositiveDecimal(raw, field string) (decimal.Decimal, error) {
	value, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil {
		return decimal.Zero, ErrValidation(field + " must be a number")
	}
	if !value.IsPositive() {
		return decimal.Zero, ErrValidation(field + " must be positive")
	}
	return value, nil
}
