// Package hostedtemplate bundles the maintained source-only app scaffold.
package hostedtemplate

import "embed"

// Assets contains only maintained scaffold assets, including required dotfiles,
// independently of the executable's working directory. Exact file paths exclude
// private working-tree files; TestBundleInventory checks agreement with the
// inventory shared with scaffold.mjs.
//
//go:embed manifest.json inventory.json
//go:embed source/package.json source/package-lock.json source/.npmrc source/.gitignore
//go:embed source/tsconfig.web.json source/tsconfig.service.json
//go:embed source/scripts/build.mjs source/scripts/browser-smoke.mjs
//go:embed source/src/web/App.tsx source/src/web/main.tsx source/src/web/style.css source/src/web/assets.d.ts
//go:embed source/src/service/main.ts source/src/service/migrate.ts source/src/service/runtime.mjs source/src/service/environment.ts
//go:embed source/src/service/database.ts source/src/service/migrations.ts source/src/service/health.ts
//go:embed source/sql/migrations/0001_preferences.sql source/test/app.test.mjs source/test/service.test.mjs
var Assets embed.FS
