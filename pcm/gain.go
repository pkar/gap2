package pcm

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync/atomic"
)

// Gain applies an adjustable attenuation to an S16LE sink. Control and audio
// goroutines may update and read the gain concurrently.
type Gain struct {
	Sink
	value atomic.Uint64
}

func NewGain(sink Sink, db float64) *Gain {
	g := &Gain{Sink: sink}
	g.SetDB(db)
	return g
}

// SetDB accepts AirPlay's -144 dB mute value and attenuation up to 0 dB.
func (g *Gain) SetDB(db float64) {
	if math.IsNaN(db) || math.IsInf(db, 0) || db > 0 {
		return
	}
	if sink, ok := g.Sink.(VolumeSink); ok && sink.SetVolumeDB(db) {
		// Preserve source precision until the final output stage.
		g.value.Store(math.Float64bits(1))
		return
	}
	v := 0.0
	if db > -144 {
		v = math.Pow(10, db/20)
	}
	g.value.Store(math.Float64bits(v))
}

func (g *Gain) Write(ctx context.Context, b Block) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.Format.Format != S16LE || b.Format.Channels <= 0 || len(b.Data)%b.Format.BytesPerFrame() != 0 {
		return errors.New("pcm: gain requires complete S16LE frames")
	}
	v := math.Float64frombits(g.value.Load())
	if v == 1 {
		return g.Sink.Write(ctx, b)
	}
	out := Block{Format: b.Format, Data: make([]byte, len(b.Data))}
	for i := 0; i+1 < len(b.Data); i += 2 {
		sample := int16(binary.LittleEndian.Uint16(b.Data[i:]))
		binary.LittleEndian.PutUint16(out.Data[i:], uint16(int16(math.Round(float64(sample)*v))))
	}
	return g.Sink.Write(ctx, out)
}
