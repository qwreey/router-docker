package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/qwreey/envmigrate"
)

// envMigrateOpts parameterizes the shared github.com/qwreey/envmigrate package for
// router-manager's own file names - mirrors webmanager/backend/main.go's own
// envMigrateOpts var, same package (github.com/qwreey/envmigrate) either way.
var envMigrateOpts = envmigrate.Options{
	VersionKey:       "ROUTER_ENV_VERSION",
	EnvFileName:      ".env.router",
	TemplateFileName: "example-env.router",
}

// envMigrateCmd implements `router-manager --env-migrate` - reconciles a
// user's .env.router (piped in via stdin) against this image's current
// example-env.router (ROUTER_ENV_TEMPLATE_PATH), writing the reconstructed
// file to stdout and any migration notes to stderr. Mirrors webmanager's own
// envmigratecmd.go exactly - see github.com/qwreey/envmigrate's package doc for
// the full behavior. Meant to be run roughly like:
//
//	cat .env.router >> .env.router.bak && docker compose exec -T code-docker-router \
//	  router-manager --env-migrate < .env.router > .env.router.new \
//	  && mv .env.router.new .env.router
//
// Sequential on purpose, not the older `cat f | tee -a f.bak | ... > f`
// pipeline: every stage of a pipeline starts concurrently, so the final
// `> f` truncation can beat cat's open of f, cat reads nothing, the backup
// gets nothing appended, and this command emits a bare template over the
// real file - ROUTER_MANAGER_AUTH_PASSWORD_HASH and all. `<` never opens the
// original for writing, and the mv only happens once everything succeeded.
// Always exits 0 once it has a template to work from, even on a weirdly-
// shaped input file - worst case the template's own structure still comes
// out, which beats producing nothing - except for an *empty* stdin, which
// is refused below precisely because that is what the race produces.
func envMigrateCmd() int {
	templatePath := os.Getenv("ROUTER_ENV_TEMPLATE_PATH")
	if templatePath == "" {
		templatePath = "/etc/router/example-env.router"
	}

	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "env-migrate: cannot read template at %s: %v\n", templatePath, err)
		return 1
	}

	oldBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "env-migrate: error reading stdin: %v\n", err)
		return 1
	}
	// Same guard as webmanager's envmigratecmd.go: a migration of nothing is
	// only ever the truncate-before-read race (or a typo'd path), and writing
	// the template out would silently reset the file. A new file is a `cp` of
	// example-env.router, not a migration.
	if len(strings.TrimSpace(string(oldBytes))) == 0 {
		fmt.Fprintln(os.Stderr, "env-migrate: stdin is empty - refusing to write a bare template over an existing file. If the input file really is empty, copy example-env.router instead; if you ran the old `cat f | tee | ... > f` one-liner, the redirection emptied f before cat read it - restore from .env.router.bak and use the sequential form in docs/router.md")
		return 1
	}

	res := envmigrate.Migrate(string(oldBytes), string(templateBytes), envMigrateOpts)

	for _, n := range res.Notes {
		fmt.Fprintf(os.Stderr, "env-migrate: %s: %s\n", n.Level, n.Message)
	}

	fmt.Print(res.Output)
	return 0
}
