/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2017-2025 WireGuard LLC. All Rights Reserved.
 */

// Package netstack is a trimmed copy of golang.zx2c4.com/wireguard/tun/netstack
// at v0.0.0-20260522210424-ecfc5a8d5446 (MIT, WireGuard LLC), with one fix.
//
// Upstream netTun.Close closes incomingPacket while gVisor's channel queue
// may still be calling WriteNotify outside its lock: from a DNS query, a
// TCP timer, or wireguard-go's own receive goroutine answering an injected
// packet. That send then panics ("send on closed channel"), which takes
// down the whole process, and go test -race reports it. Here Close closes a
// separate closed channel instead; Read and WriteNotify select on it and
// incomingPacket is never closed (tun.go). close_test.go reproduces the bug.
//
// Dropped from upstream: ping sockets ("ping" networks in DialContext).
// Close is also idempotent here. Everything else is unchanged. Drop this
// copy once upstream fixes Close.
package netstack
