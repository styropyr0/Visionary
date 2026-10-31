//go:build windows

package uptimecore

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"syscall"
	"time"
	"unsafe"
)

// On Windows the ICMP helper API (IcmpSendEcho) pings without
// administrator rights, for IPv4

var (
	iphlpapi        = syscall.NewLazyDLL("iphlpapi.dll")
	icmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	icmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
	icmpSendEcho    = iphlpapi.NewProc("IcmpSendEcho")
)

const pingHelp = "Windows pings through the ICMP helper API (iphlpapi.dll); it should be present on every Windows install."

func icmpHandle() (uintptr, error) {
	if err := icmpCreateFile.Find(); err != nil {
		return 0, err
	}
	h, _, err := icmpCreateFile.Call()
	if h == uintptr(syscall.InvalidHandle) {
		return 0, err
	}
	return h, nil
}

func pingOpen() (string, error) {
	h, err := icmpHandle()
	if err != nil {
		return "", err
	}
	icmpCloseHandle.Call(h)
	return "windows", nil
}

func pingHost(ctx context.Context, addr netip.Addr, n int, wait time.Duration) ([]*float64, error) {
	if !addr.Is4() {
		return nil, errors.New("ping checks on Windows support IPv4 addresses only")
	}
	h, err := icmpHandle()
	if err != nil {
		return nil, err
	}
	defer icmpCloseHandle.Call(h)
	ip := addr.As4()
	dst := binary.LittleEndian.Uint32(ip[:])
	data := []byte("visionary-uptime-ping-0123456789")
	reply := make([]byte, 128+len(data))
	out := make([]*float64, 0, n)
	for i := range n {
		if ctx.Err() != nil {
			out = append(out, nil)
			continue
		}
		if i > 0 {
			time.Sleep(PingGapMs * time.Millisecond)
		}
		t0 := time.Now()
		got, _, _ := icmpSendEcho.Call(h, uintptr(dst), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0,
			uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(wait.Milliseconds()))
		took := msSince(t0)
		if got == 0 || binary.LittleEndian.Uint32(reply[4:8]) != 0 {
			out = append(out, nil)
			continue
		}
		out = append(out, msPtr(took))
	}
	return out, nil
}
