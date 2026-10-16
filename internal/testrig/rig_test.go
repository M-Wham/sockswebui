package testrig

import (
	"context"
	"regexp"
	"testing"

	"github.com/M-Wham/sockswebui/internal/config"
)

func TestStartDevRig(t *testing.T) {
	r, err := StartDevRig(context.Background())
	if err != nil {
		t.Fatalf("StartDevRig: %v", err)
	}
	defer r.Close()
	eps := r.Endpoints()
	ids := map[string]config.Endpoint{}
	for _, e := range eps {
		ids[e.ID] = e
		if err := config.ValidateEndpoint("ep", e); err != nil {
			t.Errorf("endpoint %s invalid: %v", e.ID, err)
		}
	}
	for _, id := range []string{DevRigWG, DevRigWG2, DevRigSOCKS} {
		if _, ok := ids[id]; !ok {
			t.Errorf("missing endpoint %s", id)
		}
	}
	if ids[DevRigWG].PrivateKey == ids[DevRigWG2].PrivateKey {
		t.Error("rigWG and rigWG2 share a client key")
	}
	if err := config.ValidateRoute("route", []string{DevRigWG2, DevRigSOCKS}, eps); err != nil {
		t.Errorf("route rigWG2→rigSOCKS invalid: %v", err)
	}
	if r.Peer() == nil {
		t.Error("Peer() is nil")
	}
	if err := r.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestStartDevRigCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := StartDevRig(ctx); err == nil {
		r.Close()
		t.Fatal("want error for a cancelled context")
	}
}

func TestDevRigEndpoints(t *testing.T) {
	r, err := StartDevRig(context.Background())
	if err != nil {
		t.Fatalf("StartDevRig: %v", err)
	}
	defer r.Close()
	eps := r.Endpoints()
	want := []string{DevRigWG, DevRigWG2, DevRigWG3, DevRigWG4, DevRigSOCKS}
	if len(eps) != len(want) {
		t.Fatalf("got %d endpoints, want %d", len(eps), len(want))
	}
	keys := map[string]string{}
	addrs := map[string]string{}
	for i, id := range want {
		e := eps[i]
		if e.ID != id {
			t.Fatalf("endpoint %d = %q, want %q", i, e.ID, id)
		}
		if e.Kind != config.KindWireGuard {
			continue
		}
		if o, dup := keys[e.PrivateKey]; dup {
			t.Errorf("%s shares client key with %s", id, o)
		}
		keys[e.PrivateKey] = id
		if o, dup := addrs[e.Addresses[0]]; dup {
			t.Errorf("%s shares address with %s", id, o)
		}
		addrs[e.Addresses[0]] = id
	}
	if eps[0].Addresses[0] == "" || eps[1].Addresses[0] != "10.64.0.3/32" {
		t.Errorf("rigWG2 address changed: %v", eps[1].Addresses)
	}
}

// Dev-rig endpoint ids must survive export → import, which rejects ids that do
// not match the server id shape, so they use that shape; the human-visible names stay rigWG..rigSOCKS.
func TestDevRigEndpointIDShape(t *testing.T) {
	r, err := StartDevRig(context.Background())
	if err != nil {
		t.Fatalf("StartDevRig: %v", err)
	}
	defer r.Close()
	re := regexp.MustCompile(`^ep-[0-9a-f]{16}$`)
	wantNames := []string{"rigWG", "rigWG2", "rigWG3", "rigWG4", "rigSOCKS"}
	eps := r.Endpoints()
	if len(eps) != len(wantNames) {
		t.Fatalf("got %d endpoints, want %d", len(eps), len(wantNames))
	}
	seen := map[string]bool{}
	for i, e := range eps {
		if !re.MatchString(e.ID) {
			t.Errorf("endpoint %q id %q does not match %s", e.Name, e.ID, re)
		}
		if seen[e.ID] {
			t.Errorf("duplicate id %q", e.ID)
		}
		seen[e.ID] = true
		if e.Name != wantNames[i] {
			t.Errorf("endpoint %d name = %q, want %q", i, e.Name, wantNames[i])
		}
	}
}
