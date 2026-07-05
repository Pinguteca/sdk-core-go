module github.com/Pinguteca/sdk-core-go/compression

go 1.26.4

require (
	connectrpc.com/connect v1.20.0
	github.com/andybalholm/brotli v1.2.2
	github.com/klauspost/compress v1.18.6
)

require google.golang.org/protobuf v1.36.11 // indirect

replace github.com/Pinguteca/sdk-core-go => ../
