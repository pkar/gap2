// Command airplay2-receiver is a thin standalone wrapper over the airplay2
// package. Policy (flags, signals, diagnostics) lives here; protocol and
// audio work belongs to the library.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	airplay2 "github.com/pkar/gap2"
)

const version = "0.1.0-dev"

// stringSlice collects repeated flag values.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

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
	var ifaces stringSlice
	fs.Var(&ifaces, "interface", "network interface (repeatable)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: airplay2-receiver run [flags]\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	cfg := airplay2.Config{
		Name:       *name,
		ListenAddr: *listen,
		Interfaces: ifaces,
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
