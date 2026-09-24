//go:build ignore

// Run from this directory with: go run generate.go
// FFmpeg is used only to create independent test fixtures, never at runtime.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func run(name string, args ...string) []byte {
	c := exec.Command(name, args...)
	c.Stderr = os.Stderr
	b, err := c.Output()
	if err != nil {
		panic(err)
	}
	return b
}
func main() {
	dir, err := os.MkdirTemp("", "gap2-fixtures-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	for _, tc := range []struct {
		name, layout string
		frequencies  []int
	}{
		{"stereo24-48000", "stereo", []int{311, 523}},
		{"surround24-48000", "5.1", []int{311, 523, 733, 61, 997, 1201}},
		{"surround71-48000", "7.1(wide)", []int{311, 523, 733, 61, 997, 1201, 1409, 1601}},
	} {
		var signals []string
		for _, f := range tc.frequencies {
			signals = append(signals, fmt.Sprintf("0.08*sin(2*PI*%d*t)", f))
		}
		input := "aevalsrc=" + strings.Join(signals, "|") + ":s=48000:d=0.16:c=" + tc.layout
		container := filepath.Join(dir, tc.name+".m4a")
		run("ffmpeg", "-v", "error", "-f", "lavfi", "-i", input, "-c:a", "alac", "-sample_fmt", "s32p", container)
		run("ffmpeg", "-y", "-v", "error", "-i", container, "-f", "s16le", tc.name+".s16le")
		encoded := run("ffprobe", "-v", "error", "-show_packets", "-show_data", "-show_entries", "packet=data", "-of", "json", container)
		var packets struct{ Packets []struct{ Data string } }
		if err := json.Unmarshal(encoded, &packets); err != nil {
			panic(err)
		}
		file, err := os.Create(tc.name + ".frames")
		if err != nil {
			panic(err)
		}
		for _, p := range packets.Packets {
			var data []byte
			for _, line := range strings.Split(p.Data, "\n") {
				_, s, ok := strings.Cut(line, ": ")
				if !ok {
					continue
				}
				s, _, _ = strings.Cut(s, "  ")
				b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
				if err != nil {
					panic(err)
				}
				data = append(data, b...)
			}
			if err := binary.Write(file, binary.BigEndian, uint32(len(data))); err != nil {
				panic(err)
			}
			if _, err := file.Write(data); err != nil {
				panic(err)
			}
		}
		if err := file.Close(); err != nil {
			panic(err)
		}
	}
}
