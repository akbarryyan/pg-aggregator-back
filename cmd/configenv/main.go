// Command configenv prints the backend config values that shell scripts
// need (scripts/*.sh, flow-test) as `export NAME='value'` lines. It uses
// the same loader and validation as the API, so scripts reject a bad
// config exactly like the server does. Usage from bash (run from back/):
//
//	config_env="$(go run ./cmd/configenv)"
//	eval "$config_env"
//
// The two steps matter: `eval "$(go run ./cmd/configenv)"` would swallow a
// failure, since eval of an empty string returns 0 and set -e never fires.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/akbarryyan/pg-aggregator-back/internal/config"
)

func main() {
	configPath := flag.String("config", ".config.toml", "path to the TOML config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configenv: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(render(cfg))
}

// render emits only the variables the scripts actually read, under their
// pre-TOML env var names so the scripts' bodies didn't need to change.
func render(cfg *config.Config) string {
	vars := []struct{ name, value string }{
		{"DB_HOST", cfg.DB.Host},
		{"DB_PORT", strconv.Itoa(cfg.DB.Port)},
		{"DB_USER", cfg.DB.User},
		{"DB_PASSWORD", cfg.DB.Password},
		{"DB_NAME", cfg.DB.Name},
		{"CASHI_SECRET_KEY", cfg.Cashi.SecretKey},
		{"APP_ENVIRONMENT", cfg.App.Environment},
	}

	var b strings.Builder
	for _, v := range vars {
		fmt.Fprintf(&b, "export %s=%s\n", v.name, shellQuote(v.value))
	}
	return b.String()
}

// shellQuote wraps s in single quotes, inside which POSIX shells treat
// every character literally; an embedded single quote is closed, escaped
// with a backslash, and reopened (see the replacement below).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
