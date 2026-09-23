// Command embed demonstrates importing the airplay2 package into a host
// application. Construction and Status are side-effect-free; actual receiver
// operation is not implemented yet.
package main

import (
	"fmt"
	"os"

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

	fmt.Printf("receiver state: %s\n", r.Status().State)
	fmt.Println("embed: construction and Status are side-effect-free; Run is not implemented yet")
}
