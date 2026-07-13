# Vendored browser assets

Milestone 0 serves browser dependencies from the COWS binary. Production does
not load assets from a CDN.

| Asset | Version | Source | License | SHA-256 |
| --- | --- | --- | --- | --- |
| HTMX | 2.0.10 | <https://cdn.jsdelivr.net/npm/htmx.org@2.0.10/dist/htmx.min.js> | BSD-2-Clause | `71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de` |
| Alpine.js | 3.14.9 | <https://cdn.jsdelivr.net/npm/alpinejs@3.14.9/dist/cdn.min.js> | MIT | `3ed1eed252488921df65e363d6715deb04d7f92aaedb9e52199fdf73cb1e0ad3` |
| xterm.js | 5.3.0 | <https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js> | MIT | `f0aea0f75f48559013ae6643c2479dd737d26da42d5524e6d2b70915ae6523c7` |
| xterm.js stylesheet | 5.3.0 | <https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css> | MIT | `832f3f2c603b43ad4351ff04970150cc7a873014276db126a6065c6dd81e4872` |

The noVNC 1.6.0 `core/` browser modules are vendored under `novnc/core/`.
They are loaded as local ES modules by the COWS desktop page; the stock noVNC
application UI is not included. Source: <https://github.com/novnc/noVNC/tree/v1.6.0/core>.
License: MPL-2.0, with the upstream license text in `novnc/LICENSE.txt`.
The noVNC core also includes its required pako zlib modules under
`novnc/vendor/pako/`; those files are MIT licensed and retain the upstream
`vendor/pako/LICENSE` text.

The source URLs are recorded for reproducibility. Before an asset update, read
the upstream release notes and license, replace the file, recompute its SHA-256
checksum, and run `go test ./...`, `go vet ./...`, and `go build ./...`.
