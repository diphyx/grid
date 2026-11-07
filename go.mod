module github.com/diphyx/grid

go 1.27.3

require (
	github.com/flynn/noise v1.1.0
	github.com/fxamacker/cbor/v2 v2.10.0
	github.com/pelletier/go-toml/v2 v2.4.3
	golang.org/x/crypto v0.58.0
	golang.org/x/sys v0.49.0
)

require github.com/x448/float16 v0.8.4 // indirect

// v0.1.0 was withdrawn: its tag and release were removed, and v1.0.0 replaces it.
retract v0.1.0
