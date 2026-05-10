module github.com/Pinguteca/sdk-core-go/presets

go 1.26.3

require (
	connectrpc.com/connect v1.18.1
	github.com/Pinguteca/sdk-core-go v0.0.0-00010101000000-000000000000
	github.com/Pinguteca/sdk-core-go/breaker v0.0.0-00010101000000-000000000000
)

require (
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel v1.34.0 // indirect
	go.opentelemetry.io/otel/metric v1.34.0 // indirect
	go.opentelemetry.io/otel/trace v1.34.0 // indirect
	golang.org/x/oauth2 v0.25.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250115164207-1a7da9e5054f // indirect
	google.golang.org/protobuf v1.36.4 // indirect
)

replace (
	github.com/Pinguteca/sdk-core-go => ../
	github.com/Pinguteca/sdk-core-go/breaker => ../breaker
)
