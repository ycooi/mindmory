# Third-party notices

Mindmory itself is licensed under the MIT License in `LICENSE`. Its compiled
Go dependencies remain under their respective upstream terms.

`THIRD_PARTY_LICENSES.txt` contains the complete Go runtime/standard-library
license and patent notice plus the complete license and notice files shipped
by every non-standard Go module compiled into the release binaries. It is
included in every binary distribution and must be retained when redistributing
those binaries.

The compiled module inventory for release 0.1.2 is:

| Module | Version |
| --- | --- |
| `github.com/dustin/go-humanize` | `v1.0.1` |
| `github.com/google/jsonschema-go` | `v0.4.3` |
| `github.com/google/uuid` | `v1.6.0` |
| `github.com/mattn/go-isatty` | `v0.0.24` |
| `github.com/modelcontextprotocol/go-sdk` | `v1.6.1` |
| `github.com/ncruces/go-strftime` | `v1.0.0` |
| `github.com/remyoudompheng/bigfft` | `v0.0.0-20230129092748-24d4a6f8daec` |
| `github.com/segmentio/asm` | `v1.1.3` |
| `github.com/segmentio/encoding` | `v0.5.4` |
| `github.com/yosida95/uritemplate/v3` | `v3.0.2` |
| `golang.org/x/oauth2` | `v0.35.0` |
| `golang.org/x/sys` | `v0.47.0` |
| `modernc.org/libc` | `v1.74.4` |
| `modernc.org/mathutil` | `v1.7.1` |
| `modernc.org/memory` | `v1.11.0` |
| `modernc.org/sqlite` | `v1.57.0` |

The Go runtime and standard library are not separate modules in the dependency
graph; their exact build-toolchain version is recorded in the compendium and
in Go binary build metadata. The authoritative dependency versions and
cryptographic module checksums remain in `go.mod` and `go.sum`.
