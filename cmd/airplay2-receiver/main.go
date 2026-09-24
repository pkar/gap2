// Command airplay2-receiver is a thin standalone wrapper over the airplay2
// package. Policy (flags, signals, diagnostics) lives here; protocol and
// audio work belongs to the library.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	airplay2 "github.com/pkar/gap2"
	"github.com/pkar/gap2/output/alsa"
	"github.com/pkar/gap2/output/pcmfile"
	"github.com/pkar/gap2/pcm"
)

// version is the build version. It is a var so release builds can override it
// with -ldflags "-X main.version=<version>"; the default marks a development
// build.
var version = "0.1.0-dev"

// stringSlice collects repeated flag values.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// nonClosingWriter hides the io.Closer method so a pcmfile sink cannot close a
// writer whose lifetime the process owns (for example the -output file).
type nonClosingWriter struct{ w io.Writer }

func (n nonClosingWriter) Write(p []byte) (int, error) { return n.w.Write(p) }

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		run(os.Args[2:])
	case "doctor":
		doctor(os.Args[2:])
	case "version":
		fmt.Printf("airplay2-receiver %s\n", version)
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: airplay2-receiver <command> [flags]")
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  run      start the receiver")
	fmt.Fprintln(w, "  doctor   check interfaces and configuration")
	fmt.Fprintln(w, "  version  print version")
}

func run(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	name := fs.String("name", "AirPlay2 Receiver", "advertised receiver name")
	listen := fs.String("listen", ":7000", "control TCP listen address")
	pin := fs.String("pin", "3939", "numeric pairing setup code")
	pairings := fs.String("pairings", "", "JSON file for persistent pairings (empty = in-memory)")
	output := fs.String("output", "", "write decoded PCM to this file (\"-\" for stdout)")
	device := fs.String("audio-device", "", "Linux ALSA playback device path")
	outputRate := fs.Int("output-rate", 0, "fixed output sample rate (0 = initial source rate)")
	outputChannels := fs.Int("output-channels", 0, "fixed output channels, 1 through 8 (0 = initial source)")
	debug := fs.Bool("debug", false, "enable debug logging")
	var ifaces stringSlice
	fs.Var(&ifaces, "interface", "network interface (repeatable)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: airplay2-receiver run [flags]\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	var out pcm.Factory
	if *device != "" && *output != "" {
		fmt.Fprintln(os.Stderr, "airplay2-receiver: choose either -audio-device or -output")
		os.Exit(2)
	}
	if *device != "" {
		out = alsa.Device(*device)
	}
	if *output != "" {
		var w io.Writer
		var f *os.File
		if *output == "-" {
			w = os.Stdout
		} else {
			var err error
			f, err = os.Create(*output)
			if err != nil {
				fmt.Fprintf(os.Stderr, "airplay2-receiver: output: %v\n", err)
				os.Exit(1)
			}
			defer f.Close()
			w = f
		}
		// Hide the Closer so the pcmfile sink cannot close the shared output
		// file between streams; the process owns the file's lifetime.
		out = pcmfile.Capture(nonClosingWriter{w})
	}

	cfg := airplay2.Config{
		Name:           *name,
		ListenAddr:     *listen,
		Interfaces:     ifaces,
		PIN:            *pin,
		PairingsPath:   *pairings,
		Output:         out,
		OutputRate:     *outputRate,
		OutputChannels: *outputChannels,
	}
	if *debug {
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	r, err := airplay2.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "airplay2-receiver: %v\n", err)
		os.Exit(1)
	}
	defer r.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := r.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "airplay2-receiver: run: %v\n", err)
		os.Exit(1)
	}
}

func doctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	var ifaces stringSlice
	fs.Var(&ifaces, "interface", "network interface to check (repeatable; default all)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: airplay2-receiver doctor [flags]\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	cfg := airplay2.DefaultConfig()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "airplay2-receiver: config: %v\n", err)
		os.Exit(1)
	}

	names := ifaces
	if len(names) == 0 {
		ifs, err := net.Interfaces()
		if err != nil {
			fmt.Fprintf(os.Stderr, "airplay2-receiver: interfaces: %v\n", err)
			os.Exit(1)
		}
		for _, ifc := range ifs {
			names = append(names, ifc.Name)
		}
	}

	ok := true
	for _, name := range names {
		ifc, err := net.InterfaceByName(name)
		if err != nil {
			fmt.Printf("%-12s ERROR %v\n", name, err)
			ok = false
			continue
		}
		addrs, _ := ifc.Addrs()
		up := ifc.Flags&net.FlagUp != 0
		multicast := ifc.Flags&net.FlagMulticast != 0
		fmt.Printf("%-12s up=%-5v multicast=%-5v addrs=%v\n", name, up, multicast, addrs)
		if !up {
			ok = false
		}
	}
	if !ok {
		os.Exit(1)
	}
}
