package config

import "fmt"

// ValidateRoute checks a route against the route rules: at most MaxRouteHops hops, every hop a
// known endpoint, and WireGuard hops forming a contiguous prefix (proxy hops only follow).
// path is the route's field path, e.g. "slots[0].route". An empty route (direct) is valid.
func ValidateRoute(path string, route []string, endpoints []Endpoint) ValidationErrors {
	var errs ValidationErrors
	if len(route) > MaxRouteHops {
		errs.add(path, "route has %d hops; the maximum is %d", len(route), MaxRouteHops)
	}
	kinds := make(map[string]string, len(endpoints))
	for _, e := range endpoints {
		kinds[e.ID] = e.Kind
	}
	seenProxy := false
	for i, id := range route {
		field := fmt.Sprintf("%s[%d]", path, i)
		kind, ok := kinds[id]
		if !ok {
			errs.add(field, "unknown endpoint %q", id)
			continue
		}
		switch kind {
		case KindWireGuard:
			if seenProxy {
				errs.add(field, "WireGuard hop %q must precede proxy hops: WireGuard needs UDP "+
					"and upstream SOCKS5/HTTP proxies are TCP-only", id)
			}
		case KindSOCKS5, KindHTTP:
			seenProxy = true
		}
	}
	return errs
}
