//go:build linux

package main

import "syscall"

// prSetTHPDisable is PR_SET_THP_DISABLE from <linux/prctl.h> (Linux 3.15+).
const prSetTHPDisable = 41

// init turns transparent huge pages off for this process before main runs. On hosts
// with THP "always" the Go heap otherwise faults in 2 MiB pages and idle RSS with 4 slots
// roughly doubles (~36 MB to ~76 MB), past the < 64 MB RSS budget. The kernel
// checks the flag at fault time, so it covers memory the runtime has already reserved.
// GODEBUG=disablethp=1 does the same but cannot be set by a //go:debug directive.
// Failure (kernel before 3.15) is ignored: THP is then just left as the host set it.
func init() {
	_, _, _ = syscall.RawSyscall6(syscall.SYS_PRCTL, prSetTHPDisable, 1, 0, 0, 0, 0)
}
