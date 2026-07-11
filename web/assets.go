package webassets

import "embed"

// Files contains the complete browser-facing asset tree so the binary can be
// installed without a separate web-root directory.
//
//go:embed templates static
var Files embed.FS
