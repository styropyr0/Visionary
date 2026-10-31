//go:build !windows

package analytics

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const pingHelp = "Ping checks need unprivileged ICMP sockets (on Linux, the sysctl net.ipv4.ping_group_range must include the server's group; Docker allows it by default) or the CAP_NET_RAW capability."

type pingSocket struct {
	network, listen string
	udp             bool
}

func pingSockets(v6 bool) []pingSocket {
	if v6 {
		return []pingSocket{{"udp6", "::", true}, {"ip6:ipv6-icmp", "::", false}}
	}
	return []pingSocket{{"udp4", "0.0.0.0", true}, {"ip4:icmp", "0.0.0.0", false}}
}

func listenPing(v6 bool) (*icmp.PacketConn, pingSocket, error) {
	var first error
	for _, s := range pingSockets(v6) {
		c, err := icmp.ListenPacket(s.network, s.listen)
		if err == nil {
			return c, s, nil
		}
		if first == nil {
			first = err
		}
	}
	return nil, pingSocket{}, first
}

func pingOpen() (string, error) {
	c, s, err := listenPing(false)
	if err != nil {
		return "", err
	}
	c.Close()
	if s.udp {
		return "datagram socket", nil
	}
	return "raw socket", nil
}

func pingHost(ctx context.Context, addr netip.Addr, n int, wait time.Duration) ([]*float64, error) {
	v6 := !addr.Unmap().Is4()
	conn, sock, err := listenPing(v6)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var dst net.Addr = &net.IPAddr{IP: addr.AsSlice()}
	if sock.udp {
		dst = &net.UDPAddr{IP: addr.AsSlice()}
	}
	var echo, reply icmp.Type = ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	proto := 1
	if v6 {
		echo, reply, proto = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply, 58
	}
	id := os.Getpid() & 0xffff
	buf := make([]byte, 1500)
	out := make([]*float64, 0, n)
	for seq := 1; seq <= n; seq++ {
		if ctx.Err() != nil {
			out = append(out, nil)
			continue
		}
		if seq > 1 {
			time.Sleep(pingGapMs * time.Millisecond)
		}
		msg := icmp.Message{Type: echo, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("visionary-uptime-ping")}}
		b, err := msg.Marshal(nil)
		if err != nil {
			return nil, err
		}
		t0 := time.Now()
		if _, err := conn.WriteTo(b, dst); err != nil {
			return nil, err
		}
		var rtt *float64
		deadline := t0.Add(wait)
		for rtt == nil && time.Now().Before(deadline) {
			_ = conn.SetReadDeadline(deadline)
			m, _, err := conn.ReadFrom(buf)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					break
				}
				return nil, err
			}
			parsed, err := icmp.ParseMessage(proto, buf[:m])
			if err != nil || parsed.Type != reply {
				continue
			}
			// Datagram sockets rewrite the id, so only the sequence is matched there.
			if e, ok := parsed.Body.(*icmp.Echo); ok && e.Seq == seq && (sock.udp || e.ID == id) {
				rtt = msPtr(msSince(t0))
			}
		}
		out = append(out, rtt)
	}
	return out, nil
}
