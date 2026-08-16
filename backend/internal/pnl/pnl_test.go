package pnl

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/crypto-market-advisor/advisor/internal/domain"
)

func dec(v string) decimal.Decimal {
	d, err := decimal.NewFromString(v)
	if err != nil {
		panic(err)
	}
	return d
}

func decPtr(v string) *decimal.Decimal {
	d := dec(v)
	return &d
}

// longPosition is 1 BTC at 100000 with 10x leverage: notional 100000, margin 10000.
func longPosition() domain.Position {
	qty := dec("1")
	notional := dec("100000")
	margin := dec("10000")
	return domain.Position{
		ID:                uuid.New(),
		Symbol:            "BTC",
		Direction:         domain.DirectionLong,
		Status:            domain.PositionOpen,
		EntryPrice:        dec("100000"),
		Leverage:          dec("10"),
		InitialQuantity:   &qty,
		RemainingQuantity: &qty,
		InitialNotional:   &notional,
		InitialMargin:     &margin,
		SizeKnown:         true,
		OpenedAt:          time.Now().Add(-time.Hour),
	}
}

func shortPosition() domain.Position {
	p := longPosition()
	p.Direction = domain.DirectionShort
	return p
}

func TestDeriveSizeFromQuantity(t *testing.T) {
	q, n, m, ok := DeriveSize(dec("100000"), dec("10"), decPtr("0.5"), nil, nil)
	if !ok {
		t.Fatal("expected size to be derivable")
	}
	if !q.Equal(dec("0.5")) || !n.Equal(dec("50000")) || !m.Equal(dec("5000")) {
		t.Fatalf("unexpected derivation: q=%s n=%s m=%s", q, n, m)
	}
}

func TestDeriveSizeFromMarginAndNotional(t *testing.T) {
	q, n, m, ok := DeriveSize(dec("100000"), dec("10"), nil, nil, decPtr("2000"))
	if !ok {
		t.Fatal("expected size to be derivable from margin")
	}
	if !n.Equal(dec("20000")) || !q.Equal(dec("0.2")) || !m.Equal(dec("2000")) {
		t.Fatalf("unexpected derivation from margin: q=%s n=%s m=%s", q, n, m)
	}

	q2, n2, m2, ok := DeriveSize(dec("100000"), dec("10"), nil, decPtr("20000"), nil)
	if !ok || !q2.Equal(q) || !n2.Equal(n) || !m2.Equal(m) {
		t.Fatalf("notional and margin inputs must agree: %s %s %s", q2, n2, m2)
	}
}

func TestDeriveSizeWithoutInputIsUnknown(t *testing.T) {
	if _, _, _, ok := DeriveSize(dec("100000"), dec("10"), nil, nil, nil); ok {
		t.Fatal("size must be unknown when the user supplied nothing")
	}
}

func TestLongRealizedPnL(t *testing.T) {
	res, err := ComputeClose(CloseRequest{
		Position:       longPosition(),
		ExecutionPrice: dec("110000"),
		ClosePct:       decPtr("100"),
		FeesConfigured: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.RealizedPnL.Equal(dec("10000")) {
		t.Fatalf("long P&L must be +10000, got %s", res.RealizedPnL)
	}
	if !res.Remaining.IsZero() || !res.FullyClosed {
		t.Fatalf("a 100%% close must leave nothing open: remaining=%s full=%v", res.Remaining, res.FullyClosed)
	}
}

func TestShortRealizedPnL(t *testing.T) {
	res, err := ComputeClose(CloseRequest{
		Position:       shortPosition(),
		ExecutionPrice: dec("90000"),
		ClosePct:       decPtr("100"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.RealizedPnL.Equal(dec("10000")) {
		t.Fatalf("a short that fell 10000 must earn +10000, got %s", res.RealizedPnL)
	}

	loss, _ := ComputeClose(CloseRequest{
		Position:       shortPosition(),
		ExecutionPrice: dec("105000"),
		ClosePct:       decPtr("100"),
	})
	if !loss.RealizedPnL.Equal(dec("-5000")) {
		t.Fatalf("a short that rose 5000 must lose 5000, got %s", loss.RealizedPnL)
	}
}

func TestPartialCloseReducesRemaining(t *testing.T) {
	position := longPosition()

	first, err := ComputeClose(CloseRequest{
		Position:       position,
		ExecutionPrice: dec("110000"),
		ClosePct:       decPtr("25"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !first.Quantity.Equal(dec("0.25")) {
		t.Fatalf("25%% of 1 BTC is 0.25, got %s", first.Quantity)
	}
	if !first.RealizedPnL.Equal(dec("2500")) {
		t.Fatalf("expected 2500 realized, got %s", first.RealizedPnL)
	}
	if !first.Remaining.Equal(dec("0.75")) {
		t.Fatalf("expected 0.75 remaining, got %s", first.Remaining)
	}
	if first.FullyClosed {
		t.Fatal("a partial close must not close the position")
	}

	// The next percentage refers to what is still open, not to the original size.
	position.RemainingQuantity = &first.Remaining
	second, _ := ComputeClose(CloseRequest{
		Position:       position,
		ExecutionPrice: dec("120000"),
		ClosePct:       decPtr("50"),
	})
	if !second.Quantity.Equal(dec("0.375")) {
		t.Fatalf("50%% of the remaining 0.75 is 0.375, got %s", second.Quantity)
	}
	if !second.Remaining.Equal(dec("0.375")) {
		t.Fatalf("expected 0.375 remaining, got %s", second.Remaining)
	}
}

func TestCloseByQuantityIsCappedAtRemaining(t *testing.T) {
	res, err := ComputeClose(CloseRequest{
		Position:       longPosition(),
		ExecutionPrice: dec("110000"),
		Quantity:       decPtr("5"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Quantity.Equal(dec("1")) {
		t.Fatalf("closing more than is open must be capped, got %s", res.Quantity)
	}
}

func TestCloseWithoutSizeIsRejected(t *testing.T) {
	if _, err := ComputeClose(CloseRequest{Position: longPosition(), ExecutionPrice: dec("110000")}); err == nil {
		t.Fatal("expected an error when neither percentage nor quantity is given")
	}
}

func TestMakerAndTakerFees(t *testing.T) {
	base := CloseRequest{
		Position:       longPosition(),
		ExecutionPrice: dec("100000"),
		ClosePct:       decPtr("100"),
		MakerFeePct:    dec("0.02"),
		TakerFeePct:    dec("0.055"),
		FeesConfigured: true,
	}

	base.FeeType = domain.FeeMaker
	maker, _ := ComputeClose(base)
	if !maker.Fee.Equal(dec("20")) {
		t.Fatalf("maker fee on 100000 notional at 0.02%% must be 20, got %s", maker.Fee)
	}

	base.FeeType = domain.FeeTaker
	taker, _ := ComputeClose(base)
	if !taker.Fee.Equal(dec("55")) {
		t.Fatalf("taker fee on 100000 notional at 0.055%% must be 55, got %s", taker.Fee)
	}
	if !maker.FeeEstimated || !taker.FeeEstimated {
		t.Fatal("fees derived from configured rates must be flagged as estimated")
	}
}

func TestActualFeeOverridesEstimate(t *testing.T) {
	res, _ := ComputeClose(CloseRequest{
		Position:       longPosition(),
		ExecutionPrice: dec("100000"),
		ClosePct:       decPtr("100"),
		TakerFeePct:    dec("0.055"),
		FeesConfigured: true,
		ActualFee:      decPtr("43.21"),
	})
	if !res.Fee.Equal(dec("43.21")) {
		t.Fatalf("the user-entered fee must win, got %s", res.Fee)
	}
	if res.FeeEstimated {
		t.Fatal("a user-entered fee must not be marked as estimated")
	}
}

func TestUnconfiguredFeesAreZeroNotInvented(t *testing.T) {
	res, _ := ComputeClose(CloseRequest{
		Position:       longPosition(),
		ExecutionPrice: dec("100000"),
		ClosePct:       decPtr("100"),
		FeesConfigured: false,
	})
	if !res.Fee.IsZero() {
		t.Fatalf("without configured rates the fee must stay zero, got %s", res.Fee)
	}
	if !res.FeeEstimated {
		t.Fatal("an unknown fee must be flagged so the UI can warn about it")
	}
}

func TestComputeAggregatesFillsFeesAndFunding(t *testing.T) {
	position := longPosition()
	remaining := dec("0.5")
	position.RemainingQuantity = &remaining
	position.Status = domain.PositionPartiallyClosed

	current := dec("120000")
	got := Compute(Inputs{
		Position: position,
		Fills: []domain.Fill{
			{Kind: domain.FillOpen, Price: dec("100000"), Fee: dec("55")},
			{Kind: domain.FillClose, Price: dec("110000"), Fee: dec("55"), RealizedPnL: dec("5000"), Quantity: decPtr("0.5")},
		},
		Fees:           []domain.CashEvent{{Amount: dec("10")}},
		Funding:        []domain.CashEvent{{Amount: dec("-7")}},
		CurrentPrice:   &current,
		FeesConfigured: true,
	})

	if !got.GrossRealized.Equal(dec("5000")) {
		t.Fatalf("gross realized must be 5000, got %s", got.GrossRealized)
	}
	if !got.Fees.Equal(dec("120")) {
		t.Fatalf("fees must total 120, got %s", got.Fees)
	}
	if !got.Funding.Equal(dec("-7")) {
		t.Fatalf("funding must total -7, got %s", got.Funding)
	}
	if !got.NetRealized.Equal(dec("4873")) {
		t.Fatalf("net realized must be 5000-120-7=4873, got %s", got.NetRealized)
	}
	if !got.Unrealized.Equal(dec("10000")) {
		t.Fatalf("unrealized on 0.5 BTC from 100000 to 120000 must be 10000, got %s", got.Unrealized)
	}
	if !got.Total.Equal(dec("14873")) {
		t.Fatalf("total must be 14873, got %s", got.Total)
	}
	if !got.RemainingPct.Equal(dec("50")) {
		t.Fatalf("remaining must be 50%%, got %s", got.RemainingPct)
	}
	if got.ROIOnMarginPct == nil || !got.ROIOnMarginPct.Equal(dec("148.73")) {
		t.Fatalf("ROI on 10000 margin must be 148.73%%, got %v", got.ROIOnMarginPct)
	}
}

func TestShortUnrealizedIsInverted(t *testing.T) {
	position := shortPosition()
	current := dec("90000")

	got := Compute(Inputs{Position: position, CurrentPrice: &current})
	if !got.Unrealized.Equal(dec("10000")) {
		t.Fatalf("a short in profit must show a positive unrealized, got %s", got.Unrealized)
	}
	if !got.PriceChangePct.Equal(dec("10")) {
		t.Fatalf("price change for a short must be sign-adjusted, got %s", got.PriceChangePct)
	}
	if !got.LeveragedROIPct.Equal(dec("100")) {
		t.Fatalf("10%% move at 10x must be 100%% ROI, got %s", got.LeveragedROIPct)
	}
}

func TestUnknownSizeStillReportsPercentages(t *testing.T) {
	position := longPosition()
	position.SizeKnown = false
	position.InitialQuantity = nil
	position.RemainingQuantity = nil
	position.InitialMargin = nil
	position.InitialNotional = nil

	current := dec("105000")
	got := Compute(Inputs{Position: position, CurrentPrice: &current})

	if !got.Approximate {
		t.Fatal("a position without a recorded size must be flagged approximate")
	}
	if !got.PriceChangePct.Equal(dec("5")) {
		t.Fatalf("price change must still be computed, got %s", got.PriceChangePct)
	}
	if !got.LeveragedROIPct.Equal(dec("50")) {
		t.Fatalf("leveraged ROI must still be computed, got %s", got.LeveragedROIPct)
	}
	if !got.Unrealized.IsZero() {
		t.Fatalf("absolute P&L is meaningless without a size, got %s", got.Unrealized)
	}
}

func TestClassify(t *testing.T) {
	if Classify(dec("1")) != domain.ResultWin {
		t.Fatal("positive net must be a win")
	}
	if Classify(dec("-1")) != domain.ResultLoss {
		t.Fatal("negative net must be a loss")
	}
	if Classify(decimal.Zero) != domain.ResultBreakeven {
		t.Fatal("zero net must be breakeven")
	}
}

func TestDecimalPrecisionIsPreserved(t *testing.T) {
	// A quantity that cannot be represented exactly in binary floating point.
	position := longPosition()
	qty := dec("0.1")
	position.InitialQuantity = &qty
	position.RemainingQuantity = &qty

	var total decimal.Decimal
	for i := 0; i < 10; i++ {
		res, err := ComputeClose(CloseRequest{
			Position:       position,
			ExecutionPrice: dec("100010"),
			Quantity:       decPtr("0.01"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		total = total.Add(res.RealizedPnL)
		remaining := res.Remaining
		position.RemainingQuantity = &remaining
	}
	if !total.Equal(dec("1")) {
		t.Fatalf("ten closes of 0.01 BTC at +10 must total exactly 1, got %s", total)
	}
	if !position.RemainingQuantity.IsZero() {
		t.Fatalf("the position must be exactly flat, got %s", position.RemainingQuantity)
	}
}
