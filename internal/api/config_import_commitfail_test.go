package api

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
	"github.com/M-Wham/sockswebui/internal/supervisor"
)

// failCommit makes the real store's import fail at the config commit: after the backup is
// written and beforeCommit (stopAllSlots) ran, config.json is swapped for a non-empty
// directory, so the rename onto it fails (for root too). The original file is put back
// before replaceConfig returns. after, if set, runs once the store call has returned.
func (x *importEnv) failCommit(t *testing.T, after func()) {
	t.Helper()
	path := filepath.Join(x.dir, config.ConfigFileName)
	aside := path + ".aside"
	x.srv.replaceConfig = func(build func(config.Config) (config.Config, error), beforeCommit func()) (string, error) {
		name, err := x.store.ReplaceWithBackupFunc(build, func() {
			beforeCommit()
			if err := os.Rename(path, aside); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(path, "blocker"), 0o700); err != nil {
				t.Fatal(err)
			}
		})
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(aside, path); err != nil {
			t.Fatal(err)
		}
		if after != nil {
			after()
		}
		return name, err
	}
	t.Cleanup(func() { x.srv.replaceConfig = nil })
}

func (x *importEnv) state(id int) supervisor.State {
	for _, st := range x.ctrl.Snapshot() {
		if st.ID == id {
			return st.State
		}
	}
	return supervisor.StateStopped
}

// newCommitFailEnv has slots 1 (UP), 2 (DEGRADED) and 3 (STOPPED).
func newCommitFailEnv(t *testing.T) (*importEnv, []byte) {
	t.Helper()
	x := newImportEnv(t)
	slotFixture(t, x.authed, 2, true)
	slotFixture(t, x.authed, 3, false)
	x.ctrl.setState(1, supervisor.StateUp)
	x.ctrl.setState(2, supervisor.StateDegraded)
	x.ctrl.setState(3, supervisor.StateStopped)
	p := x.payload()
	p.Slots = p.Slots[:1] // a different config, so a commit would change something
	return x, sealFile(t, p, "no-secrets")
}

func TestImportCommitFailSlotsState(t *testing.T) {
	t.Run("RestartsPreviouslyRunning", func(t *testing.T) {
		x, file := newCommitFailEnv(t)
		before := x.snap()
		x.failCommit(t, nil)
		rec := x.importConfig(file, exportAdminPW, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("got %d %s, want 500", rec.Code, rec.Body)
		}
		after := x.snap()
		if !slices.Equal(after.raw, before.raw) {
			t.Error("config.json bytes changed")
		}
		if !reflect.DeepEqual(after.cfg, before.cfg) {
			t.Error("store.Get() changed")
		}
		if !slices.Equal(after.dir, before.dir) {
			t.Errorf("data dir listing changed (backup kept?): %v -> %v", before.dir, after.dir)
		}
		for _, id := range []int{1, 2} {
			if st := x.state(id); st != supervisor.StateStarting && st != supervisor.StateUp {
				t.Errorf("slot %d state = %s, want STARTING/UP", id, st)
			}
		}
		if st := x.state(3); st != supervisor.StateStopped {
			t.Errorf("slot 3 state = %s, want STOPPED", st)
		}
		got := after.seq[len(before.seq):]
		want := []string{"stop 1", "stop 2", "start 1", "start 2"}
		if !slices.Equal(got, want) {
			t.Errorf("controller calls = %v, want %v", got, want)
		}
	})
	t.Run("NoRestartWhenConfigDiverged", func(t *testing.T) {
		// The restart needs Get() to equal the pre-import snapshot; a config that
		// changed in between was never confirmed with these slots running.
		x, file := newCommitFailEnv(t)
		before := x.snap()
		x.failCommit(t, func() {
			if err := x.store.Update(func(c *config.Config) error {
				c.Slots[0].Name = "changed"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
		rec := x.importConfig(file, exportAdminPW, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("got %d %s, want 500", rec.Code, rec.Body)
		}
		got := x.snap().seq[len(before.seq):]
		if want := []string{"stop 1", "stop 2"}; !slices.Equal(got, want) {
			t.Errorf("controller calls = %v, want %v", got, want)
		}
		for _, id := range []int{1, 2, 3} {
			if st := x.state(id); st != supervisor.StateStopped {
				t.Errorf("slot %d state = %s, want STOPPED", id, st)
			}
		}
	})
	t.Run("StartFailureLoggedNotFatal", func(t *testing.T) {
		x, file := newCommitFailEnv(t)
		startsBefore, _, _ := x.ctrl.calls() // fixture saves start enabled slots
		x.ctrl.mu.Lock()
		x.ctrl.startErr = supervisor.ErrNoSlot
		x.ctrl.mu.Unlock()
		x.failCommit(t, nil)
		rec := x.importConfig(file, exportAdminPW, nil)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("got %d %s, want 500", rec.Code, rec.Body)
		}
		if starts, _, _ := x.ctrl.calls(); !slices.Equal(starts[len(startsBefore):], []int{1, 2}) {
			t.Errorf("starts = %v, want [1 2] after %v", starts, startsBefore)
		}
	})
}
