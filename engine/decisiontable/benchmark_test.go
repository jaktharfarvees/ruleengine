package decisiontable

import (
	"context"
	"fmt"
	"testing"

	baseengine "github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/rule"
)

func BenchmarkEvaluate(b *testing.B) {
	for _, rows := range []int{10, 100} {
		for _, trace := range []bool{false, true} {
			b.Run(fmt.Sprintf("rows=%d/trace=%t", rows, trace), func(b *testing.B) {
				logic := benchmarkLogic(rows)
				implementation := New()
				input := baseengine.Input{Facts: map[string]any{"value": rows - 1}, Trace: trace}
				if _, err := implementation.Evaluate(context.Background(), logic, input); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if _, err := implementation.Evaluate(context.Background(), logic, input); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func benchmarkLogic(count int) rule.RuleLogic {
	rows := make([]rule.DecisionRow, count)
	for index := range rows {
		rows[index] = rule.DecisionRow{
			ID: fmt.Sprintf("row-%03d", index), When: map[string]string{"value": fmt.Sprintf("== %d", index)},
			Then: map[string]any{"matched": index},
		}
	}
	return rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: rows}
}
