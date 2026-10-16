package main

import (
	"fmt"
	"log/slog"
	"net"

	"github.com/M-Wham/sockswebui/internal/config"
)

// UI bind sources reported in the boot log.
const (
	uiBindSourceFlag    = "flag"
	uiBindSourceSetting = "setting"
)

// resolveUIAddr picks the one UI bind address (the stored setting is the default source): an explicitly passed --ui-addr wins; otherwise the stored
// settings.ui_bind_addr, whose default for a fresh data dir is config.Default()'s.
func resolveUIAddr(opts options, cfg config.Config) (addr, source string) {
	if opts.UIAddrExplicit {
		return opts.UIAddr, uiBindSourceFlag
	}
	return cfg.Settings.UIBindAddr, uiBindSourceSetting
}

// listenUI opens the UI listener on addr. When the flag overrides a different stored
// value it logs one WARN naming both. A listen failure is logged with the source and the
// --ui-addr escape hatch; ok is false then.
func listenUI(addr, source, stored string, log *slog.Logger) (ln net.Listener, ok bool) {
	if source == uiBindSourceFlag && addr != stored {
		log.Warn(fmt.Sprintf("--ui-addr %s overrides settings.ui_bind_addr %s; the stored value is ignored "+
			"while the flag is passed", addr, stored))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		hint := "fix settings.ui_bind_addr in config.json or pass --ui-addr explicitly"
		if source == uiBindSourceFlag {
			hint = "fix or drop --ui-addr"
		}
		log.Error("ui server: cannot listen", "addr", addr, "source", source, "hint", hint, "err", err)
		return nil, false
	}
	return ln, true
}
