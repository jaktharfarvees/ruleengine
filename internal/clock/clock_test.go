package clock

import (
	"testing"
	"time"
)

func TestFixed(t *testing.T) {
	want := time.Date(2026, time.October, 13, 9, 30, 0, 0, time.UTC)
	if got := (Fixed{Time: want}).Now(); !got.Equal(want) {
		t.Fatalf("Fixed.Now() = %v, want %v", got, want)
	}
}

func TestSystem(t *testing.T) {
	before := time.Now()
	got := (System{}).Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("System.Now() = %v, want value in [%v, %v]", got, before, after)
	}
}
