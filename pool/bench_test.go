package pool

import (
	"context"
	"strconv"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/ruletest"
)

func testBudget() fact.Budget { return ruletest.Budget() }

// withTicker replaces the janitor's ticker so a test drives it by hand.
func withTicker(ticks chan time.Time) Option {
	return func(c *config) {
		c.newTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	}
}

func benchPool(b *testing.B, tenants int) (*Pool, []string) {
	b.Helper()
	e := newEnv()
	p, err := New(e.factory)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = p.Close(context.Background()) })
	ids := make([]string, tenants)
	for i := range ids {
		ids[i] = "tenant-" + strconv.Itoa(i)
	}
	if err := p.Warm(context.Background(), ids...); err != nil {
		b.Fatal(err)
	}
	return p, ids
}

// BenchmarkForHit measures the path every request takes once its tenant is
// pooled.
func BenchmarkForHit(b *testing.B) {
	p, ids := benchPool(b, 1)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := p.For(ctx, ids[0]); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkForHitParallel is the same path under contention across many
// tenants, which is how a busy service uses it.
func BenchmarkForHitParallel(b *testing.B) {
	p, ids := benchPool(b, 64)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if _, err := p.For(ctx, ids[i%len(ids)]); err != nil {
				b.Error(err)
				return
			}
			i++
		}
	})
}

// BenchmarkGetHit measures the lookup-only path used inside transactions.
func BenchmarkGetHit(b *testing.B) {
	p, ids := benchPool(b, 1)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, ok := p.Get(ids[0]); !ok {
			b.Fatal("miss")
		}
	}
}

var _ ruleengine.Resolver = (*Engine)(nil)
