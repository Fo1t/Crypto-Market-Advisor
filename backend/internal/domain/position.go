package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// FillKind distinguishes the opening fill from closing fills.
type FillKind string

// Fill kinds.
const (
	FillOpen  FillKind = "OPEN"
	FillClose FillKind = "CLOSE"
)

// Position is a manually recorded user position.
// Quantities are decimals; nothing in accounting touches float64.
type Position struct {
	ID                uuid.UUID        `json:"id"`
	AssetID           int64            `json:"asset_id"`
	Symbol            string           `json:"symbol"`
	Direction         Direction        `json:"direction"`
	Status            PositionStatus   `json:"status"`
	EntryPrice        decimal.Decimal  `json:"entry_price"`
	Leverage          decimal.Decimal  `json:"leverage"`
	InitialQuantity   *decimal.Decimal `json:"initial_quantity,omitempty"`
	RemainingQuantity *decimal.Decimal `json:"remaining_quantity,omitempty"`
	InitialNotional   *decimal.Decimal `json:"initial_notional,omitempty"`
	InitialMargin     *decimal.Decimal `json:"initial_margin,omitempty"`
	SizeKnown         bool             `json:"size_known"`
	OpenedAt          time.Time        `json:"opened_at"`
	ClosedAt          *time.Time       `json:"closed_at,omitempty"`
	RecommendationID  *uuid.UUID       `json:"recommendation_id,omitempty"`
	FeeType           FeeType          `json:"fee_type"`
	OriginalPlan      *TradePlan       `json:"original_plan,omitempty"`
	CurrentPlan       *TradePlan       `json:"current_plan,omitempty"`
	Note              string           `json:"note,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// Fill is one execution: the open, or a (partial) close.
type Fill struct {
	ID           uuid.UUID        `json:"id"`
	PositionID   uuid.UUID        `json:"position_id"`
	Kind         FillKind         `json:"kind"`
	Quantity     *decimal.Decimal `json:"quantity,omitempty"`
	ClosePct     *decimal.Decimal `json:"close_pct,omitempty"`
	Price        decimal.Decimal  `json:"price"`
	Fee          decimal.Decimal  `json:"fee"`
	FeeType      FeeType          `json:"fee_type"`
	FeeEstimated bool             `json:"fee_estimated"`
	RealizedPnL  decimal.Decimal  `json:"realized_pnl"`
	ExecutedAt   time.Time        `json:"executed_at"`
	CreatedAt    time.Time        `json:"created_at"`
	Note         string           `json:"note,omitempty"`
}

// PositionEvent is one entry of the append-only audit trail.
type PositionEvent struct {
	ID         int64             `json:"id"`
	PositionID uuid.UUID         `json:"position_id"`
	Type       PositionEventType `json:"event_type"`
	Payload    map[string]any    `json:"payload"`
	OccurredAt time.Time         `json:"occurred_at"`
	CreatedAt  time.Time         `json:"created_at"`
}

// CashEvent is a manually recorded fee or funding transaction.
type CashEvent struct {
	ID         uuid.UUID       `json:"id"`
	PositionID uuid.UUID       `json:"position_id"`
	Amount     decimal.Decimal `json:"amount"`
	FeeType    FeeType         `json:"fee_type,omitempty"`
	OccurredAt time.Time       `json:"occurred_at"`
	Note       string          `json:"note,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// PnL is the computed profit and loss state of a position.
// Approximate is set when the user never supplied a position size, in which
// case only percentage figures are meaningful.
type PnL struct {
	GrossRealized   decimal.Decimal  `json:"gross_realized_pnl"`
	NetRealized     decimal.Decimal  `json:"net_realized_pnl"`
	Unrealized      decimal.Decimal  `json:"unrealized_pnl"`
	Total           decimal.Decimal  `json:"total_pnl"`
	Fees            decimal.Decimal  `json:"fees"`
	Funding         decimal.Decimal  `json:"funding"`
	RealizedPct     *decimal.Decimal `json:"realized_pnl_pct,omitempty"`
	UnrealizedPct   *decimal.Decimal `json:"unrealized_pnl_pct,omitempty"`
	PriceChangePct  decimal.Decimal  `json:"price_change_pct"`
	LeveragedROIPct decimal.Decimal  `json:"leveraged_roi_pct"`
	ROIOnMarginPct  *decimal.Decimal `json:"roi_on_margin_pct,omitempty"`
	Approximate     bool             `json:"approximate"`
	FeesConfigured  bool             `json:"fees_configured"`
	RemainingPct    decimal.Decimal  `json:"remaining_pct"`
}

// PositionView bundles a position with everything the UI needs to render it.
type PositionView struct {
	Position     Position        `json:"position"`
	Fills        []Fill          `json:"fills"`
	Events       []PositionEvent `json:"events,omitempty"`
	Fees         []CashEvent     `json:"fee_events,omitempty"`
	Funding      []CashEvent     `json:"funding_events,omitempty"`
	CurrentPrice *float64        `json:"current_price,omitempty"`
	PnL          PnL             `json:"pnl"`
	AgeMinutes   int             `json:"age_minutes"`
	Result       TradeResult     `json:"result"`
	MFEPct       *float64        `json:"max_favorable_excursion_pct,omitempty"`
	MAEPct       *float64        `json:"max_adverse_excursion_pct,omitempty"`
}
