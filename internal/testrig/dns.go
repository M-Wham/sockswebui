package testrig

// Tiny hand-rolled, stdlib-only DNS responder for the WG peer's netstack.
// It answers A queries from a static map; AAAA and other types for a known
// name get an empty NOERROR answer, unknown names get NXDOMAIN. Every answer
// is marked authoritative with RA set so stub resolvers do not treat it as a
// lame referral.

import (
	"encoding/binary"
	"net"
	"net/netip"
	"strings"
)

const (
	dnsTypeA    = 1
	dnsClassIN  = 1
	dnsRcodeNX  = 3
	dnsRcodeErr = 1
	dnsTTL      = 60
)

// ServeDNS answers queries on pc from records (lower-case FQDN without the
// trailing dot → IPv4) until pc is closed. The returned channel closes once
// the serving goroutine has exited.
func ServeDNS(pc net.PacketConn, records map[string]netip.Addr) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if resp := dnsAnswer(buf[:n], records); resp != nil {
				_, _ = pc.WriteTo(resp, from)
			}
		}
	}()
	return done
}

// dnsAnswer builds the response to one query, or nil if q is not a query.
func dnsAnswer(q []byte, records map[string]netip.Addr) []byte {
	if len(q) < 12 || q[2]&0x80 != 0 || binary.BigEndian.Uint16(q[4:6]) != 1 {
		return nil
	}
	name, end, ok := dnsName(q, 12)
	if !ok || end+4 > len(q) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(q[end : end+2])
	qclass := binary.BigEndian.Uint16(q[end+2 : end+4])
	question := q[12 : end+4]

	ip, known := records[strings.ToLower(name)]
	rcode := byte(0)
	switch {
	case !known:
		rcode = dnsRcodeNX
	case qclass != dnsClassIN:
		rcode = dnsRcodeErr
	}
	answer := known && rcode == 0 && qtype == dnsTypeA && ip.Is4()

	resp := make([]byte, 12, 12+len(question)+16)
	copy(resp[0:2], q[0:2])               // ID
	resp[2] = 0x80 | 0x04 | (q[2] & 0x79) // QR, AA, opcode, RD
	resp[3] = 0x80 | rcode                // RA, rcode
	binary.BigEndian.PutUint16(resp[4:6], 1)
	if answer {
		binary.BigEndian.PutUint16(resp[6:8], 1)
	}
	resp = append(resp, question...)
	if answer {
		a := ip.As4()
		resp = append(resp, 0xC0, 12) // pointer to the question name
		resp = binary.BigEndian.AppendUint16(resp, dnsTypeA)
		resp = binary.BigEndian.AppendUint16(resp, dnsClassIN)
		resp = binary.BigEndian.AppendUint32(resp, dnsTTL)
		resp = binary.BigEndian.AppendUint16(resp, 4)
		resp = append(resp, a[:]...)
	}
	return resp
}

// dnsName decodes an uncompressed name starting at off. It returns the dotted
// name (no trailing dot) and the offset just past the terminating zero label.
func dnsName(b []byte, off int) (string, int, bool) {
	var labels []string
	for off < len(b) {
		l := int(b[off])
		off++
		if l == 0 {
			return strings.Join(labels, "."), off, true
		}
		if l > 63 || off+l > len(b) {
			return "", 0, false
		}
		labels = append(labels, string(b[off:off+l]))
		off += l
	}
	return "", 0, false
}
