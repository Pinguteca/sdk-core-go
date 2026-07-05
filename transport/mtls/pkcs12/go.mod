module github.com/Pinguteca/sdk-core-go/transport/mtls/pkcs12

go 1.26.4

require (
	github.com/Pinguteca/sdk-core-go v0.0.0-00010101000000-000000000000
	software.sslmate.com/src/go-pkcs12 v0.7.2
)

require golang.org/x/crypto v0.51.0 // indirect

replace github.com/Pinguteca/sdk-core-go => ../../..
