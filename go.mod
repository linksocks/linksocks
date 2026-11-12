module github.com/linksocks/linksocks

go 1.25.0

// These versions were tagged by mistake during development and are superseded
// by v1.14.0, which is the first release of that work. Module proxies cache
// versions permanently, so deleting their tags was not enough: retract keeps
// them out of resolution. Go reads retractions from the highest version only,
// which is why they live in this cleanup release.
retract (
	v1.11.0 // superseded by v1.14.0 before release
	v1.11.1 // accidentally tagged
	v1.12.0 // accidentally tagged
	v1.13.0 // accidentally tagged
	v1.13.1 // accidentally tagged
)

require (
	github.com/flynn/noise v1.1.0
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/huin/goupnp v1.3.0
	github.com/miekg/dns v1.1.73
	github.com/quic-go/quic-go v0.59.1
	github.com/rs/zerolog v1.33.0
	github.com/spf13/cobra v1.8.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.11.1
	golang.org/x/crypto v0.54.0
	golang.org/x/net v0.57.0
	golang.org/x/sys v0.47.0
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
