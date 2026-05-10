module github.com/Pinguteca/sdk-core-go/compression

go 1.26.3

require (
	connectrpc.com/connect v1.18.1
	github.com/andybalholm/brotli v1.2.1
	github.com/klauspost/compress v1.18.6
)

require google.golang.org/protobuf v1.34.2 // indirect

replace github.com/Pinguteca/sdk-core-go => ../
