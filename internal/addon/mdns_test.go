package addon

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func makeTestQuery(id, flags uint16, questions ...[]byte) []byte {
	pkt := make([]byte, 12)
	binary.BigEndian.PutUint16(pkt[0:2], id)
	binary.BigEndian.PutUint16(pkt[2:4], flags)
	binary.BigEndian.PutUint16(pkt[4:6], uint16(len(questions)))
	for _, q := range questions {
		pkt = append(pkt, q...)
	}
	return pkt
}

func makeQuestion(fqdn string, qtype, qclass uint16) []byte {
	w, ok := encodeDNSName(fqdn)
	if !ok {
		panic("bad fqdn: " + fqdn)
	}
	var tail [4]byte
	binary.BigEndian.PutUint16(tail[0:2], qtype)
	binary.BigEndian.PutUint16(tail[2:4], qclass)
	return append(w, tail[:]...)
}

func TestHandleMDNSQueryStandardDualStackWithCompression(t *testing.T) {
	ip := net.IPv4(192, 168, 10, 97)
	hosts := []string{"techo5.local."}

	// Question 1: TECHO5.LOCAL. IN A (QM) at offset 12
	q1 := makeQuestion("TECHO5.LOCAL.", dnsTypeA, dnsClassIN)
	// Question 2: pointer to offset 12 (0xC00C) IN AAAA (QU)
	q2 := []byte{0xC0, 0x0C, 0x00, dnsTypeAAAA, 0x80, dnsClassIN}
	pkt := makeTestQuery(0x1234, 0x0000, q1, q2)

	replies := handleMDNSQuery(pkt, mdnsPort, ip, hosts)
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	rep := replies[0]
	if !rep.multicast || !rep.unicast {
		t.Fatalf("expected both multicast (QM) and unicast (QU), got multicast=%v unicast=%v", rep.multicast, rep.unicast)
	}

	// Verify RFC 6762 header: ID=0, Flags=0x8400, QD=0, AN=1, NS=0, AR=1
	if gotID := binary.BigEndian.Uint16(rep.packet[0:2]); gotID != 0 {
		t.Errorf("standard mDNS response ID = 0x%x, want 0", gotID)
	}
	if gotFlags := binary.BigEndian.Uint16(rep.packet[2:4]); gotFlags != 0x8400 {
		t.Errorf("flags = 0x%x, want 0x8400", gotFlags)
	}
	if an := binary.BigEndian.Uint16(rep.packet[6:8]); an != 1 {
		t.Errorf("ANCOUNT = %d, want 1", an)
	}
	if ar := binary.BigEndian.Uint16(rep.packet[10:12]); ar != 1 {
		t.Errorf("ARCOUNT = %d, want 1", ar)
	}

	// Parse Answer RR (Type A = 192.168.10.97)
	name, off, ok := parseDNSName(rep.packet, 12)
	if !ok || name != "techo5.local." {
		t.Fatalf("answer name = %q (ok=%v), want techo5.local.", name, ok)
	}
	rrType := binary.BigEndian.Uint16(rep.packet[off : off+2])
	rrClass := binary.BigEndian.Uint16(rep.packet[off+2 : off+4])
	rdLen := int(binary.BigEndian.Uint16(rep.packet[off+8 : off+10]))
	if rrType != dnsTypeA || rrClass != (dnsClassIN|dnsCacheFlush) || rdLen != 4 {
		t.Fatalf("unexpected A header: type=%d class=0x%x rdlen=%d", rrType, rrClass, rdLen)
	}
	gotIP := net.IP(rep.packet[off+10 : off+14])
	if !gotIP.Equal(ip) {
		t.Errorf("A record IP = %v, want %v", gotIP, ip)
	}

	// Parse Additional RR (Type NSEC asserting only A exists)
	nsecOff := off + 14
	nsecName, off2, ok := parseDNSName(rep.packet, nsecOff)
	if !ok || nsecName != "techo5.local." {
		t.Fatalf("NSEC name = %q (ok=%v), want techo5.local.", nsecName, ok)
	}
	if gotType := binary.BigEndian.Uint16(rep.packet[off2 : off2+2]); gotType != dnsTypeNSEC {
		t.Fatalf("additional RR type = %d, want NSEC (%d)", gotType, dnsTypeNSEC)
	}
	nsecRDLen := int(binary.BigEndian.Uint16(rep.packet[off2+8 : off2+10]))
	bitmap := rep.packet[off2+10+nsecRDLen-3 : off2+10+nsecRDLen]
	if !bytes.Equal(bitmap, []byte{0x00, 0x01, 0x40}) {
		t.Errorf("NSEC bitmap = %x, want 000140 (Type A only)", bitmap)
	}
}

func TestHandleMDNSQueryStandaloneAAAAReturnsNegativeNSEC(t *testing.T) {
	ip := net.IPv4(192, 168, 10, 97)
	pkt := makeTestQuery(0, 0, makeQuestion("techo5.local.", dnsTypeAAAA, dnsClassIN))

	replies := handleMDNSQuery(pkt, mdnsPort, ip, []string{"techo5.local."})
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	_, off, ok := parseDNSName(replies[0].packet, 12)
	if !ok {
		t.Fatal("failed to parse answer name")
	}
	if gotType := binary.BigEndian.Uint16(replies[0].packet[off : off+2]); gotType != dnsTypeNSEC {
		t.Errorf("AAAA-only answer type = %d, want NSEC (%d)", gotType, dnsTypeNSEC)
	}
}

func TestHandleMDNSQueryLegacyUnicast(t *testing.T) {
	ip := net.IPv4(192, 168, 10, 97)
	pkt := makeTestQuery(0xBEEF, 0x0100, makeQuestion("techo5.local.", dnsTypeA, dnsClassIN))

	replies := handleMDNSQuery(pkt, 54321, ip, []string{"techo5.local."})
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want 1", len(replies))
	}
	rep := replies[0]
	if !rep.unicast || rep.multicast {
		t.Fatalf("legacy query expected unicast only, got unicast=%v multicast=%v", rep.unicast, rep.multicast)
	}
	if gotID := binary.BigEndian.Uint16(rep.packet[0:2]); gotID != 0xBEEF {
		t.Errorf("legacy response ID = 0x%x, want 0xBEEF", gotID)
	}
	if qd := binary.BigEndian.Uint16(rep.packet[4:6]); qd != 1 {
		t.Errorf("legacy QDCOUNT = %d, want 1", qd)
	}
}

func TestHandleMDNSQueryIgnoresResponsesAndCorruptPointers(t *testing.T) {
	ip := net.IPv4(192, 168, 10, 97)
	// Response packet (QR=1) must be ignored
	respPkt := makeTestQuery(0, 0x8400, makeQuestion("techo5.local.", dnsTypeA, dnsClassIN))
	if got := handleMDNSQuery(respPkt, mdnsPort, ip, []string{"techo5.local."}); len(got) != 0 {
		t.Errorf("expected 0 replies for QR=1 response packet, got %d", len(got))
	}

	// Self-referencing pointer at offset 12 (0xC0 0x0C) must be rejected cleanly
	loopPkt := []byte{
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xC0, 0x0C, 0x00, 0x01, 0x00, 0x01,
	}
	if got := handleMDNSQuery(loopPkt, mdnsPort, ip, []string{"techo5.local."}); len(got) != 0 {
		t.Errorf("expected 0 replies for pointer loop packet, got %d", len(got))
	}
}

func TestActiveMDNSHostnamesFallbackWhenEchodStopped(t *testing.T) {
	dir := t.TempDir()
	stPath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(stPath, []byte(`{"device":{"name":"EchoShow5"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	whenRunning := activeMDNSHostnames(stPath, true)
	for _, n := range whenRunning {
		if n == "echoshow5.local." {
			t.Errorf("did not expect echoshow5.local. while echod is running: %v", whenRunning)
		}
	}

	whenStopped := activeMDNSHostnames(stPath, false)
	foundTecho5 := false
	foundShow := false
	for _, n := range whenStopped {
		if n == "techo5.local." {
			foundTecho5 = true
		}
		if n == "echoshow5.local." {
			foundShow = true
		}
	}
	if !foundTecho5 || !foundShow {
		t.Errorf("expected both techo5.local. and echoshow5.local. when echod stopped, got %v", whenStopped)
	}
}
