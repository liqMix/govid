package ebiv

import (
	"bytes"
	"testing"
)

func benchDecode(b *testing.B, opts ...EncoderOption) {
	cfg := Config{Width: 640, Height: 480, FPSNum: 30, FPSDen: 1}
	g := geometryFor(cfg.Width, cfg.Height)
	var buf bytes.Buffer
	enc, _ := NewEncoder(&buf, cfg, opts...)
	for i := 0; i < 8; i++ {
		enc.WriteFrame(synthPan(g, i))
	}
	enc.Close()
	container := buf.Bytes()

	d, _ := NewDemuxer(bytes.NewReader(container))
	c := NewCodec()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pkt, err := d.NextPacket()
		if err != nil {
			d.Seek(0)
			pkt, _ = d.NextPacket()
		}
		if _, err := c.Decode(pkt); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeIntra(b *testing.B)      { benchDecode(b, WithIntra(20)) }
func BenchmarkDecodeInter(b *testing.B)      { benchDecode(b, WithIntra(20), WithGOP(8)) }
func BenchmarkDecodeIntraTiled(b *testing.B) { benchDecode(b, WithIntra(20), WithTiles(4, 4)) }
