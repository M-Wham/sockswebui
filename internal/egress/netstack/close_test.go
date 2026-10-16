package netstack

import (
	"errors"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

var (
	localIP  = netip.MustParseAddr("10.9.0.1")
	remoteIP = netip.MustParseAddr("10.9.0.2")
)

// udpToLocal builds an IPv4/UDP packet from remoteIP to a closed port on
// localIP. Injected via Write it makes the stack answer with ICMP port
// unreachable, i.e. it drives WriteNotify from the device-receive path.
func udpToLocal() []byte {
	const payload = 4
	pkt := make([]byte, header.IPv4MinimumSize+header.UDPMinimumSize+payload)
	ip := header.IPv4(pkt)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(pkt)),
		TTL:         64,
		Protocol:    uint8(header.UDPProtocolNumber),
		SrcAddr:     tcpip.AddrFrom4(remoteIP.As4()),
		DstAddr:     tcpip.AddrFrom4(localIP.As4()),
	})
	ip.SetChecksum(^ip.CalculateChecksum())
	u := header.UDP(pkt[header.IPv4MinimumSize:])
	u.Encode(&header.UDPFields{SrcPort: 4000, DstPort: 9, Length: uint16(header.UDPMinimumSize + payload)})
	return pkt
}

// TestCloseWithWritersInFlight is the regression test for upstream
// netTun.Close closing incomingPacket while WriteNotify is sending on it
// ("panic: send on closed channel", and a -race report). The reader is slow
// on purpose, so outbound packets are blocked in WriteNotify when Close runs.
func TestCloseWithWritersInFlight(t *testing.T) {
	for i := 0; i < 50; i++ {
		closeUnderLoad(t)
	}
}

func closeUnderLoad(t *testing.T) {
	t.Helper()
	tdev, tnet, err := CreateNetTUN([]netip.Addr{localIP}, nil, 1420)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	readErr := make(chan error, 1)
	wg.Add(1)
	go func() { // stands in for wireguard-go's RoutineReadFromTUN, but busy
		defer wg.Done()
		bufs, sizes := [][]byte{make([]byte, 2048)}, []int{0}
		for {
			if _, err := tdev.Read(bufs, sizes, 0); err != nil {
				readErr <- err
				return
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	for w := 0; w < 4; w++ { // stack-originated writes (like a DNS query)
		wg.Add(1)
		go func() {
			defer wg.Done()
			uc, err := tnet.DialUDPAddrPort(netip.AddrPort{}, netip.AddrPortFrom(remoteIP, 9))
			if err != nil {
				return
			}
			defer uc.Close()
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := uc.Write([]byte("ping")); err != nil {
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() { // device-receive path: injected packets trigger ICMP replies
		defer wg.Done()
		pkt := udpToLocal()
		for {
			select {
			case <-done:
				return
			default:
			}
			if _, err := tdev.Write([][]byte{pkt}, 0); err != nil {
				return
			}
		}
	}()

	time.Sleep(5 * time.Millisecond)
	if err := tdev.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(done)
	waitOrFail(t, &wg, "writers and reader after Close")
	if err := <-readErr; !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Read after Close = %v, want os.ErrClosed", err)
	}
	if err := tdev.Close(); err != nil { // idempotent: device.Close may race a second caller
		t.Fatalf("second Close: %v", err)
	}
}

func waitOrFail(t *testing.T, wg *sync.WaitGroup, what string) {
	t.Helper()
	ch := make(chan struct{})
	go func() { wg.Wait(); close(ch) }()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}
