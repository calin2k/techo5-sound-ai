package asp

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func encodeTestDescriptor(buffers []Buffer, channels []Channel) []byte {
	nb, nc := len(buffers), len(channels)
	sz := HeaderSize + nb*BufferSize + nc*ChannelSize
	var cum uint32
	for _, b := range buffers {
		cum += b.Length
	}
	out := make([]byte, sz)
	copy(out, Magic)
	binary.LittleEndian.PutUint16(out[4:], Version)
	out[6], out[7] = byte(nb), byte(nc)
	binary.LittleEndian.PutUint32(out[8:], uint32(sz))
	binary.LittleEndian.PutUint32(out[12:], cum)
	p := HeaderSize
	for _, b := range buffers {
		binary.LittleEndian.PutUint32(out[p:], b.Offset)
		binary.LittleEndian.PutUint32(out[p+4:], b.Length)
		binary.LittleEndian.PutUint32(out[p+8:], b.SampleRate)
		binary.LittleEndian.PutUint32(out[p+12:], b.Frames)
		binary.LittleEndian.PutUint32(out[p+16:], b.Stride)
		out[p+20], out[p+21], out[p+22], out[p+23] = b.Format, b.ValidBits, b.LSBShift, b.Channels
		binary.LittleEndian.PutUint16(out[p+24:], b.FirstChannel)
		p += BufferSize
	}
	for _, c := range channels {
		out[p], out[p+1] = c.BufferIndex, c.Role
		binary.LittleEndian.PutUint16(out[p+2:], c.RoleIndex)
		binary.LittleEndian.PutUint32(out[p+4:], c.FrameOffset)
		p += ChannelSize
	}
	return out
}

func TestRunMixesS24_3LEAndTeesToSink(t *testing.T) {
	// 4 channels (2 MIC + 2 REF) in S24_3LE, 320 frames @ 16 kHz, stride = 12 bytes
	stride := uint32(12)
	frames := uint32(320)
	length := stride * frames
	descBytes := encodeTestDescriptor(
		[]Buffer{{
			Offset: 0, Length: length, SampleRate: 16000, Frames: frames, Stride: stride,
			Format: FormatS24_3, ValidBits: 24, LSBShift: 0, Channels: 4, FirstChannel: 0,
		}},
		[]Channel{
			{BufferIndex: 0, Role: RoleMIC, RoleIndex: 0, FrameOffset: 0},
			{BufferIndex: 0, Role: RoleMIC, RoleIndex: 1, FrameOffset: 3},
			{BufferIndex: 0, Role: RoleREF, RoleIndex: 0, FrameOffset: 6},
			{BufferIndex: 0, Role: RoleREF, RoleIndex: 1, FrameOffset: 9},
		},
	)

	pcm := make([]byte, length)
	for f := 0; f < int(frames); f++ {
		off := f * int(stride)
		// Mic 0 = +4000 << 8, Mic 1 = +6000 << 8 => average = +5000
		v0 := int32(4000) << 8
		v1 := int32(6000) << 8
		pcm[off+0], pcm[off+1], pcm[off+2] = byte(v0), byte(v0>>8), byte(v0>>16)
		pcm[off+3], pcm[off+4], pcm[off+5] = byte(v1), byte(v1>>8), byte(v1>>16)
	}

	var in bytes.Buffer
	in.Write(descBytes)
	in.Write(pcm)

	var out bytes.Buffer
	var teed [][]byte
	if err := Run(&in, &out, "", func(frame []byte) {
		cp := append([]byte(nil), frame...)
		teed = append(teed, cp)
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	res := out.Bytes()
	if len(res) != 1+OutputBytes {
		t.Fatalf("output length = %d, want %d", len(res), 1+OutputBytes)
	}
	if res[0] != ReadyByte {
		t.Fatalf("ready byte = %#x, want %#x", res[0], ReadyByte)
	}
	s0 := int16(binary.LittleEndian.Uint16(res[1:3]))
	// 5000 raw average with headroom cap (23197) -> 23197
	if s0 != 23197 {
		t.Fatalf("leveled sample[0] = %d, want 23197", s0)
	}
	if len(teed) != 1 || len(teed[0]) != OutputBytes {
		t.Fatalf("teed frames = %d, want 1 of %d bytes", len(teed), OutputBytes)
	}
}

func TestRunUnixDatagramSinkDelivery(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "sound.sock")
	srv, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	_ = os.Chmod(sockPath, 0600)

	sink, closeSink := NewUnixDatagramSink(sockPath)
	defer closeSink()

	want := make([]byte, OutputBytes)
	binary.LittleEndian.PutUint16(want[0:2], uint16(1234))
	sink(want)

	_ = srv.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, OutputBytes)
	n, _, err := srv.ReadFromUnix(got)
	if err != nil {
		t.Fatalf("ReadFromUnix: %v", err)
	}
	if n != OutputBytes || binary.LittleEndian.Uint16(got[0:2]) != 1234 {
		t.Fatalf("unexpected datagram: n=%d sample0=%d", n, binary.LittleEndian.Uint16(got[0:2]))
	}
}
