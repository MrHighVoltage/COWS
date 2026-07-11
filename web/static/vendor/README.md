# Vendored browser assets

Milestone 0 serves browser dependencies from the COWS binary. Production does
not load assets from a CDN.

| Asset | Version | Source | License | SHA-256 |
| --- | --- | --- | --- | --- |
| HTMX | 2.0.10 | <https://cdn.jsdelivr.net/npm/htmx.org@2.0.10/dist/htmx.min.js> | BSD-2-Clause | `71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de` |
| Alpine.js | 3.14.9 | <https://cdn.jsdelivr.net/npm/alpinejs@3.14.9/dist/cdn.min.js> | MIT | `3ed1eed252488921df65e363d6715deb04d7f92aaedb9e52199fdf73cb1e0ad3` |

The source URLs are recorded for reproducibility. Before an asset update, read
the upstream release notes and license, replace the file, recompute its SHA-256
checksum, and run `go test ./...`, `go vet ./...`, and `go build ./...`.
