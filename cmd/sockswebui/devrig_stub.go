//go:build !testrig

package main

import (
	"context"
	"flag"
	"log/slog"

	"github.com/M-Wham/sockswebui/internal/api"
	"github.com/M-Wham/sockswebui/internal/config"
)

// devTestrigCompiled reports whether --dev-testrig exists in this build.
const devTestrigCompiled = false

// registerDevFlags registers nothing in release builds: --dev-testrig is unknown.
func registerDevFlags(*flag.FlagSet, *options) {}

// startDevRig is a no-op in release builds: no rig, no dev API routes.
func startDevRig(context.Context, options, *config.Store, *slog.Logger) (func(), []api.Option, error) {
	return func() {}, nil, nil
}
