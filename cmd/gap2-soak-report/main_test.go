package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestReportRequiresSustainedPlayback(t *testing.T) {
	for _, broken := range []string{"", "underrun", "stall", "drop"} {
		var log strings.Builder
		for i := 0; i <= 121; i++ {
			u := 1
			frames := i * 240000
			if broken == "underrun" && i >= 50 {
				u = 2
			}
			if broken == "stall" && i == 50 {
				frames = 49 * 240000
			}
			if broken == "drop" && i == 50 {
				log.WriteString("msg=\"audio transport disconnected; waiting for reconnect\"\n")
			}
			fmt.Fprintf(&log, "msg=\"AP2 audio progress\" durationSeconds=%d playedFrames=%d underruns=%d\n", i*5, frames, u)
		}
		r, err := summarize(strings.NewReader(log.String()), 10*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if r.Passed != (broken == "") {
			t.Fatalf("%s: %+v", broken, r)
		}
	}
}
func TestReportDoesNotCountSilenceOrMultipleShortStreams(t *testing.T) {
	var log strings.Builder
	for i := 0; i < 2; i++ {
		for second := 0; second < 600; second += 5 {
			fmt.Fprintf(&log, "msg=\"AP2 audio progress\" durationSeconds=%d playedFrames=%d underruns=0\n", second, second*48000)
		}
		log.WriteString("msg=\"audio stream summary\"\n")
	}
	r, err := summarize(strings.NewReader(log.String()), 10*time.Minute)
	if err != nil || r.Passed || r.Streams != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = summarize(strings.NewReader("msg=\"AP2 audio progress\" durationSeconds=1000 playedFrames=0 underruns=0\n"), time.Second)
	if err != nil || r.Passed {
		t.Fatalf("%+v %v", r, err)
	}
}
