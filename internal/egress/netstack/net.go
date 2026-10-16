/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2017-2025 WireGuard LLC. All Rights Reserved.
 */

// Trimmed copy of golang.zx2c4.com/wireguard/tun/netstack
// (v0.0.0-20260522210424-ecfc5a8d5446); see doc.go for why it is vendored.

package netstack

import (
	"context"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
)

func (net *Net) DialContextTCPAddrPort(ctx context.Context, addr netip.AddrPort) (*gonet.TCPConn, error) {
	fa, pn := convertToFullAddr(addr)
	return gonet.DialContextTCP(ctx, net.stack, fa, pn)
}

func (net *Net) DialContextTCP(ctx context.Context, addr *net.TCPAddr) (*gonet.TCPConn, error) {
	if addr == nil {
		return net.DialContextTCPAddrPort(ctx, netip.AddrPort{})
	}
	ip, _ := netip.AddrFromSlice(addr.IP)
	return net.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(ip, uint16(addr.Port)))
}

func (net *Net) DialTCPAddrPort(addr netip.AddrPort) (*gonet.TCPConn, error) {
	fa, pn := convertToFullAddr(addr)
	return gonet.DialTCP(net.stack, fa, pn)
}

func (net *Net) DialTCP(addr *net.TCPAddr) (*gonet.TCPConn, error) {
	if addr == nil {
		return net.DialTCPAddrPort(netip.AddrPort{})
	}
	ip, _ := netip.AddrFromSlice(addr.IP)
	return net.DialTCPAddrPort(netip.AddrPortFrom(ip, uint16(addr.Port)))
}

func (net *Net) ListenTCPAddrPort(addr netip.AddrPort) (*gonet.TCPListener, error) {
	fa, pn := convertToFullAddr(addr)
	return gonet.ListenTCP(net.stack, fa, pn)
}

func (net *Net) ListenTCP(addr *net.TCPAddr) (*gonet.TCPListener, error) {
	if addr == nil {
		return net.ListenTCPAddrPort(netip.AddrPort{})
	}
	ip, _ := netip.AddrFromSlice(addr.IP)
	return net.ListenTCPAddrPort(netip.AddrPortFrom(ip, uint16(addr.Port)))
}

func (net *Net) DialUDPAddrPort(laddr, raddr netip.AddrPort) (*gonet.UDPConn, error) {
	var lfa, rfa *tcpip.FullAddress
	var pn tcpip.NetworkProtocolNumber
	if laddr.IsValid() || laddr.Port() > 0 {
		var addr tcpip.FullAddress
		addr, pn = convertToFullAddr(laddr)
		lfa = &addr
	}
	if raddr.IsValid() || raddr.Port() > 0 {
		var addr tcpip.FullAddress
		addr, pn = convertToFullAddr(raddr)
		rfa = &addr
	}
	return gonet.DialUDP(net.stack, lfa, rfa, pn)
}

func (net *Net) ListenUDPAddrPort(laddr netip.AddrPort) (*gonet.UDPConn, error) {
	return net.DialUDPAddrPort(laddr, netip.AddrPort{})
}

func (net *Net) DialUDP(laddr, raddr *net.UDPAddr) (*gonet.UDPConn, error) {
	var la, ra netip.AddrPort
	if laddr != nil {
		ip, _ := netip.AddrFromSlice(laddr.IP)
		la = netip.AddrPortFrom(ip, uint16(laddr.Port))
	}
	if raddr != nil {
		ip, _ := netip.AddrFromSlice(raddr.IP)
		ra = netip.AddrPortFrom(ip, uint16(raddr.Port))
	}
	return net.DialUDPAddrPort(la, ra)
}

func (net *Net) ListenUDP(laddr *net.UDPAddr) (*gonet.UDPConn, error) {
	return net.DialUDP(laddr, nil)
}

func partialDeadline(now, deadline time.Time, addrsRemaining int) (time.Time, error) {
	if deadline.IsZero() {
		return deadline, nil
	}
	timeRemaining := deadline.Sub(now)
	if timeRemaining <= 0 {
		return time.Time{}, errTimeout
	}
	timeout := timeRemaining / time.Duration(addrsRemaining)
	const saneMinimum = 2 * time.Second
	if timeout < saneMinimum {
		if timeRemaining < saneMinimum {
			timeout = timeRemaining
		} else {
			timeout = saneMinimum
		}
	}
	return now.Add(timeout), nil
}

var protoSplitter = regexp.MustCompile(`^(tcp|udp)(4|6)?$`)

func (tnet *Net) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if ctx == nil {
		panic("nil context")
	}
	var acceptV4, acceptV6 bool
	matches := protoSplitter.FindStringSubmatch(network)
	if matches == nil {
		return nil, &net.OpError{Op: "dial", Err: net.UnknownNetworkError(network)}
	} else if len(matches[2]) == 0 {
		acceptV4 = true
		acceptV6 = true
	} else {
		acceptV4 = matches[2][0] == '4'
		acceptV6 = !acceptV4
	}
	host, sport, err := net.SplitHostPort(address)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Err: err}
	}
	port, err := strconv.Atoi(sport)
	if err != nil || port < 0 || port > 65535 {
		return nil, &net.OpError{Op: "dial", Err: errNumericPort}
	}
	allAddr, err := tnet.LookupContextHost(ctx, host)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Err: err}
	}
	var addrs []netip.AddrPort
	for _, addr := range allAddr {
		ip, err := netip.ParseAddr(addr)
		if err == nil && ((ip.Is4() && acceptV4) || (ip.Is6() && acceptV6)) {
			addrs = append(addrs, netip.AddrPortFrom(ip, uint16(port)))
		}
	}
	if len(addrs) == 0 && len(allAddr) != 0 {
		return nil, &net.OpError{Op: "dial", Err: errNoSuitableAddress}
	}

	var firstErr error
	for i, addr := range addrs {
		select {
		case <-ctx.Done():
			err := ctx.Err()
			if err == context.Canceled {
				err = errCanceled
			} else if err == context.DeadlineExceeded {
				err = errTimeout
			}
			return nil, &net.OpError{Op: "dial", Err: err}
		default:
		}

		dialCtx := ctx
		if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
			partialDeadline, err := partialDeadline(time.Now(), deadline, len(addrs)-i)
			if err != nil {
				if firstErr == nil {
					firstErr = &net.OpError{Op: "dial", Err: err}
				}
				break
			}
			if partialDeadline.Before(deadline) {
				var cancel context.CancelFunc
				dialCtx, cancel = context.WithDeadline(ctx, partialDeadline)
				defer cancel()
			}
		}

		var c net.Conn
		switch matches[1] {
		case "tcp":
			c, err = tnet.DialContextTCPAddrPort(dialCtx, addr)
		case "udp":
			c, err = tnet.DialUDPAddrPort(netip.AddrPort{}, addr)
		}
		if err == nil {
			return c, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = &net.OpError{Op: "dial", Err: errMissingAddress}
	}
	return nil, firstErr
}

func (tnet *Net) Dial(network, address string) (net.Conn, error) {
	return tnet.DialContext(context.Background(), network, address)
}
