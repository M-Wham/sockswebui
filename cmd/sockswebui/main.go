// Command sockswebui serves the sockswebui web interface.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/M-Wham/sockswebui/internal/api"
	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
	"github.com/M-Wham/sockswebui/web"
)

// version is overridden at build time: -ldflags "-X main.version=...".
var version = "dev"

// onListen, when non-nil, is called with the bound UI address (test hook).
var onListen func(addr string)

// onAutostart, when non-nil, is called with the manager right after slot autostart (test hook).
var onAutostart func(m *supervisor.Manager)

// lockDir takes the exclusive data-dir lock; only tests replace it.
var lockDir = config.LockDir

// serverLimits bounds UI connections. There is deliberately no write timeout:
// it would cut off long-lived SSE streams (the events handler clears its read deadline).
type serverLimits struct {
	read, idle time.Duration
	maxHeader  int
}

// uiServerLimits is the UI server's connection limits; only tests change it.
var uiServerLimits = serverLimits{read: 30 * time.Second, idle: 120 * time.Second, maxHeader: 16 << 10}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, web.DistFS())
	stop()
	os.Exit(code)
}

// run is the testable entrypoint; it returns the process exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, uiFS fs.FS) int {
	opts, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "sockswebui:", err)
		return 2
	}
	if opts.ShowVersion {
		fmt.Fprintln(stdout, "sockswebui", version)
		return 0
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	if err := ensureDataDir(opts.DataDir); err != nil {
		log.Error("data dir", "err", err)
		return 1
	}
	if opts.ResetAdmin {
		return resetAdmin(opts.DataDir, stdout, log)
	}
	// The lock spans the store's whole life: quarantine, salvage, every write and the
	// shutdown writes of mgr.Close all happen under it.
	release, ok := acquireDataDir(opts.DataDir, log)
	if !ok {
		return 1
	}
	defer release() // outermost: runs after mgr.Close and the UI shutdown
	store, err := openStore(opts.DataDir, log)
	if err != nil {
		log.Error("cannot load config; refusing to start", "file", config.ConfigFileName, "err", err)
		return 1
	}
	stopRig, rigRoutes, err := startDevRig(ctx, opts, store, log)
	if err != nil {
		log.Error("dev testrig", "err", err)
		return 1
	}
	defer stopRig() // runs after mgr.Close
	// The UI listener opens before slot autostart: a slot configured on the
	// UI port then fails with "port in use", and a UI listen failure starts no slot.
	uiAddr, uiSource := resolveUIAddr(opts, store.Get())
	ln, ok := listenUI(uiAddr, uiSource, store.Get().Settings.UIBindAddr, log)
	if !ok {
		return 1
	}
	mgr := supervisor.New(supervisor.Options{Config: store.Get(), Logger: log})
	defer mgr.Close() // after the UI server: stops every slot
	// Boot autostart of enabled slots; failures surface as slot ERROR states.
	if err := mgr.Run(); err != nil {
		log.Warn("autostart", "err", err)
	}
	if onAutostart != nil {
		onAutostart(mgr)
	}
	names := bootNames(os.Hostname, opts.UIHosts, log)
	apiOpts := append([]api.Option{api.WithUIBind(uiAddr, uiSource)}, rigRoutes...)
	if err := serveUI(ctx, ln, uiSource, newHandler(store, log, mgr, uiFS, names, apiOpts...), log); err != nil {
		log.Error("ui server", "err", err)
		return 1
	}
	return 0
}

// bootNames builds the accepted UI name list once at boot. hostname is read
// exactly once; if it fails or is not a usable DNS name, a WARN is logged and the list
// falls back to IP literals, localhost and the --ui-host names, never to "allow all".
func bootNames(hostname func() (string, error), uiHosts []string, log *slog.Logger) api.NameList {
	h, err := hostname()
	switch {
	case err != nil:
		log.Warn("cannot read device hostname; UI accepts only IP addresses, localhost and --ui-host names",
			"err", err)
		h = ""
	case api.ValidUIHost(h) != nil:
		log.Warn("device hostname is not a valid DNS name; UI accepts only IP addresses, localhost and "+
			"--ui-host names", "hostname", h)
		h = ""
	}
	return api.NewNameList(h, uiHosts...)
}

// ensureDataDir creates dir (mode 0700) if missing. Nothing is written into it.
func ensureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return nil
}

// openStore opens <dataDir>/config.json. A corrupt file is quarantined by the store and
// reported via LoadError. A file that exists but cannot be read, or a corrupt
// file that cannot be quarantined, is returned as an error so the caller exits non-zero;
// the file is left untouched. Errors from the store never
// contain config values, so no secrets reach the log.
func openStore(dataDir string, log *slog.Logger) (*config.Store, error) {
	store, err := config.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if msg := store.LoadError(); msg != "" {
		log.Error("config.json not loaded; slots and endpoints start from defaults, the old config is kept "+
			"in the quarantine file named in detail", "detail", msg)
		if !store.CredentialsSalvaged() {
			log.Warn("admin credentials could not be recovered from the corrupt config; setup is open — " +
				"complete POST /api/setup promptly, anyone who can reach the UI can claim it")
		}
	}
	return store, nil
}

// serveUI serves h on ln until ctx is done, then shuts down gracefully. It owns ln.
// source (flag|setting) is reported in the "ui listening" line.
func serveUI(ctx context.Context, ln net.Listener, source string, h http.Handler, log *slog.Logger) error {
	// Request contexts derive from base; cancelling it ends long-lived SSE streams
	// so Shutdown does not wait on them.
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	if c, ok := h.(io.Closer); ok {
		defer c.Close() // stops the API log-aggregator ticker and flushes summaries
	}
	var fresh newConns
	lim := uiServerLimits
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: lim.read, IdleTimeout: lim.idle, MaxHeaderBytes: lim.maxHeader,
		BaseContext: func(net.Listener) context.Context { return base },
		ConnState:   fresh.hook}
	log.Info("ui listening", "addr", ln.Addr().String(), "source", source, "version", version)
	if onListen != nil {
		onListen(ln.Addr().String())
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	cancelBase()
	// Conns that never sent a request would stall Shutdown until its deadline.
	fresh.closeAll()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("ui stopped")
	return nil
}

// acquireDataDir takes the exclusive data-dir lock. ok is false when
// another process holds it or locking is unsupported; nothing is written in that case.
func acquireDataDir(dir string, log *slog.Logger) (release func() error, ok bool) {
	release, holder, err := lockDir(dir)
	switch {
	case errors.Is(err, config.ErrLocked):
		log.Error(fmt.Sprintf("data dir %s locked by another sockswebui process (%s as recorded); stop it first",
			dir, holder))
		return nil, false
	case err != nil:
		log.Error("cannot lock data dir", "err", err)
		return nil, false
	case holder == "":
		log.Warn("data dir locked but the holder pid could not be recorded", "path", dir)
	default:
		log.Info("data dir locked", "path", dir, "pid", os.Getpid())
	}
	return release, true
}
