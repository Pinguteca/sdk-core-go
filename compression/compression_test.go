package compression

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

// sample is a string with realistic redundancy so each algorithm has something
// to compress. ~3KB so the compressed output diverges meaningfully from input.
var sample = strings.Repeat("Pinguteca SDK compression round-trip test payload. ", 64)

func TestClientOptions(t *testing.T) {
	t.Parallel()
	opts := ClientOptions()
	// Brotli accept, Zstd accept, Send=Brotli. Gzip is registered by Connect by default.
	if got, want := len(opts), 3; got != want {
		t.Fatalf("len(ClientOptions()) = %d, want %d", got, want)
	}
}

func TestHandlerOptions(t *testing.T) {
	t.Parallel()
	opts := HandlerOptions()
	if got, want := len(opts), 2; got != want {
		t.Fatalf("len(HandlerOptions()) = %d, want %d", got, want)
	}
}

func TestBrotliRoundTrip(t *testing.T) {
	t.Parallel()
	roundTrip(t, newBrotliCompressor(), newBrotliDecompressor())
}

func TestZstdRoundTrip(t *testing.T) {
	t.Parallel()
	roundTrip(t, newZstdCompressor(), newZstdDecompressor())
}

// roundTrip exercises a (Compressor, Decompressor) pair: compress -> decompress
// yields the original bytes, then re-uses both ends to round-trip a second
// payload. The reuse step matters because Connect pools compressors and
// invokes Reset between requests rather than allocating fresh ones.
func roundTrip(t *testing.T, comp connect.Compressor, decomp connect.Decompressor) {
	t.Helper()
	defer func() {
		_ = comp.Close()
		_ = decomp.Close()
	}()

	input := []byte(sample)
	var compressed bytes.Buffer

	comp.Reset(&compressed)
	if _, err := comp.Write(input); err != nil {
		t.Fatalf("compress write: %v", err)
	}
	if err := comp.Close(); err != nil {
		t.Fatalf("compress close: %v", err)
	}
	if compressed.Len() == 0 {
		t.Fatalf("compressed output is empty")
	}
	if compressed.Len() >= len(input) {
		t.Fatalf("compressed (%d) >= input (%d): payload should compress", compressed.Len(), len(input))
	}

	if err := decomp.Reset(&compressed); err != nil {
		t.Fatalf("decompress reset: %v", err)
	}
	output, err := io.ReadAll(decomp)
	if err != nil {
		t.Fatalf("decompress read: %v", err)
	}
	if !bytes.Equal(input, output) {
		t.Fatalf("round-trip mismatch: input=%d bytes, output=%d bytes", len(input), len(output))
	}

	// Reuse: reset both ends and round-trip a different payload. Connect pools
	// these and calls Reset between requests; verifying reuse here protects
	// against state leaking between RPC handlings.
	other := []byte(strings.Repeat("second payload, second payload, ", 32))
	compressed.Reset()
	comp.Reset(&compressed)
	if _, err := comp.Write(other); err != nil {
		t.Fatalf("reuse compress: %v", err)
	}
	if err := comp.Close(); err != nil {
		t.Fatalf("reuse close: %v", err)
	}
	if err := decomp.Reset(&compressed); err != nil {
		t.Fatalf("reuse decompress reset: %v", err)
	}
	output, err = io.ReadAll(decomp)
	if err != nil {
		t.Fatalf("reuse decompress read: %v", err)
	}
	if !bytes.Equal(other, output) {
		t.Fatalf("reuse mismatch")
	}
}
