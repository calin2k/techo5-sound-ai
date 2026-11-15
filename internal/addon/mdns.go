package addon

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	mdnsPort        = 5353
	dnsTypeA        = 1
	dnsTypeAAAA     = 28
	dnsTypeNSEC     = 47
	dnsTypeANY      = 255
	dnsClassIN      = 1
	dnsCacheFlush   = 0x8000
	mdnsDefaultTTL  = 120
	mdnsLegacyTTL   = 10
	mdnsPollEvery   = 3 * time.Second
	techo5StatePath = "/data/misc/techo5/state.json"
)

var mdnsGroup4 = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: mdnsPort}

// RunMDNS advertises and answers .local mDNS queries (e.g. techo5.local) on the active
// LAN IPv4 interface. Because techo5-sound-ai lives on /data and starts via rc.local,
// techo5.local remains reachable across reboots, DHCP IP changes, and TECHO5 OTA updates.
func RunMDNS(ctx context.Context) {
	for {
		iface, ip4 := lanInterfaceIPv4()
		if iface == nil || ip4 == nil {
			if !sleepMDNS(ctx, mdnsPollEvery) {
				return
			}
			continue
		}

		if err := serveMDNSInterface(ctx, iface, ip4); err != nil && ctx.Err() == nil {
			log.Printf("WARN mdns responder error iface=%s ip=%s err=%v", iface.Name, ip4, err)
		}
		if !sleepMDNS(ctx, mdnsPollEvery) {
			return
		}
	}
}

func serveMDNSInterface(ctx context.Context, iface *net.Interface, ip4 net.IP) error {
	conn, err := net.ListenMulticastUDP("udp4", iface, mdnsGroup4)
	if err != nil {
		return err
	}
	defer conn.Close()

	configureMDNSSocket(conn, ip4)

	names := activeMDNSHostnames(techo5StatePath, isProcessRunning("techo5"))
	log.Printf("INFO mdns responder active iface=%s ip=%s names=%s", iface.Name, ip4, strings.Join(names, ","))

	for _, h := range names {
		if pkt, ok := buildMDNSAnnouncement(h, ip4, mdnsDefaultTTL); ok {
			_, _ = conn.WriteToUDP(pkt, mdnsGroup4)
		}
	}

	reannounceAt := time.Now().Add(time.Second)
	reannounced := false
	nextCheck := time.Now().Add(mdnsPollEvery)
	buf := make([]byte, 2048)

	for {
		if ctx.Err() != nil {
			return nil
		}
		now := time.Now()
		if !reannounced && !now.Before(reannounceAt) {
			reannounced = true
			for _, h := range names {
				if pkt, ok := buildMDNSAnnouncement(h, ip4, mdnsDefaultTTL); ok {
					_, _ = conn.WriteToUDP(pkt, mdnsGroup4)
				}
			}
		}
		if !now.Before(nextCheck) {
			curIface, curIP := lanInterfaceIPv4()
			if curIface == nil || curIP == nil || curIface.Index != iface.Index || !curIP.Equal(ip4) {
				return nil
			}
			names = activeMDNSHostnames(techo5StatePath, isProcessRunning("techo5"))
			nextCheck = now.Add(mdnsPollEvery)
		}

		deadline := nextCheck
		if !reannounced && reannounceAt.Before(deadline) {
			deadline = reannounceAt
		}
		_ = conn.SetReadDeadline(deadline)
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if src == nil || n < 12 {
			continue
		}

		for _, act := range handleMDNSQuery(buf[:n], src.Port, ip4, names) {
			if act.unicast {
				_, _ = conn.WriteToUDP(act.packet, src)
			}
			if act.multicast {
				_, _ = conn.WriteToUDP(act.packet, mdnsGroup4)
			}
		}
	}
}

func configureMDNSSocket(conn *net.UDPConn, ip4 net.IP) {
	v4 := ip4.To4()
	if v4 == nil {
		return
	}
	rc, err := conn.SyscallConn()
	if err != nil {
		return
	}
	var addr [4]byte
	copy(addr[:], v4)
	_ = rc.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, 255)
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, 255)
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_LOOP, 1)
		_ = syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, addr)
	})
}

func lanInterfaceIPv4() (*net.Interface, net.IP) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	var fallbackIface *net.Interface
	var fallbackIP net.IP
	for i := range ifaces {
		ifc := ifaces[i]
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagMulticast == 0 {
			continue
		}
		if isVirtualOrVPNInterface(ifc.Name) {
			continue
		}
		ip := firstUsableIPv4(&ifc)
		if ip == nil {
			continue
		}
		if ifc.Name == "wlan0" || ifc.Name == "eth0" {
			return &ifc, ip
		}
		if fallbackIface == nil {
			cp := ifc
			fallbackIface = &cp
			fallbackIP = ip
		}
	}
	return fallbackIface, fallbackIP
}

func isVirtualOrVPNInterface(name string) bool {
	n := strings.ToLower(name)
	for _, prefix := range []string{"lo", "tailscale", "netbird", "wg", "tun", "tap", "docker", "br-", "veth", "virbr"} {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

func firstUsableIPv4(ifc *net.Interface) net.IP {
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		v4 := ip.To4()
		if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() || v4.IsMulticast() || v4.IsUnspecified() {
			continue
		}
		return v4
	}
	return nil
}

// activeMDNSHostnames returns canonical lower-case FQDN .local names (with trailing dot)
// that this responder should answer for. Always includes "techo5.local." (and the OS hostname
// slug if valid); also includes the TECHO5 device slug (e.g. "echoshow5.local.") as a fallback
// whenever echod is not running so both names stay reachable during maintenance.
func activeMDNSHostnames(statePath string, echodRunning bool) []string {
	var out []string
	add := func(label string) {
		s := dnsSlug(label)
		if s == "" {
			return
		}
		fqdn := s + ".local."
		for _, existing := range out {
			if existing == fqdn {
				return
			}
		}
		out = append(out, fqdn)
	}

	add("techo5")
	if h, err := os.Hostname(); err == nil {
		if idx := strings.IndexByte(h, '.'); idx > 0 {
			h = h[:idx]
		}
		add(h)
	}
	if !echodRunning && statePath != "" {
		if devName := readTECHO5DeviceName(statePath); devName != "" {
			add(devName)
		}
	}
	return out
}

func readTECHO5DeviceName(statePath string) string {
	b, err := os.ReadFile(statePath)
	if err != nil {
		return ""
	}
	var st struct {
		Device struct {
			Name string `json:"name"`
		} `json:"device"`
	}
	if json.Unmarshal(b, &st) != nil {
		return ""
	}
	return strings.TrimSpace(st.Device.Name)
}

func isProcessRunning(comm string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err == nil && strings.TrimSpace(string(b)) == comm {
			return true
		}
	}
	return false
}

func dnsSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 63 {
			break
		}
	}
	return strings.TrimRight(b.String(), "-")
}

type mdnsReply struct {
	packet    []byte
	unicast   bool
	multicast bool
}

type hostQueryMatch struct {
	name      string
	wantA     bool
	wantOther bool
	qu        bool
	qm        bool
	firstQType  uint16
	firstQClass uint16
}

func handleMDNSQuery(pkt []byte, srcPort int, ip4 net.IP, hostnames []string) []mdnsReply {
	v4 := ip4.To4()
	if len(pkt) < 12 || v4 == nil || len(hostnames) == 0 {
		return nil
	}
	id := binary.BigEndian.Uint16(pkt[0:2])
	flags := binary.BigEndian.Uint16(pkt[2:4])
	if flags&0x8000 != 0 || (flags>>11)&0x0F != 0 {
		return nil // ignore responses and non-standard opcodes
	}
	qdCount := int(binary.BigEndian.Uint16(pkt[4:6]))
	if qdCount <= 0 || qdCount > 64 {
		return nil
	}

	var matches []hostQueryMatch
	findOrAdd := func(fqdn string) *hostQueryMatch {
		for i := range matches {
			if matches[i].name == fqdn {
				return &matches[i]
			}
		}
		matches = append(matches, hostQueryMatch{name: fqdn})
		return &matches[len(matches)-1]
	}

	off := 12
	for i := 0; i < qdCount; i++ {
		qname, nextOff, ok := parseDNSName(pkt, off)
		if !ok || nextOff+4 > len(pkt) {
			return nil
		}
		qtype := binary.BigEndian.Uint16(pkt[nextOff : nextOff+2])
		qclassRaw := binary.BigEndian.Uint16(pkt[nextOff+2 : nextOff+4])
		off = nextOff + 4

		qu := (qclassRaw & 0x8000) != 0
		qclass := qclassRaw & 0x7FFF
		if qclass != dnsClassIN && qclass != dnsTypeANY {
			continue
		}
		matchedHost := ""
		for _, h := range hostnames {
			if qname == h {
				matchedHost = h
				break
			}
		}
		if matchedHost == "" {
			continue
		}
		m := findOrAdd(matchedHost)
		if m.firstQType == 0 {
			m.firstQType = qtype
			m.firstQClass = qclass
		}
		if qu {
			m.qu = true
		} else {
			m.qm = true
		}
		if qtype == dnsTypeA || qtype == dnsTypeANY {
			m.wantA = true
		} else {
			m.wantOther = true
		}
	}

	if len(matches) == 0 {
		return nil
	}

	legacyUnicast := srcPort != mdnsPort
	var replies []mdnsReply
	for _, m := range matches {
		if legacyUnicast {
			resp, ok := buildLegacyUnicastResponse(id, m, v4)
			if ok {
				replies = append(replies, mdnsReply{packet: resp, unicast: true})
			}
			continue
		}
		resp, ok := buildStandardMDNSResponse(m, v4, mdnsDefaultTTL)
		if !ok {
			continue
		}
		replies = append(replies, mdnsReply{
			packet:    resp,
			unicast:   m.qu,
			multicast: m.qm,
		})
	}
	return replies
}

func buildMDNSAnnouncement(fqdn string, ip4 net.IP, ttl uint32) ([]byte, bool) {
	v4 := ip4.To4()
	if v4 == nil {
		return nil, false
	}
	return buildStandardMDNSResponse(hostQueryMatch{
		name:  fqdn,
		wantA: true,
		qm:    true,
	}, v4, ttl)
}

func buildStandardMDNSResponse(m hostQueryMatch, v4 net.IP, ttl uint32) ([]byte, bool) {
	wireName, ok := encodeDNSName(m.name)
	if !ok {
		return nil, false
	}
	aRec := buildARecord(wireName, dnsClassIN|dnsCacheFlush, ttl, v4)
	nsecRec := buildNSECRecord(wireName, dnsClassIN|dnsCacheFlush, ttl)

	buf := make([]byte, 12, 12+len(aRec)+len(nsecRec))
	binary.BigEndian.PutUint16(buf[0:2], 0)      // ID = 0 per RFC 6762 §18.1
	binary.BigEndian.PutUint16(buf[2:4], 0x8400) // QR=1, AA=1
	binary.BigEndian.PutUint16(buf[4:6], 0)      // QDCOUNT = 0
	binary.BigEndian.PutUint16(buf[8:10], 0)     // NSCOUNT = 0

	if m.wantA {
		// Answer: A record; Additional: NSEC proving only A exists (prevents dual-stack AAAA wait).
		binary.BigEndian.PutUint16(buf[6:8], 1)   // ANCOUNT = 1
		binary.BigEndian.PutUint16(buf[10:12], 1) // ARCOUNT = 1
		buf = append(buf, aRec...)
		buf = append(buf, nsecRec...)
	} else {
		// Question was for AAAA (or another non-A type): answer negatively via NSEC, include A in Additional.
		binary.BigEndian.PutUint16(buf[6:8], 1)   // ANCOUNT = 1
		binary.BigEndian.PutUint16(buf[10:12], 1) // ARCOUNT = 1
		buf = append(buf, nsecRec...)
		buf = append(buf, aRec...)
	}
	return buf, true
}

func buildLegacyUnicastResponse(id uint16, m hostQueryMatch, v4 net.IP) ([]byte, bool) {
	wireName, ok := encodeDNSName(m.name)
	if !ok {
		return nil, false
	}
	buf := make([]byte, 12, 128)
	binary.BigEndian.PutUint16(buf[0:2], id)
	binary.BigEndian.PutUint16(buf[2:4], 0x8400) // QR=1, AA=1
	binary.BigEndian.PutUint16(buf[4:6], 1)      // Echo 1 question per RFC 6762 §6.7

	var q [4]byte
	binary.BigEndian.PutUint16(q[0:2], m.firstQType)
	binary.BigEndian.PutUint16(q[2:4], m.firstQClass)

	if m.wantA {
		binary.BigEndian.PutUint16(buf[6:8], 1) // ANCOUNT = 1
		buf = append(buf, wireName...)
		buf = append(buf, q[:]...)
		buf = append(buf, buildARecord(wireName, dnsClassIN, mdnsLegacyTTL, v4)...)
	} else {
		// Negative NODATA response with NSEC for AAAA/other legacy queries.
		binary.BigEndian.PutUint16(buf[6:8], 0)
		binary.BigEndian.PutUint16(buf[8:10], 1) // NSCOUNT = 1
		buf = append(buf, wireName...)
		buf = append(buf, q[:]...)
		buf = append(buf, buildNSECRecord(wireName, dnsClassIN, mdnsLegacyTTL)...)
	}
	return buf, true
}

func buildARecord(wireName []byte, rrClass uint16, ttl uint32, v4 net.IP) []byte {
	rec := make([]byte, 0, len(wireName)+10+4)
	rec = append(rec, wireName...)
	var hdr [10]byte
	binary.BigEndian.PutUint16(hdr[0:2], dnsTypeA)
	binary.BigEndian.PutUint16(hdr[2:4], rrClass)
	binary.BigEndian.PutUint32(hdr[4:8], ttl)
	binary.BigEndian.PutUint16(hdr[8:10], 4)
	rec = append(rec, hdr[:]...)
	rec = append(rec, v4[:4]...)
	return rec
}

// buildNSECRecord constructs an RFC 6762 §6.1 negative-existence NSEC record asserting
// that ONLY Type A (RR type 1 -> window 0, length 1, bitmap 0x40) exists at wireName.
func buildNSECRecord(wireName []byte, rrClass uint16, ttl uint32) []byte {
	rdataLen := len(wireName) + 3
	rec := make([]byte, 0, len(wireName)+10+rdataLen)
	rec = append(rec, wireName...)
	var hdr [10]byte
	binary.BigEndian.PutUint16(hdr[0:2], dnsTypeNSEC)
	binary.BigEndian.PutUint16(hdr[2:4], rrClass)
	binary.BigEndian.PutUint32(hdr[4:8], ttl)
	binary.BigEndian.PutUint16(hdr[8:10], uint16(rdataLen))
	rec = append(rec, hdr[:]...)
	// RFC 4034 §4.1.1 / RFC 6762 §18.14: Next Domain Name is uncompressed wireName.
	rec = append(rec, wireName...)
	// Window block 0, bitmap length 1 byte, bit 1 (Type A = 1 -> 0x80 >> 1 = 0x40).
	rec = append(rec, 0x00, 0x01, 0x40)
	return rec
}

func encodeDNSName(fqdn string) ([]byte, bool) {
	s := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(fqdn)), ".")
	if s == "" {
		return nil, false
	}
	parts := strings.Split(s, ".")
	out := make([]byte, 0, len(s)+2)
	for _, p := range parts {
		if len(p) == 0 || len(p) > 63 {
			return nil, false
		}
		out = append(out, byte(len(p)))
		out = append(out, p...)
	}
	out = append(out, 0x00)
	if len(out) > 255 {
		return nil, false
	}
	return out, true
}

func parseDNSName(pkt []byte, startOff int) (string, int, bool) {
	var b strings.Builder
	off := startOff
	nextOff := -1
	jumps := 0
	totalLen := 0

	for {
		if off < 0 || off >= len(pkt) {
			return "", 0, false
		}
		l := int(pkt[off])
		if l == 0 {
			if nextOff < 0 {
				nextOff = off + 1
			}
			if b.Len() == 0 {
				return ".", nextOff, true
			}
			b.WriteByte('.')
			return b.String(), nextOff, true
		}
		if l&0xC0 == 0xC0 {
			if off+1 >= len(pkt) {
				return "", 0, false
			}
			ptr := ((l & 0x3F) << 8) | int(pkt[off+1])
			if ptr >= off || jumps >= 16 {
				return "", 0, false
			}
			if nextOff < 0 {
				nextOff = off + 2
			}
			off = ptr
			jumps++
			continue
		}
		if l&0xC0 != 0 {
			return "", 0, false // reserved label type
		}
		off++
		if off+l > len(pkt) {
			return "", 0, false
		}
		totalLen += l + 1
		if totalLen > 255 {
			return "", 0, false
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		for i := 0; i < l; i++ {
			c := pkt[off+i]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			b.WriteByte(c)
		}
		off += l
	}
}

func sleepMDNS(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
