module github.com/sopranoworks/gityard

go 1.26.2

require (
	github.com/gorilla/websocket v1.5.3
	github.com/modelcontextprotocol/go-sdk v1.6.0
	github.com/sopranoworks/shoka/pkg v0.0.0
	go.yaml.in/yaml/v4 v4.0.0-rc.2
	golang.org/x/sync v0.20.0
)

require (
	github.com/boombuler/barcode v1.0.1-0.20190219062509-6c824513bacc // indirect
	github.com/fxamacker/cbor/v2 v2.9.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/webauthn v0.17.4 // indirect
	github.com/go-webauthn/x v0.2.6 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/pquerna/otp v1.5.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.etcd.io/bbolt v1.4.3 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
)

// The Shoka core (pkg/) submodule is local-only — not yet pushed or tagged (Shape A).
// During development GitYard consumes it via this replace into the sibling checkout.
// Remove once pkg/ is published and pin a real version.
replace github.com/sopranoworks/shoka/pkg => ../shoka/pkg
