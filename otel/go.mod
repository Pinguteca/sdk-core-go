module github.com/Pinguteca/sdk-core-go/otel

go 1.26.3

require (
	connectrpc.com/connect v1.19.2
	github.com/Pinguteca/sdk-core-go v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
	go.opentelemetry.io/otel v1.34.0
	go.opentelemetry.io/otel/trace v1.34.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel/metric v1.34.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260511170946-3700d4141b60 // indirect
)

replace github.com/Pinguteca/sdk-core-go => ../
