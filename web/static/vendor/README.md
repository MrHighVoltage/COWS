# Vendored browser assets

Milestone 0 serves browser dependencies from the COWS binary. Production does
not load assets from a CDN.

| Asset | Version | Source | License | SHA-256 |
| --- | --- | --- | --- | --- |
| HTMX | 2.0.10 | <https://cdn.jsdelivr.net/npm/htmx.org@2.0.10/dist/htmx.min.js> | BSD-2-Clause | `71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de` |
| Alpine.js | 3.14.9 | <https://cdn.jsdelivr.net/npm/alpinejs@3.14.9/dist/cdn.min.js> | MIT | `3ed1eed252488921df65e363d6715deb04d7f92aaedb9e52199fdf73cb1e0ad3` |
| xterm.js | 5.3.0 | <https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js> | MIT | `f0aea0f75f48559013ae6643c2479dd737d26da42d5524e6d2b70915ae6523c7` |
| xterm.js stylesheet | 5.3.0 | <https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css> | MIT | `832f3f2c603b43ad4351ff04970150cc7a873014276db126a6065c6dd81e4872` |
| xterm-addon-fit | 0.8.0 | <https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js> | MIT | `10f3194c5f17c1786fb7d5db865c1ec8539b6736a318063fd38bdaaf7c46848f` |

The noVNC 1.6.0 `core/` browser modules are vendored under `novnc/core/`.
They are loaded as local ES modules by the COWS desktop page; the stock noVNC
application UI is not included. Source: <https://github.com/novnc/noVNC/tree/v1.6.0/core>.
License: MPL-2.0, with the upstream license text in `novnc/LICENSE.txt`.
The noVNC core also includes its required pako zlib modules under
`novnc/vendor/pako/`; those files are MIT licensed and retain the upstream
`vendor/pako/LICENSE` text.

The xterm.js fit addon `0.8.0` is vendored as `xterm/xterm-addon-fit.js` with
its MIT license in `xterm/xterm-addon-fit.LICENSE`. It is loaded locally by the
terminal page to calculate terminal rows and columns without a frontend build
step. Source: <https://github.com/xtermjs/xterm-addon-fit/tree/0.8.0>.

The source URLs are recorded for reproducibility. Run
`tools/web-assets.sh verify` for an offline checksum check. Run
`tools/web-assets.sh update` only when deliberately refreshing the pinned
versions; review the resulting lock-file checksum changes, release notes, and
licenses before committing. The update tool uses curl, tar, sha256sum, and the
font subsetting tools for the embedded font; none are required to build or run
COWS. The noVNC tree and embedded font are included in
`web/static/assets.lock` alongside the standalone JavaScript assets.
