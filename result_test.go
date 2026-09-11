package odbc

import (
	"errors"
	"math"
	"testing"
)

func TestResultRowCountBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		counts   []int64
		want     int64
		unknown  bool
		overflow bool
	}{
		{name: "empty"},
		{name: "zero", counts: []int64{0, 0}},
		{name: "sum", counts: []int64{2, 3}, want: 5},
		{name: "maximum", counts: []int64{math.MaxInt64 - 1, 1}, want: math.MaxInt64},
		{name: "unknown", counts: []int64{3, -1, 4}, unknown: true},
		{name: "invalid negative", counts: []int64{math.MinInt64}, unknown: true},
		{name: "overflow", counts: []int64{math.MaxInt64, 1, 1}, overflow: true},
		{name: "both", counts: []int64{-1, math.MaxInt64, 1, -1, 0}, unknown: true, overflow: true},
		{name: "overflow then unknown", counts: []int64{math.MaxInt64, 1, -1}, unknown: true, overflow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := new(Result)
			for _, count := range tc.counts {
				result.addRowCount(count)
			}
			for range 2 {
				count, err := result.RowsAffected()
				if count != tc.want || errors.Is(err, ErrRowsAffectedUnknown) != tc.unknown || errors.Is(err, ErrRowsAffectedOverflow) != tc.overflow {
					t.Fatalf("RowsAffected = %d, %v; want %d unknown=%t overflow=%t", count, err, tc.want, tc.unknown, tc.overflow)
				}
			}
		})
	}
}
