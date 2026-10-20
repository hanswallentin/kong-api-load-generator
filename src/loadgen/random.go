package loadgen

import (
	"math/rand/v2"
	"sync"
	"time"
)

// source is a concurrency-safe random source. A non-zero seed makes the
// choices repeatable, which the statistical tests rely on.
type source struct {
	mu sync.Mutex
	r  *rand.Rand
}

func newSource(seed uint64) *source {
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	return &source{r: rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))}
}

func (s *source) float64() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.r.Float64()
}

// intN returns a value in [0, n). It panics if n <= 0, as math/rand does.
func (s *source) intN(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.r.IntN(n)
}
