// Package compression registers Brotli and Zstd compressors with Connect-Go,
// adds Brotli as the default Send compression, and leaves the Connect-Go
// default Gzip support in place. Three algorithms cover the realistic
// deployment matrix:
//
//   - Brotli: best ratio for text-heavy payloads (JSON, debug-encoded protobuf);
//     ~95%+ proxy and CDN acceptance. Default for sends so payloads stay small
//     without per-call configuration.
//   - Zstd: best speed/ratio trade-off for binary payloads or high-throughput
//     server-to-server traffic. ~70% server-side acceptance as of 2026 — used
//     when the server advertises `zstd` in Accept-Encoding.
//   - Gzip: universal compatibility fallback. Connect-Go registers it by
//     default on both client and handler; we leave that registration alone.
//
// Connect content negotiation picks a mutually supported algorithm based on
// the Content-Encoding the client sends and the Accept-Encoding the server
// advertises. Registering Brotli and Zstd on both sides covers every pair.
//
// Wire client-side via [ClientOptions] when constructing a Connect client and
// server-side via [HandlerOptions] when registering a service handler.
package compression

import (
	"errors"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// Encoding names as advertised in Content-Encoding / Accept-Encoding.
const (
	NameBrotli = "br"
	NameZstd   = "zstd"
	NameGzip   = "gzip"
)

// brotliQuality balances ratio against CPU. Brotli levels run 0..11; level 4
// gives ~90% of best-level ratio at a fraction of the cost. Sweet spot for
// streaming RPC traffic where compression and decompression both happen on
// the request path.
const brotliQuality = 4

// ClientOptions registers Brotli and Zstd with the Connect client and selects
// Brotli as the default Send compression. Connect-Go registers Gzip by default,
// so all three algorithms are available for content negotiation. Servers that
// do not support Brotli fall back to Zstd or Gzip via Accept-Encoding.
func ClientOptions() []connect.ClientOption {
	return []connect.ClientOption{
		connect.WithAcceptCompression(NameBrotli, newBrotliDecompressor, newBrotliCompressor),
		connect.WithAcceptCompression(NameZstd, newZstdDecompressor, newZstdCompressor),
		connect.WithSendCompression(NameBrotli),
	}
}

// HandlerOptions registers Brotli and Zstd on a Connect handler so inbound
// requests can arrive in any of Brotli/Zstd/Gzip and outbound responses can
// use whichever the caller advertises in Accept-Encoding.
func HandlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{
		connect.WithCompression(NameBrotli, newBrotliDecompressor, newBrotliCompressor),
		connect.WithCompression(NameZstd, newZstdDecompressor, newZstdCompressor),
	}
}

// ---------------------------------------------------------------------------
// Brotli
// ---------------------------------------------------------------------------

func newBrotliCompressor() connect.Compressor {
	// brotli.Writer satisfies the io.Writer + Close + Reset(io.Writer) shape
	// expected by connect.Compressor without further wrapping. The initial
	// io.Discard target is replaced by Connect via Reset before any write.
	return brotli.NewWriterLevel(io.Discard, brotliQuality)
}

type brotliDecompressor struct {
	r *brotli.Reader
}

func (d *brotliDecompressor) Read(p []byte) (int, error) {
	if d.r == nil {
		return 0, io.EOF
	}
	n, err := d.r.Read(p)
	if err == nil {
		return n, nil
	}
	// Return canonical io.EOF unwrapped because [io.ReadAll] and similar
	// callers compare with == rather than errors.Is. Other errors are wrapped
	// for context.
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, fmt.Errorf("compression: brotli read: %w", err)
}

func (d *brotliDecompressor) Reset(r io.Reader) error {
	if d.r == nil {
		d.r = brotli.NewReader(r)
		return nil
	}
	if err := d.r.Reset(r); err != nil {
		return fmt.Errorf("compression: brotli reset: %w", err)
	}
	return nil
}

func (d *brotliDecompressor) Close() error {
	d.r = nil
	return nil
}

func newBrotliDecompressor() connect.Decompressor {
	return &brotliDecompressor{}
}

// ---------------------------------------------------------------------------
// Zstd
// ---------------------------------------------------------------------------

func newZstdCompressor() connect.Compressor {
	// zstd.NewWriter only fails when given options it cannot satisfy; with no
	// options the only failure mode is an OOM the runtime would already have
	// surfaced, so the error is unreachable here.
	enc, _ := zstd.NewWriter(io.Discard)
	return enc
}

type zstdDecompressor struct {
	d *zstd.Decoder
}

func (d *zstdDecompressor) Read(p []byte) (int, error) {
	if d.d == nil {
		return 0, io.EOF
	}
	n, err := d.d.Read(p)
	if err == nil {
		return n, nil
	}
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, fmt.Errorf("compression: zstd read: %w", err)
}

func (d *zstdDecompressor) Reset(r io.Reader) error {
	if d.d == nil {
		dec, err := zstd.NewReader(r)
		if err != nil {
			return fmt.Errorf("compression: zstd reader init: %w", err)
		}
		d.d = dec
		return nil
	}
	if err := d.d.Reset(r); err != nil {
		return fmt.Errorf("compression: zstd reset: %w", err)
	}
	return nil
}

func (d *zstdDecompressor) Close() error {
	if d.d != nil {
		d.d.Close() // klauspost zstd Decoder.Close has no error return
		d.d = nil
	}
	return nil
}

func newZstdDecompressor() connect.Decompressor {
	return &zstdDecompressor{}
}
