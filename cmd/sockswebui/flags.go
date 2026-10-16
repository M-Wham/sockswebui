package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/M-Wham/sockswebui/internal/api"
)

// defaultDataDir is the data directory. --ui-addr has no default: the stored
// settings.ui_bind_addr decides the bind unless the flag is passed (see uiaddr.go).
const defaultDataDir = "/var/lib/sockswebui"

type options struct {
	ShowVersion    bool
	ResetAdmin     bool // --reset-admin: clear the admin login under the lock and exit
	DataDir        string
	UIAddr         string
	UIAddrExplicit bool     // --ui-addr was passed on the command line
	UIHosts        []string // --ui-host values (repeatable): extra accepted UI names
	DevTestrig     bool     // only settable when built with -tags testrig
}

// uiHostsFlag is the repeatable --ui-host flag; each value must be a bare host name.
type uiHostsFlag struct{ hosts *[]string }

func (f uiHostsFlag) String() string {
	if f.hosts == nil {
		return ""
	}
	return strings.Join(*f.hosts, ",")
}

func (f uiHostsFlag) Set(v string) error {
	if err := api.ValidUIHost(v); err != nil {
		return fmt.Errorf("%q: %w", v, err)
	}
	*f.hosts = append(*f.hosts, v)
	return nil
}

// parseFlags parses command-line arguments (without the program name).
func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("sockswebui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.ShowVersion, "version", false, "print version and exit")
	fs.BoolVar(&o.ResetAdmin, "reset-admin", false,
		"with the service stopped: clear the admin password hash and session secret, keep endpoints and slots, "+
			"print restart instructions and exit")
	fs.StringVar(&o.DataDir, "data-dir", defaultDataDir, "directory holding config.json")
	fs.StringVar(&o.UIAddr, "ui-addr", "",
		"listen address for the web UI; overrides settings.ui_bind_addr when passed (default: the stored setting)")
	fs.Var(uiHostsFlag{&o.UIHosts}, "ui-host",
		"extra accepted UI host name, e.g. the public name behind a reverse proxy (repeatable)")
	registerDevFlags(fs, &o)
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, errors.New("unexpected positional arguments")
	}
	if o.ResetAdmin && o.ShowVersion {
		return o, errors.New("--reset-admin cannot be combined with --version")
	}
	if o.DataDir == "" {
		return o, errors.New("--data-dir must not be empty")
	}
	fs.Visit(func(f *flag.Flag) { o.UIAddrExplicit = o.UIAddrExplicit || f.Name == "ui-addr" })
	if o.UIAddrExplicit && o.UIAddr == "" {
		return o, errors.New("--ui-addr must not be empty")
	}
	return o, nil
}
