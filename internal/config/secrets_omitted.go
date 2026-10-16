package config

import "slices"

// SecretsMissingMessage is the user-facing reason a slot cannot be enabled or started while
// secrets dropped by a no-secrets config import have not been re-entered.
const SecretsMissingMessage = "secrets missing: re-enter the secrets omitted by the config import before enabling this slot"

// SlotSecretsMissing reports whether sl cannot run because its own SecretsOmitted list, or that
// of any endpoint in its route, is non-empty. Route ids not found in c are ignored here
// (ValidateRoute reports them).
func SlotSecretsMissing(c Config, sl Slot) bool {
	if len(sl.SecretsOmitted) > 0 {
		return true
	}
	for _, id := range sl.Route {
		for _, e := range c.Endpoints {
			if e.ID == id && len(e.SecretsOmitted) > 0 {
				return true
			}
		}
	}
	return false
}

// passwordOmitted reports whether e's password is listed as missing.
func passwordOmitted(e Endpoint) bool { return slices.Contains(e.SecretsOmitted, OmittedPassword) }
