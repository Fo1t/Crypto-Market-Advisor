package pnl

import "errors"

// ErrNoCloseSize is returned when a close request specifies neither a
// percentage nor a quantity.
var ErrNoCloseSize = errors.New("close request needs either close_pct or quantity")
