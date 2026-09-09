package main

import (
	"fmt"
	"sort"
	"time"
)

type stat struct {
	N   int
	Avg float64
	P50 float64
	P95 float64
	P99 float64
	Max float64
	Min float64
}

func statsOf(vals []float64) stat {
	if len(vals) == 0 {
		return stat{}
	}
	s := make([]float64, len(vals))
	copy(s, vals)
	sort.Float64s(s)
	var sum float64
	for _, v := range s {
		sum += v
	}
	pick := func(p float64) float64 {
		i := int(float64(len(s)-1) * p / 100)
		if i < 0 {
			i = 0
		}
		if i >= len(s) {
			i = len(s) - 1
		}
		return s[i]
	}
	return stat{
		N: len(s), Avg: sum / float64(len(s)),
		P50: pick(50), P95: pick(95), P99: pick(99),
		Max: s[len(s)-1], Min: s[0],
	}
}

// msF 把耗时转成毫秒数值(浮点)
func msF(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

func fmtMs(v float64) string {
	switch {
	case v >= 10000:
		return fmt.Sprintf("%.1fs", v/1000)
	case v >= 1000:
		return fmt.Sprintf("%.2fs", v/1000)
	case v >= 100:
		return fmt.Sprintf("%.0fms", v)
	default:
		return fmt.Sprintf("%.2fms", v)
	}
}

func fmtF(v float64) string { return fmt.Sprintf("%.1f", v) }
