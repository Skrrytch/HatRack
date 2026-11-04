package core

import (
	"fmt"
	"sort"
	"time"
)

// FormatDuration renders 83 minutes as "1h 23m" and 5 minutes as "5m".
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %02dm", m/60, m%60)
}

// Minutes rounds a duration to whole minutes.
func Minutes(d time.Duration) int { return int(d.Round(time.Minute) / time.Minute) }

// FormatMinutes renders whole minutes like FormatDuration.
func FormatMinutes(m int) string { return FormatDuration(time.Duration(m) * time.Minute) }

// Apportion rounds durations to whole minutes so that they add up to the
// rounded sum (largest remainder): every part gets its full minutes, the
// minutes left over go to the parts with the largest remainders. Rounding
// each part on its own would let many short entries drift from the total.
func Apportion(ds []time.Duration) []int {
	var sum time.Duration
	for _, d := range ds {
		sum += d
	}
	return ApportionTo(ds, Minutes(sum))
}

// ApportionTo is Apportion with a given total, for parts of a part (the
// activities of a hat add up to the hat's minutes).
func ApportionTo(ds []time.Duration, total int) []int {
	out := make([]int, len(ds))
	left := total
	for i, d := range ds {
		out[i] = int(max(d, 0) / time.Minute)
		left -= out[i]
	}
	order := make([]int, len(ds))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return ds[order[a]]%time.Minute > ds[order[b]]%time.Minute })
	for k := 0; left > 0 && len(order) > 0; k = (k + 1) % len(order) {
		out[order[k]]++
		left--
	}
	return out
}

// RoundTotals rounds hat totals and their activities so that the activities
// add up to their hat and the hats to the total.
func RoundTotals(tot []HatTotal) (hats []int, acts [][]int, sum int) {
	ds := make([]time.Duration, len(tot))
	for i, t := range tot {
		ds[i] = t.Duration
	}
	hats = Apportion(ds)
	acts = make([][]int, len(tot))
	for i, t := range tot {
		ad := make([]time.Duration, len(t.Activities))
		for j, a := range t.Activities {
			ad[j] = a.Duration
		}
		acts[i] = ApportionTo(ad, hats[i])
		sum += hats[i]
	}
	return hats, acts, sum
}
