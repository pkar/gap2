// Command embed demonstrates importing the airplay2 package into a host
// application: construct a receiver, run it until interrupted, and print the
// final state on shutdown. Decoded PCM is written to stdout as raw interleaved
// samples so it can be captured with a redirect; status messages go to stderr
// so the two streams stay separate.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	airplay2 "github.com/pkar/gap2"
	"github.com/pkar/gap2/output/pcmfile"
	"github.com/pkar/gap2/pcm"
)

func main() {
	format := pcm.Format{Rate: 48000, Channels: 2, Format: pcm.S16LE}
	cfg := airplay2.Config{
		Name:       "EmbeddedReceiver",
		ListenAddr: ":7000",
		Output:     pcmfile.Factory(os.Stdout, format),
	}

	r, err := airplay2.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "embed: %v\n", err)
		os.Exit(1)
	}
	defer r.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "embed: receiver listening on %s (state %s)\n", cfg.ListenAddr, r.Status().State)
	if err := r.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "embed: run: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "embed: stopped (state %s)\n", r.Status().State)
}
