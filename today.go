package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// TodayLines summarises the entries of one day: how long it was recorded, what
// the battery did, which programs were busiest and which problems came up.
func TodayLines(p palette, es []Entry, now time.Time) []string {
	out := []string{p.c(cBold+";"+cAccent, "kerna") + p.c(cDim, "  "+L("today", "bugün")), ""}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var day []Entry
	for _, e := range es {
		if !e.T.Before(start) {
			day = append(day, e)
		}
	}
	if len(day) == 0 {
		out = append(out, "  "+L("Nothing recorded today yet.", "Bugün henüz kayıt yok."))
		return append(out, p.c(cDim, "  "+L("Start recording with: kerna service install", "Kaydı başlatmak için: kerna service install")))
	}
	hours := float64(len(day)) * recordEvery.Hours()
	out = append(out, "  "+L(fmt.Sprintf("Recorded %s hours, %d samples.", fl(hours, 1), len(day)), fmt.Sprintf("%s saat kaydedildi, %d örnek.", fl(hours, 1), len(day))), "")

	// battery
	var sumW, peakW float64
	var peakAt time.Time
	var nW int
	var wh float64
	hourSum := make([]float64, 24)
	hourN := make([]int, 24)
	for _, e := range day {
		if e.Watts <= 0 {
			continue
		}
		nW++
		sumW += e.Watts
		wh += e.Watts * recordEvery.Hours()
		if e.Watts > peakW {
			peakW, peakAt = e.Watts, e.T
		}
		hourSum[e.T.Hour()] += e.Watts
		hourN[e.T.Hour()]++
	}
	if nW > 0 {
		out = append(out, "  "+p.c(cBold, L("Battery", "Pil")))
		out = append(out, "    "+L(fmt.Sprintf("On battery %s hours, about %s Wh used, average %s W, highest %s W at %s.", fl(float64(nW)*recordEvery.Hours(), 1), fl(wh, 0), fl(sumW/float64(nW), 1), fl(peakW, 0), peakAt.Format("15:04")),
			fmt.Sprintf("Pilde %s saat, yaklaşık %s Wh harcandı, ortalama %s W, en yüksek %s W (%s).", fl(float64(nW)*recordEvery.Hours(), 1), fl(wh, 0), fl(sumW/float64(nW), 1), fl(peakW, 0), peakAt.Format("15:04"))))
		var avg []float64
		for h := 0; h < 24; h++ {
			if hourN[h] > 0 {
				avg = append(avg, hourSum[h]/float64(hourN[h]))
			} else {
				avg = append(avg, -1)
			}
		}
		var sb strings.Builder
		for _, v := range avg {
			if v < 0 {
				sb.WriteString("·")
			} else {
				sb.WriteString(spark([]float64{v}, 1, 0, peakW))
			}
		}
		out = append(out, "    "+p.c(cAccent, sb.String())+p.c(cDim, "  "+L("average watts per hour, 00 to 23", "saat başına ortalama watt, 00'dan 23'e")))
		out = append(out, "")
	}

	// GPU
	var gw float64
	var ng int
	for _, e := range day {
		if e.GPUWatts > 0 {
			gw += e.GPUWatts
			ng++
		}
	}
	if ng > 0 {
		out = append(out, "  "+p.c(cBold, L("Graphics", "Grafik")), "    "+L(fmt.Sprintf("The GPU drivers reported %s W on average.", fl(gw/float64(ng), 1)), fmt.Sprintf("Ekran kartı sürücüleri ortalama %s W bildirdi.", fl(gw/float64(ng), 1))), "")
	}

	// programs
	sum := map[string]float64{}
	for _, e := range day {
		for _, t := range e.Top {
			sum[t.N] += t.C
		}
	}
	type kv struct {
		n string
		v float64
	}
	var tops []kv
	for n, v := range sum {
		tops = append(tops, kv{n, v / float64(len(day))})
	}
	sort.Slice(tops, func(i, j int) bool { return tops[i].v > tops[j].v })
	if len(tops) > 0 {
		out = append(out, "  "+p.c(cBold, L("Busiest programs", "En yoğun programlar"))+p.c(cDim, "  "+L("(average, in percent of one core)", "(ortalama, bir çekirdeğin yüzdesi)")))
		for i, t := range tops {
			if i == 5 {
				break
			}
			out = append(out, fmt.Sprintf("    %-24s %s%%", describeShort(t.n), fl(t.v, 0)))
		}
		out = append(out, "")
	}

	// GPU users
	gsum := map[string]float64{}
	for _, e := range day {
		for _, t := range e.GTop {
			gsum[t.N] += t.C
		}
	}
	var gtops []kv
	for n, v := range gsum {
		gtops = append(gtops, kv{n, v / float64(len(day))})
	}
	sort.Slice(gtops, func(i, j int) bool { return gtops[i].v > gtops[j].v })
	if len(gtops) > 0 {
		out = append(out, "  "+p.c(cBold, L("GPU users", "GPU kullananlar"))+p.c(cDim, "  "+L("(average, in percent of the GPU)", "(ortalama, GPU'nun yüzdesi)")))
		for i, t := range gtops {
			if i == 4 {
				break
			}
			out = append(out, fmt.Sprintf("    %-24s %s%%", describeShort(t.n), fl(t.v, 0)))
		}
		out = append(out, "")
	}

	// problems
	ins := Incidents(day)
	if len(ins) == 0 {
		out = append(out, "  "+p.c(cOK, "●")+" "+L("No problems today.", "Bugün sorun yok."))
	} else {
		out = append(out, "  "+p.c(cBold, L(fmt.Sprintf("%d problems", len(ins)), fmt.Sprintf("%d sorun", len(ins)))))
		for i, in := range ins {
			if i == 6 {
				out = append(out, p.c(cDim, "    …"))
				break
			}
			out = append(out, "    "+p.c(cDim, in.From.Format("15:04")+"–"+in.To.Add(recordEvery).Format("15:04")+"  ")+titleOf(in.ID))
		}
	}
	return out
}
