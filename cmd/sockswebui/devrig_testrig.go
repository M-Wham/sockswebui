//go:build testrig

package main

// --dev-testrig (build tag testrig only): boots an in-process testrig (WireGuard
// peer + SOCKS5 upstream) and seeds endpoints and slot 1 pointing at it, for the
// Playwright smoke. All networking lives in internal/testrig.

import (
	"context"
	"flag"
	"fmt"
	"log/slog"

	"github.com/M-Wham/sockswebui/internal/api"
	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/testrig"
)

const devTestrigCompiled = true

// devRigSlotPort is the SOCKS5 port of the seeded slot 1 (tests override it).
var devRigSlotPort = 1080

// devRigSlots is the number of slots --dev-testrig-slots seeds (clamped 1-4).
var devRigSlots = 1

func registerDevFlags(fs *flag.FlagSet, o *options) {
	fs.IntVar(&devRigSlots, "dev-testrig-slots", 1,
		"with --dev-testrig, seed this many idle slots (1-4), each on its own rig WireGuard route")
	fs.BoolVar(&o.DevTestrig, "dev-testrig", false,
		"start an in-process testrig and seed endpoints + slot 1 (dev/test builds only)")
}

// startDevRig starts the rig when --dev-testrig is set and seeds the store if it has
// no endpoints. The returned func stops the rig; call it after the supervisor closes.
// The returned API options register the dev rig controls (devRigRoutes).
func startDevRig(ctx context.Context, o options, store *config.Store, log *slog.Logger) (func(), []api.Option, error) {
	if !o.DevTestrig {
		return func() {}, nil, nil
	}
	rig, err := testrig.StartDevRig(ctx)
	if err != nil {
		return nil, nil, err
	}
	stop := func() { _ = rig.Close() }
	seeded := false
	err = store.Update(func(c *config.Config) error {
		if len(c.Endpoints) > 0 {
			return nil
		}
		seeded = true
		n := min(max(devRigSlots, 1), config.MaxSlots)
		c.Endpoints = devRigEndpoints(rig.Endpoints(), n)
		seedIDs := map[int]bool{}
		slots := []config.Slot{}
		for i := 1; i <= n; i++ {
			slots = append(slots, devRigSlotN(i))
			seedIDs[i] = true
		}
		for _, s := range c.Slots {
			if !seedIDs[s.ID] {
				slots = append(slots, s)
			}
		}
		c.Slots = slots
		return nil
	})
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("dev testrig seed: %w", err)
	}
	if seeded {
		log.Warn("dev testrig: seeded endpoints and slot 1", "endpoints",
			[]string{testrig.DevRigWGName, testrig.DevRigWG2Name, testrig.DevRigSOCKSName}, "slot_port", devRigSlotPort)
	} else {
		log.Warn("dev testrig: config already has endpoints; seeding skipped")
	}
	return stop, devRigRoutes(rig.Peer(), log), nil
}

// devRigEndpoints keeps rigWG..rigWG<max(n,2)> and rigSOCKS, so the default
// single-slot seed is unchanged (rigWG, rigWG2, rigSOCKS).
func devRigEndpoints(all []config.Endpoint, n int) []config.Endpoint {
	keep := map[string]bool{testrig.DevRigSOCKS: true}
	for i, id := range []string{testrig.DevRigWG, testrig.DevRigWG2, testrig.DevRigWG3, testrig.DevRigWG4} {
		keep[id] = i < max(n, 2)
	}
	var out []config.Endpoint
	for _, e := range all {
		if keep[e.ID] {
			out = append(out, e)
		}
	}
	return out
}

// devRigSlotN is seeded slot n (1-4): route [rigWG<n>] (by id, testrig.DevRigWG*), port devRigSlotPort+n-1
// for n > 1 (slot 1 keeps devRigSlotPort).
func devRigSlotN(n int) config.Slot {
	ids := []string{testrig.DevRigWG, testrig.DevRigWG2, testrig.DevRigWG3, testrig.DevRigWG4}
	name := "rig"
	if n > 1 {
		name = fmt.Sprintf("rig%d", n)
	}
	return config.Slot{
		ID: n, Name: name, Enabled: true, BindAddr: "127.0.0.1", Port: devRigSlotPort + n - 1,
		Route: []string{ids[n-1]}, Killswitch: config.KillswitchBlock,
		Health: config.Health{IntervalS: 2, URL: testrig.DevRigHealthURL, FailThreshold: 2},
	}
}
