package database

import (
	"bytes"
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
)

var nzbCompressionBenchmarkSink []byte

func TestCompressNZBXMLRoundTrip(t *testing.T) {
	src := benchmarkNZBXML(1 << 20)
	compressed := compressNZBXML(src)
	if !IsNZBXMLCompressed(compressed) {
		t.Fatal("compressed NZB does not contain zstd frame magic")
	}
	if len(compressed) >= len(src) {
		t.Fatalf("compressed size = %d, want less than source size %d", len(compressed), len(src))
	}

	decoded, err := decompressNZBXML(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, src) {
		t.Fatal("decompressed NZB differs from source")
	}
}

func TestCompressNZBXMLSupportsConcurrentCallers(t *testing.T) {
	src := benchmarkNZBXML(256 << 10)
	const callers = 16
	start := make(chan struct{})
	errs := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			ready.Done()
			<-start
			compressed := compressNZBXML(src)
			decoded, err := decompressNZBXML(compressed)
			if err == nil && !bytes.Equal(decoded, src) {
				err = fmt.Errorf("decompressed NZB differs from source")
			}
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

type nzbEncoderBenchmarkVariant struct {
	name      string
	warmCalls int
	options   []zstd.EOption
}

func nzbEncoderBenchmarkVariants() []nzbEncoderBenchmarkVariant {
	defaultConcurrency := runtime.GOMAXPROCS(0)
	return []nzbEncoderBenchmarkVariant{
		{
			name:      "default",
			warmCalls: defaultConcurrency,
			options:   []zstd.EOption{zstd.WithEncoderLevel(zstd.SpeedDefault)},
		},
		{
			name:      "concurrency-1",
			warmCalls: 1,
			options: []zstd.EOption{
				zstd.WithEncoderLevel(zstd.SpeedDefault),
				zstd.WithEncoderConcurrency(1),
			},
		},
		{
			name:      "lower-memory",
			warmCalls: defaultConcurrency,
			options: []zstd.EOption{
				zstd.WithEncoderLevel(zstd.SpeedDefault),
				zstd.WithLowerEncoderMem(true),
			},
		},
		{
			name:      "concurrency-1-lower-memory",
			warmCalls: 1,
			options: []zstd.EOption{
				zstd.WithEncoderLevel(zstd.SpeedDefault),
				zstd.WithEncoderConcurrency(1),
				zstd.WithLowerEncoderMem(true),
			},
		},
	}
}

// BenchmarkNZBZstdEncoderOptions compares memory retained by a warmed encoder,
// single-call throughput, allocations, and compressed size. The payload models
// an NZB near production's estimated p99 uncompressed size.
func BenchmarkNZBZstdEncoderOptions(b *testing.B) {
	src := benchmarkNZBXML(4 << 20)
	for _, variant := range nzbEncoderBenchmarkVariants() {
		b.Run(variant.name, func(b *testing.B) {
			encoder, retained, compressedSize := warmedBenchmarkEncoder(b, src, variant.options, variant.warmCalls)
			b.ReportAllocs()
			b.SetBytes(int64(len(src)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				nzbCompressionBenchmarkSink = encoder.EncodeAll(src, make([]byte, 0, len(src)/4))
			}
			runtime.KeepAlive(encoder)
			b.ReportMetric(float64(retained)/(1<<20), "retained-MiB")
			b.ReportMetric(100*float64(compressedSize)/float64(len(src)), "size-percent")
		})
	}
}

// BenchmarkNZBZstdEncoderOptionsParallel measures aggregate throughput when
// concurrent workflow jobs share the package-level encoder.
func BenchmarkNZBZstdEncoderOptionsParallel(b *testing.B) {
	src := benchmarkNZBXML(4 << 20)
	for _, variant := range nzbEncoderBenchmarkVariants() {
		b.Run(variant.name, func(b *testing.B) {
			encoder, err := zstd.NewWriter(nil, variant.options...)
			if err != nil {
				b.Fatal(err)
			}
			compressedSize := 0
			for i := 0; i < variant.warmCalls; i++ {
				compressed := encoder.EncodeAll(src, nil)
				compressedSize = len(compressed)
			}
			runtime.GC()
			b.ReportAllocs()
			b.SetBytes(int64(len(src)))
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				var dst []byte
				for pb.Next() {
					dst = encoder.EncodeAll(src, make([]byte, 0, len(src)/4))
				}
				runtime.KeepAlive(dst)
			})
			runtime.KeepAlive(encoder)
			b.ReportMetric(100*float64(compressedSize)/float64(len(src)), "size-percent")
		})
	}
}

func warmedBenchmarkEncoder(b *testing.B, src []byte, options []zstd.EOption, warmCalls int) (*zstd.Encoder, uint64, int) {
	b.Helper()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	encoder, err := zstd.NewWriter(nil, options...)
	if err != nil {
		b.Fatal(err)
	}
	compressedSize := 0
	for i := 0; i < warmCalls; i++ {
		compressed := encoder.EncodeAll(src, nil)
		compressedSize = len(compressed)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(encoder)

	if after.HeapAlloc <= before.HeapAlloc {
		return encoder, 0, compressedSize
	}
	return encoder, after.HeapAlloc - before.HeapAlloc, compressedSize
}

func benchmarkNZBXML(targetBytes int) []byte {
	var out bytes.Buffer
	out.Grow(targetBytes + (16 << 10))
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"><head><meta type="category">TV</meta></head>`)

	state := uint64(0x9e3779b97f4a7c15)
	for file := 0; out.Len() < targetBytes; file++ {
		fmt.Fprintf(&out, `<file poster="poster%d@example.invalid" date="1700000000" subject="Example.Show.S01E%02d.1080p.WEB-DL-GROUP.part%03d.rar yEnc"><groups><group>alt.binaries.test</group></groups><segments>`, file%17, file%24+1, file)
		for segment := 1; segment <= 100 && out.Len() < targetBytes; segment++ {
			var messageID [40]byte
			for i := range messageID {
				state ^= state << 13
				state ^= state >> 7
				state ^= state << 17
				messageID[i] = "abcdefghijklmnopqrstuvwxyz0123456789"[state%36]
			}
			fmt.Fprintf(&out, `<segment bytes="768000" number="%d">%s@example.invalid</segment>`, segment, messageID[:])
		}
		out.WriteString(`</segments></file>`)
	}
	out.WriteString(`</nzb>`)
	return out.Bytes()
}
