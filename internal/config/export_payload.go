package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// importedEndpointID is the id shape required of endpoints read from an export file. Validate keeps its looser non-empty+unique rule for existing configs.
var importedEndpointID = regexp.MustCompile(`^ep-[0-9a-f]{16}$`)

// Allowed "omitted" names per object, in canonical order.
var (
	endpointOmittable = []string{OmittedPrivateKey, OmittedPresharedKey, OmittedPassword}
	slotOmittable     = []string{OmittedSocksPassword}
)

// rejectField shadows the server-owned secrets_omitted key so a file carrying it is rejected
// (the file's list is "omitted").
type rejectField struct{}

func (*rejectField) UnmarshalJSON([]byte) error {
	return errors.New(`unknown field "secrets_omitted"`)
}

type wireEndpoint struct {
	Endpoint
	Omitted []string    `json:"omitted,omitempty"`
	Shadow  rejectField `json:"secrets_omitted,omitzero"`
}

type wireSlot struct {
	Slot
	Omitted []string    `json:"omitted,omitempty"`
	Shadow  rejectField `json:"secrets_omitted,omitzero"`
}

type wirePayload struct {
	Endpoints []wireEndpoint `json:"endpoints"`
	Slots     []wireSlot     `json:"slots"`
}

// encodePayload marshals p in file form: SecretsOmitted becomes "omitted".
func encodePayload(p ExportPayload) ([]byte, error) {
	w := wirePayload{Endpoints: make([]wireEndpoint, len(p.Endpoints)), Slots: make([]wireSlot, len(p.Slots))}
	for i, e := range p.Endpoints {
		w.Endpoints[i] = wireEndpoint{Endpoint: e, Omitted: e.SecretsOmitted}
	}
	for i, s := range p.Slots {
		w.Slots[i] = wireSlot{Slot: s, Omitted: s.SecretsOmitted}
	}
	return json.Marshal(w)
}

// decodePayload strictly decodes a payload, maps "omitted" onto SecretsOmitted and checks
// imported endpoint ids. Omitted lists are checked in both modes.
func decodePayload(raw []byte) (ExportPayload, error) {
	var w wirePayload
	if err := strictUnmarshal(raw, &w); err != nil {
		return ExportPayload{}, formatErr("payload: %v", err)
	}
	out := ExportPayload{Endpoints: make([]Endpoint, len(w.Endpoints)), Slots: make([]Slot, len(w.Slots))}
	var verrs ValidationErrors
	for i, we := range w.Endpoints {
		e := we.Endpoint
		if err := checkOmitted(fmt.Sprintf("endpoints[%d]", i), we.Omitted, endpointOmittable, endpointSecrets(e)); err != nil {
			return ExportPayload{}, err
		}
		e.SecretsOmitted = we.Omitted
		if !importedEndpointID.MatchString(e.ID) {
			verrs.add(fmt.Sprintf("endpoints[%d].id", i), "must match ^ep-[0-9a-f]{16}$")
		}
		out.Endpoints[i] = e
	}
	for i, ws := range w.Slots {
		s := ws.Slot
		if err := checkOmitted(fmt.Sprintf("slots[%d]", i), ws.Omitted, slotOmittable, slotSecrets(s)); err != nil {
			return ExportPayload{}, err
		}
		s.SecretsOmitted = ws.Omitted
		out.Slots[i] = s
	}
	if len(verrs) > 0 {
		return ExportPayload{}, verrs
	}
	return out, nil
}

// checkOmitted rejects unknown or repeated names and names whose secret still has a value.
func checkOmitted(path string, omitted, allowed []string, present map[string]bool) error {
	for i, name := range omitted {
		if !slices.Contains(allowed, name) || slices.Contains(omitted[:i], name) {
			return formatErr("%s.omitted: unknown or repeated name %q", path, name)
		}
		if present[name] {
			return formatErr("%s.omitted: %q is listed but has a value", path, name)
		}
	}
	return nil
}

// endpointSecrets reports which of e's secret fields are non-empty, by omitted name.
func endpointSecrets(e Endpoint) map[string]bool {
	return map[string]bool{
		OmittedPrivateKey:   e.PrivateKey != "",
		OmittedPresharedKey: e.PresharedKey != "",
		OmittedPassword:     e.Password != "",
	}
}

func slotSecrets(s Slot) map[string]bool {
	return map[string]bool{OmittedSocksPassword: s.SocksAuth != nil && s.SocksAuth.Password != ""}
}

// StripSecrets returns a deep copy of p with every secret emptied. Each object's SecretsOmitted
// lists (in canonical order) the secrets that had a value plus any already listed. p is not
// modified.
func StripSecrets(p ExportPayload) ExportPayload {
	c := cloneConfig(Config{Endpoints: p.Endpoints, Slots: p.Slots})
	for i := range c.Endpoints {
		e := &c.Endpoints[i]
		e.SecretsOmitted = mergeOmitted(endpointOmittable, e.SecretsOmitted, endpointSecrets(*e))
		e.PrivateKey, e.PresharedKey, e.Password = "", "", ""
	}
	for i := range c.Slots {
		s := &c.Slots[i]
		s.SecretsOmitted = mergeOmitted(slotOmittable, s.SecretsOmitted, slotSecrets(*s))
		if s.SocksAuth != nil {
			s.SocksAuth.Password = ""
		}
	}
	return ExportPayload{Endpoints: c.Endpoints, Slots: c.Slots}
}

func mergeOmitted(order, existing []string, present map[string]bool) []string {
	var out []string
	for _, name := range order {
		if present[name] || slices.Contains(existing, name) {
			out = append(out, name)
		}
	}
	return out
}
