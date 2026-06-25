module github.com/sopranoworks/gityard

go 1.26.2

require (
	github.com/go-git/go-billy/v6 v6.0.0-alpha.1
	github.com/go-git/go-git/v6 v6.0.0-alpha.4
	github.com/gorilla/websocket v1.5.3
	github.com/modelcontextprotocol/go-sdk v1.6.0
	github.com/sopranoworks/shoka/pkg v0.0.0
	go.etcd.io/bbolt v1.4.3
	go.yaml.in/yaml/v4 v4.0.0-rc.2
	golang.org/x/crypto v0.52.0
	golang.org/x/sync v0.20.0
)

require (
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/boombuler/barcode v1.0.1-0.20190219062509-6c824513bacc // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/emirpasic/gods v1.18.1 // indirect
	github.com/fxamacker/cbor/v2 v2.9.2 // indirect
	github.com/go-git/gcfg/v2 v2.0.2 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/go-webauthn/webauthn v0.17.4 // indirect
	github.com/go-webauthn/x v0.2.6 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kevinburke/ssh_config v1.6.0 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	github.com/philhofer/fwd v1.2.0 // indirect
	github.com/pjbgf/sha1cd v0.6.0 // indirect
	github.com/pquerna/otp v1.5.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/tinylib/msgp v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)

// The Shoka core (pkg/) submodule is local-only — not yet pushed or tagged (Shape A).
// During development GitYard consumes it via this replace into the sibling checkout.
// Remove once pkg/ is published and pin a real version.
replace github.com/sopranoworks/shoka/pkg => ../shoka/pkg
