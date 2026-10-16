package fact

import (
	"fmt"
	"time"
)

// Budget bounds what one request may spend fetching facts.
//
// There is no default. An unset budget is a configuration error rather than a
// silent fallback, because a silent default on a fact budget is a silent
// change to a payroll figure: the first time a dataset grows, decisions would
// start failing or slowing with nothing in the configuration to explain why.
type Budget struct {
	// MaxDatasetFacts is how many distinct dataset queries one request may
	// execute, counted after de-duplication.
	MaxDatasetFacts int
	// PerFactTimeout bounds one executor call.
	PerFactTimeout time.Duration
	// TotalFactTimeout bounds the whole fetch stage.
	TotalFactTimeout time.Duration
	// MaxRowsScanned is both the row limit compiled into every query and the
	// post-hoc check applied to what an executor reports scanning.
	MaxRowsScanned int
}

// Validate reports whether the budget is usable. Every value must be positive;
// zero means unset and is an error.
func (b Budget) Validate() error {
	if b.MaxDatasetFacts <= 0 {
		return fmt.Errorf("fact budget MaxDatasetFacts must be positive, got %d", b.MaxDatasetFacts)
	}
	if b.PerFactTimeout <= 0 {
		return fmt.Errorf("fact budget PerFactTimeout must be positive, got %s", b.PerFactTimeout)
	}
	if b.TotalFactTimeout <= 0 {
		return fmt.Errorf("fact budget TotalFactTimeout must be positive, got %s", b.TotalFactTimeout)
	}
	if b.TotalFactTimeout < b.PerFactTimeout {
		return fmt.Errorf("fact budget TotalFactTimeout %s is shorter than PerFactTimeout %s",
			b.TotalFactTimeout, b.PerFactTimeout)
	}
	if b.MaxRowsScanned <= 0 {
		return fmt.Errorf("fact budget MaxRowsScanned must be positive, got %d", b.MaxRowsScanned)
	}
	return nil
}

// spend tracks one request's consumption against a budget. A breach fails the
// whole decision: there is no partial fact set, ever (INV3), because a missing
// fact silently becomes a different decision.
type spend struct {
	budget      Budget
	datasetCost int
	rowsScanned int
}

func (s *spend) chargeQuery() error {
	s.datasetCost++
	if s.datasetCost > s.budget.MaxDatasetFacts {
		return fmt.Errorf("request needs more than %d dataset facts", s.budget.MaxDatasetFacts)
	}
	return nil
}

// chargeRows applies the post-hoc row check. An executor reporting more rows
// than the compiled limit is misbehaving, so the decision fails closed rather
// than trusting the value that came back with it.
func (s *spend) chargeRows(field string, scanned int) error {
	if scanned > s.budget.MaxRowsScanned {
		return fmt.Errorf("executor scanned %d rows for field %q against a limit of %d",
			scanned, field, s.budget.MaxRowsScanned)
	}
	s.rowsScanned += scanned
	if s.rowsScanned > s.budget.MaxRowsScanned {
		return fmt.Errorf("request scanned %d rows in total against a limit of %d",
			s.rowsScanned, s.budget.MaxRowsScanned)
	}
	return nil
}
