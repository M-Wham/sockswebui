package supervisor

// State is a slot's lifecycle state.
type State string

// Slot states. DEGRADED is entered by the kill switch.
const (
	StateStopped  State = "STOPPED"
	StateStarting State = "STARTING"
	StateUp       State = "UP"
	StateDegraded State = "DEGRADED"
	StateError    State = "ERROR"
)

// running reports whether the slot owns live resources driven by its
// goroutine (anything but STOPPED and ERROR).
func (s State) running() bool {
	return s == StateStarting || s == StateUp || s == StateDegraded
}
