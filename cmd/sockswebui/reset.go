package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/M-Wham/sockswebui/internal/config"
)

// resetAdmin is the --reset-admin mode. It
// takes the data-dir lock first (exit 1 naming the holder while a service runs), clears the
// admin password hash and session secret in config.json, prints restart instructions and
// the leftover files that still hold a hash, and logs one audit line. It never listens and
// never starts slots. Secrets never reach stdout or the log: only file names are printed.
func resetAdmin(dir string, stdout io.Writer, log *slog.Logger) int {
	release, ok := acquireDataDir(dir, log)
	if !ok {
		return 1
	}
	defer release()
	addr, err := config.ResetAdmin(dir)
	switch {
	case errors.Is(err, config.ErrNoConfig):
		fmt.Fprintf(stdout, "No %s in %s; nothing to reset.\n", config.ConfigFileName, dir)
		return 0
	case err != nil:
		log.Error("cannot reset the admin password; config.json left untouched", "err", err)
		return 1
	}
	fmt.Fprintf(stdout, "Admin password cleared. Start the service and complete setup at http://%s/ "+
		"immediately; anyone who reaches the UI first can claim it.\n", addr)
	listHashFiles(dir, stdout, log)
	log.Info("admin reset", "uid", os.Geteuid())
	return 0
}

// listHashFiles prints the leftovers that may still hold the old admin hash. Nothing is deleted.
func listHashFiles(dir string, stdout io.Writer, log *slog.Logger) {
	files, err := config.HashBearingFiles(dir)
	if err != nil {
		log.Warn("cannot list leftover config files", "err", err)
		return
	}
	if len(files) == 0 {
		return
	}
	fmt.Fprintf(stdout, "These files in %s may still hold the old admin password hash. Delete them if it may be "+
		"compromised (the .bad-* files are the only copy of a corrupt config's endpoints and slots):\n", dir)
	for _, f := range files {
		fmt.Fprintf(stdout, "  %s\n", f)
	}
}
