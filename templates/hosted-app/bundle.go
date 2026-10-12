// Package hostedtemplate bundles the maintained source-only app scaffold.
package hostedtemplate

import "embed"

// Assets includes dotfiles and is independent of the executable's working directory.
// inventory.json is shared with scaffold.mjs and selects the generated files.
//
//go:embed manifest.json inventory.json all:source
var Assets embed.FS
