// gap2-soak-report summarizes receiver diagnostics without retaining audio,
// addresses, pairing information, or raw log records.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type report struct {
	ProgressSamples          int     `json:"progress_samples"`
	Streams                  int     `json:"streams"`
	LongestContinuousSeconds float64 `json:"longest_continuous_seconds"`
	TotalStableSeconds       float64 `json:"total_stable_seconds"`
	UnderrunsAfterStartup    uint64  `json:"underruns_after_startup"`
	TransportDisconnects     int     `json:"transport_disconnects"`
	StalledIntervals         int     `json:"stalled_intervals"`
	Passed                   bool    `json:"passed"`
}

var numeric = regexp.MustCompile(`(?:^| )(durationSeconds|playedFrames|underruns)=([0-9.]+)`)

func summarize(r io.Reader, minimum time.Duration) (report, error) {
	var result report
	var previous, first float64
	var frames, under uint64
	active := false
	seen := false
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		line := scan.Text()
		if strings.Contains(line, `msg="audio transport disconnected; waiting for reconnect"`) {
			result.TransportDisconnects++
			active = false
		}
		if strings.Contains(line, `msg="audio stream summary"`) || strings.Contains(line, `msg="control disconnected"`) {
			active = false
			seen = false
		}
		if !strings.Contains(line, `msg="AP2 audio progress"`) {
			continue
		}
		values := map[string]float64{}
		for _, m := range numeric.FindAllStringSubmatch(line, -1) {
			v, err := strconv.ParseFloat(m[2], 64)
			if err != nil {
				return result, err
			}
			values[m[1]] = v
		}
		seconds, ok := values["durationSeconds"]
		if !ok {
			continue
		}
		f, ok := values["playedFrames"]
		if !ok {
			continue
		}
		u, ok := values["underruns"]
		if !ok {
			continue
		}
		result.ProgressSamples++
		if !seen || seconds < previous {
			result.Streams++
			seen = true
			active = false
		}
		if active && (uint64(u) > under || uint64(f) <= frames || seconds-previous > 15) {
			if uint64(u) > under {
				result.UnderrunsAfterStartup += uint64(u) - under
			}
			result.StalledIntervals++
			active = false
		}
		if f > 0 {
			if active {
				result.TotalStableSeconds += seconds - previous
			}
			if !active {
				first = seconds
				active = true
			}
			if duration := seconds - first; duration > result.LongestContinuousSeconds {
				result.LongestContinuousSeconds = duration
			}
		}
		previous, frames, under = seconds, uint64(f), uint64(u)
	}
	if err := scan.Err(); err != nil {
		return result, err
	}
	result.Passed = result.LongestContinuousSeconds >= minimum.Seconds() && result.UnderrunsAfterStartup == 0 && result.StalledIntervals == 0 && result.TransportDisconnects == 0
	return result, nil
}
func main() {
	minimum := flag.Duration("min-duration", 10*time.Minute, "required uninterrupted playback after startup")
	flag.Parse()
	if *minimum <= 0 {
		fmt.Fprintln(os.Stderr, "min-duration must be positive")
		os.Exit(2)
	}
	r, err := summarize(os.Stdin, *minimum)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if !r.Passed {
		os.Exit(1)
	}
}
