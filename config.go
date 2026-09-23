package airplay2

import (
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/pkar/gap2/pcm"
)

// Config describes a receiver before it is started.
//
// New validates the configuration but performs no I/O and starts no goroutines.
type Config struct {
	// Name is the advertised receiver name. Empty means "AirPlay2 Receiver".
	Name string

	// Interfaces restricts advertisement and listening to named network
	// interfaces. An empty list means all suitable interfaces.
	Interfaces []string

	// ListenAddr is the TCP address for the RTSP-like control service.
	// Empty means ":7000".
	ListenAddr string

	// Output is the PCM sink factory used for each playback stream. It may be
	// nil for a discovery/control-only receiver.
	Output pcm.Factory

	// Logger receives structured diagnostics. Nil means slog.Default.
	Logger *slog.Logger

	// Limits bounds network and memory use. Zero values are replaced by
	// DefaultLimits.
	Limits Limits
}

// Limits bounds network and memory use.
type Limits struct {
	MaxConnections    int
	MaxRequestBytes   int
	MaxHeaderBytes    int
	MaxHeaders        int
	MaxBodyBytes      int
	PairTimeout       time.Duration
	ReadHeaderTimeout time.Duration
}

// DefaultLimits returns conservative receiver limits.
func DefaultLimits() Limits {
	return Limits{
		MaxConnections:    8,
		MaxRequestBytes:   64 << 10,
		MaxHeaderBytes:    32 << 10,
		MaxHeaders:        128,
		MaxBodyBytes:      1 << 20,
		PairTimeout:       2 * time.Minute,
		ReadHeaderTimeout: 30 * time.Second,
	}
}

// DefaultConfig returns a receiver configuration suitable for embedding.
func DefaultConfig() Config {
	return Config{
		Name:       "AirPlay2 Receiver",
		ListenAddr: ":7000",
		Logger:     slog.Default(),
		Limits:     DefaultLimits(),
	}
}

func (c Config) normalized() Config {
	if strings.TrimSpace(c.Name) == "" {
		c.Name = "AirPlay2 Receiver"
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":7000"
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	c.Limits = c.Limits.withDefaults()
	return c
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxConnections <= 0 {
		l.MaxConnections = d.MaxConnections
	}
	if l.MaxRequestBytes <= 0 {
		l.MaxRequestBytes = d.MaxRequestBytes
	}
	if l.MaxHeaderBytes <= 0 {
		l.MaxHeaderBytes = d.MaxHeaderBytes
	}
	if l.MaxHeaders <= 0 {
		l.MaxHeaders = d.MaxHeaders
	}
	if l.MaxBodyBytes <= 0 {
		l.MaxBodyBytes = d.MaxBodyBytes
	}
	if l.PairTimeout <= 0 {
		l.PairTimeout = d.PairTimeout
	}
	if l.ReadHeaderTimeout <= 0 {
		l.ReadHeaderTimeout = d.ReadHeaderTimeout
	}
	return l
}

// Validate checks a raw configuration for syntactic validity. It does not
// perform network or interface I/O.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("%w: name is empty", ErrNotConfigured)
	}
	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		return fmt.Errorf("%w: listen address %q: %v", ErrNotConfigured, c.ListenAddr, err)
	}
	return nil
}
