package asp

import (
	"bytes"
	"encoding/binary"
	"math"
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

	// Simulate daemon restart: close and unlink old socket, bind a new socket at the same path,
	// and verify the existing sink continues delivering frames without re-initialization.
	_ = srv.Close()
	_ = os.Remove(sockPath)
	srv2, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv2.Close()

	binary.LittleEndian.PutUint16(want[0:2], uint16(5678))
	sink(want)
	_ = srv2.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err = srv2.ReadFromUnix(got)
	if err != nil {
		t.Fatalf("ReadFromUnix after socket restart: %v", err)
	}
	if n != OutputBytes || binary.LittleEndian.Uint16(got[0:2]) != 5678 {
		t.Fatalf("unexpected datagram after restart: n=%d sample0=%d", n, binary.LittleEndian.Uint16(got[0:2]))
	}
}

func TestReadDescriptorRejectsCorruptAndMalformedInputs(t *testing.T) {
	validBuf := Buffer{
		Offset: 0, Length: 640, SampleRate: 16000, Frames: 320, Stride: 2,
		Format: FormatS16, ValidBits: 16, LSBShift: 0, Channels: 1,
	}
	validCh := Channel{BufferIndex: 0, Role: RoleMIC, RoleIndex: 0, FrameOffset: 0}
	base := encodeTestDescriptor([]Buffer{validBuf}, []Channel{validCh})

	clone := func(fn func(b []byte)) []byte {
		cp := append([]byte(nil), base...)
		fn(cp)
		return cp
	}

	cases := []struct {
		name string
		raw  []byte
	}{
		{"short_header", base[:10]},
		{"bad_magic", clone(func(b []byte) { copy(b[0:4], "BAD!") })},
		{"bad_version", clone(func(b []byte) { binary.LittleEndian.PutUint16(b[4:6], 2) })},
		{"zero_buffers", clone(func(b []byte) { b[6] = 0 })},
		{"zero_channels", clone(func(b []byte) { b[7] = 0 })},
		{"wrong_desc_size", clone(func(b []byte) { binary.LittleEndian.PutUint32(b[8:12], 999) })},
		{"zero_input_size", clone(func(b []byte) { binary.LittleEndian.PutUint32(b[12:16], 0) })},
		{"huge_input_size", clone(func(b []byte) { binary.LittleEndian.PutUint32(b[12:16], 8*1024*1024) })},
		{"truncated_body", base[:HeaderSize+10]},
		{"zero_buffer_frames", encodeTestDescriptor([]Buffer{{
			Offset: 0, Length: 640, SampleRate: 16000, Frames: 0, Stride: 2, Format: FormatS16, Channels: 1,
		}}, []Channel{validCh})},
		{"buffer_exceeds_input_size", encodeTestDescriptor([]Buffer{{
			Offset: 100, Length: 640, SampleRate: 16000, Frames: 320, Stride: 2, Format: FormatS16, Channels: 1,
		}}, []Channel{validCh})},
		{"unknown_format", encodeTestDescriptor([]Buffer{{
			Offset: 0, Length: 640, SampleRate: 16000, Frames: 320, Stride: 2, Format: 99, Channels: 1,
		}}, []Channel{validCh})},
		{"channel_buffer_out_of_range", encodeTestDescriptor([]Buffer{validBuf}, []Channel{{
			BufferIndex: 3, Role: RoleMIC, FrameOffset: 0,
		}})},
		{"channel_unknown_role", encodeTestDescriptor([]Buffer{validBuf}, []Channel{{
			BufferIndex: 0, Role: 99, FrameOffset: 0,
		}})},
		{"channel_frame_offset_out_of_stride", encodeTestDescriptor([]Buffer{validBuf}, []Channel{{
			BufferIndex: 0, Role: RoleMIC, FrameOffset: 2,
		}})},
		{"no_mic_channels", encodeTestDescriptor([]Buffer{validBuf}, []Channel{{
			BufferIndex: 0, Role: RoleREF, FrameOffset: 0,
		}})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadDescriptor(bytes.NewReader(tc.raw)); err == nil {
				t.Fatalf("expected ReadDescriptor(%s) to fail", tc.name)
			}
		})
	}
}

func TestExtractSampleFormatsAndClamping(t *testing.T) {
	// 1. Out of bounds / negative pos / unknown format -> 0
	if got := ExtractSample([]byte{1, 2, 3, 4}, -1, FormatS16, 16, 0); got != 0 {
		t.Fatalf("negative pos: got %d, want 0", got)
	}
	if got := ExtractSample([]byte{1}, 0, FormatS16, 16, 0); got != 0 {
		t.Fatalf("short S16: got %d, want 0", got)
	}
	if got := ExtractSample([]byte{1, 2}, 0, FormatS24_3, 24, 0); got != 0 {
		t.Fatalf("short S24_3: got %d, want 0", got)
	}
	if got := ExtractSample([]byte{1, 2, 3}, 0, FormatS32, 32, 0); got != 0 {
		t.Fatalf("short S32: got %d, want 0", got)
	}
	if got := ExtractSample([]byte{1, 2, 3, 4}, 0, 99, 16, 0); got != 0 {
		t.Fatalf("unknown format: got %d, want 0", got)
	}

	// 2. S16 with lsbShift
	var s16 [2]byte
	binary.LittleEndian.PutUint16(s16[:], uint16(8000))
	if got := ExtractSample(s16[:], 0, FormatS16, 16, 2); got != 2000 {
		t.Fatalf("S16 shifted: got %d, want 2000", got)
	}

	// 3. S24_3 negative sign extension and clamping
	neg := int32(-12000) << 8
	s24 := []byte{byte(neg), byte(neg >> 8), byte(neg >> 16)}
	if got := ExtractSample(s24, 0, FormatS24_3, 24, 0); got != -12000 {
		t.Fatalf("S24_3 negative: got %d, want -12000", got)
	}

	// 4. S32 positive and negative
	var s32 [4]byte
	v32 := int32(-9000) << 16
	binary.LittleEndian.PutUint32(s32[:], uint32(v32))
	if got := ExtractSample(s32[:], 0, FormatS32, 32, 0); got != -9000 {
		t.Fatalf("S32 negative: got %d, want -9000", got)
	}
}

func TestProcessor48kHzDecimationAndNLMSEchoCancellation(t *testing.T) {
	// 48 kHz (960 frames per 20 ms block), 2 channels (1 MIC + 1 REF) in S16LE (stride = 4)
	frames := uint32(960)
	stride := uint32(4)
	length := frames * stride
	descBytes := encodeTestDescriptor(
		[]Buffer{{
			Offset: 0, Length: length, SampleRate: 48000, Frames: frames, Stride: stride,
			Format: FormatS16, ValidBits: 16, LSBShift: 0, Channels: 2,
		}},
		[]Channel{
			{BufferIndex: 0, Role: RoleMIC, RoleIndex: 0, FrameOffset: 0},
			{BufferIndex: 0, Role: RoleREF, RoleIndex: 0, FrameOffset: 2},
		},
	)
	desc, err := ReadDescriptor(bytes.NewReader(descBytes))
	if err != nil {
		t.Fatal(err)
	}
	proc := NewProcessor(desc)
	proc.Leveling = false

	// Guard: nil or short output buffer does not panic
	proc.Process(nil, nil)
	proc.Process(make([]byte, length), make([]byte, 10))

	// Inject NaN into filter weight to verify automatic recovery in Process
	proc.w[0] = float32(math.NaN())

	inBuf := make([]byte, length)
	outBuf := make([]byte, OutputBytes)

	var firstBlockEnergy, lastBlockEnergy float64
	for blk := 0; blk < 20; blk++ {
		for f := 0; f < int(frames); f++ {
			tm := float64(blk*int(frames)+f) / 48000.0
			ref := int16(10000 * math.Sin(2*math.Pi*500*tm))
			// Mic sees 60% of the playback reference (echo)
			mic := int16(float64(ref) * 0.6)
			off := f * int(stride)
			binary.LittleEndian.PutUint16(inBuf[off:], uint16(mic))
			binary.LittleEndian.PutUint16(inBuf[off+2:], uint16(ref))
		}
		proc.Process(inBuf, outBuf)
		var e float64
		for i := 0; i < OutputSamples; i++ {
			s := float64(int16(binary.LittleEndian.Uint16(outBuf[i*2:])))
			e += s * s
		}
		if blk == 0 {
			firstBlockEnergy = e
		}
		if blk == 19 {
			lastBlockEnergy = e
		}
	}
	if lastBlockEnergy >= firstBlockEnergy*0.25 {
		t.Fatalf("expected NLMS echo cancellation to attenuate reference tone: first=%v last=%v", firstBlockEnergy, lastBlockEnergy)
	}

	// Verify 3-tap boxcar anti-aliasing filter rejects 16 kHz folding tone (+9000, -4500, -4500) at 48 kHz
	clear(inBuf)
	triple := [3]int16{9000, -4500, -4500}
	for f := 0; f < int(frames); f++ {
		off := f * int(stride)
		binary.LittleEndian.PutUint16(inBuf[off:], uint16(triple[f%3]))
	}
	proc.Process(inBuf, outBuf)
	for i := 0; i < OutputSamples; i++ {
		if s := int16(binary.LittleEndian.Uint16(outBuf[i*2:])); s != 0 {
			t.Fatalf("expected 16 kHz folding tone to be canceled by 3-tap anti-aliasing filter, got sample[%d]=%d", i, s)
		}
	}
}

func TestRunChainedUpstreamSuccessAndFailures(t *testing.T) {
	dir := t.TempDir()
	descBytes := encodeTestDescriptor(
		[]Buffer{{
			Offset: 0, Length: OutputBytes, SampleRate: 16000, Frames: OutputSamples, Stride: 2,
			Format: FormatS16, ValidBits: 16, Channels: 1,
		}},
		[]Channel{{BufferIndex: 0, Role: RoleMIC, FrameOffset: 0}},
	)
	frame := make([]byte, OutputBytes)
	binary.LittleEndian.PutUint16(frame[0:2], uint16(4321))

	// 1. Healthy upstream script: reads 52-byte descriptor + 640-byte frame, writes 0xa5 + 640-byte frame
	okScript := filepath.Join(dir, "orig-ok.sh")
	scriptBody := "#!/bin/sh\ndd bs=52 count=1 of=/dev/null 2>/dev/null\nprintf '\\245'\ndd bs=640 count=1 2>/dev/null\n"
	if err := os.WriteFile(okScript, []byte(scriptBody), 0755); err != nil {
		t.Fatal(err)
	}

	var in bytes.Buffer
	in.Write(descBytes)
	in.Write(frame)
	var out bytes.Buffer
	var teed int
	if err := Run(&in, &out, okScript, func(b []byte) { teed += len(b) }); err != nil {
		t.Fatalf("Run chained okScript: %v", err)
	}
	if out.Len() != 1+OutputBytes || out.Bytes()[0] != ReadyByte || teed != OutputBytes {
		t.Fatalf("unexpected chained output: len=%d ready=%#x teed=%d", out.Len(), out.Bytes()[0], teed)
	}

	// 2. Upstream returns bad ready byte (0x00)
	badReadyScript := filepath.Join(dir, "orig-bad.sh")
	if err := os.WriteFile(badReadyScript, []byte("#!/bin/sh\nprintf '\\000'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	in.Reset()
	in.Write(descBytes)
	out.Reset()
	if err := Run(&in, &out, badReadyScript, nil); err == nil {
		t.Fatal("expected error when upstream returns bad ready byte")
	}

	// 3. Upstream exits immediately without writing ready byte
	eofScript := filepath.Join(dir, "orig-eof.sh")
	if err := os.WriteFile(eofScript, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	in.Reset()
	in.Write(descBytes)
	out.Reset()
	if err := Run(&in, &out, eofScript, nil); err == nil {
		t.Fatal("expected error when upstream exits before ready byte")
	}
}

func FuzzReadDescriptor(f *testing.F) {
	f.Add(encodeTestDescriptor(
		[]Buffer{{Offset: 0, Length: 640, SampleRate: 16000, Frames: 320, Stride: 2, Format: FormatS16, ValidBits: 16, Channels: 1}},
		[]Channel{{BufferIndex: 0, Role: RoleMIC, FrameOffset: 0}},
	))
	f.Add([]byte("ASP1"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		desc, err := ReadDescriptor(bytes.NewReader(raw))
		if err == nil && len(desc.Buffers) == 0 {
			t.Fatal("ReadDescriptor succeeded with 0 buffers")
		}
	})
}

func FuzzExtractSample(f *testing.F) {
	f.Add([]byte{0x01, 0x02, 0x03, 0x04}, 0, uint8(FormatS24_3), uint8(24), uint8(0))
	f.Fuzz(func(t *testing.T, buf []byte, pos int, format, validBits, lsbShift uint8) {
		_ = ExtractSample(buf, pos, format, validBits, lsbShift)
	})
}

func BenchmarkProcessorProcess(b *testing.B) {
	stride := uint32(12)
	frames := uint32(320)
	length := stride * frames
	descBytes := encodeTestDescriptor(
		[]Buffer{{
			Offset: 0, Length: length, SampleRate: 16000, Frames: frames, Stride: stride,
			Format: FormatS24_3, ValidBits: 24, LSBShift: 0, Channels: 4,
		}},
		[]Channel{
			{BufferIndex: 0, Role: RoleMIC, RoleIndex: 0, FrameOffset: 0},
			{BufferIndex: 0, Role: RoleMIC, RoleIndex: 1, FrameOffset: 3},
			{BufferIndex: 0, Role: RoleREF, RoleIndex: 0, FrameOffset: 6},
			{BufferIndex: 0, Role: RoleREF, RoleIndex: 1, FrameOffset: 9},
		},
	)
	desc, err := ReadDescriptor(bytes.NewReader(descBytes))
	if err != nil {
		b.Fatal(err)
	}
	proc := NewProcessor(desc)
	in := make([]byte, length)
	out := make([]byte, OutputBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		proc.Process(in, out)
	}
}

func TestProcessorPlaybackActiveTagging(t *testing.T) {
	frames := uint32(320)
	stride := uint32(4)
	length := frames * stride
	descBytes := encodeTestDescriptor(
		[]Buffer{{
			Offset: 0, Length: length, SampleRate: 16000, Frames: frames, Stride: stride,
			Format: FormatS16, ValidBits: 16, Channels: 2,
		}},
		[]Channel{
			{BufferIndex: 0, Role: RoleMIC, RoleIndex: 0, FrameOffset: 0},
			{BufferIndex: 0, Role: RoleREF, RoleIndex: 0, FrameOffset: 2},
		},
	)

	// 1. Silent reference channel -> PlaybackActive() == false, sink receives 640 bytes
	silentFrame := make([]byte, length)
	for f := 0; f < int(frames); f++ {
		binary.LittleEndian.PutUint16(silentFrame[f*4:], uint16(1000)) // MIC only, REF = 0
	}
	// 2. Active reference channel (RMS > 180) -> PlaybackActive() == true, sink receives 642 bytes with pkt[640]==1
	activeFrame := make([]byte, length)
	for f := 0; f < int(frames); f++ {
		binary.LittleEndian.PutUint16(activeFrame[f*4:], uint16(1000))
		binary.LittleEndian.PutUint16(activeFrame[f*4+2:], uint16(4000)) // REF = 4000 > 180
	}

	var in bytes.Buffer
	in.Write(descBytes)
	in.Write(silentFrame)
	in.Write(activeFrame)

	var out bytes.Buffer
	var sinkFrames [][]byte
	if err := Run(&in, &out, "", func(b []byte) {
		sinkFrames = append(sinkFrames, append([]byte(nil), b...))
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if out.Len() != 1+2*OutputBytes {
		t.Fatalf("stdout len = %d, want %d (must never include playback tag bytes)", out.Len(), 1+2*OutputBytes)
	}
	if len(sinkFrames) != 2 {
		t.Fatalf("sinkFrames len = %d, want 2", len(sinkFrames))
	}
	if len(sinkFrames[0]) != OutputBytes {
		t.Fatalf("silent ref frame len = %d, want %d", len(sinkFrames[0]), OutputBytes)
	}
	if len(sinkFrames[1]) != OutputBytes+2 || sinkFrames[1][OutputBytes] != 1 {
		t.Fatalf("active ref frame len = %d, tag = %d, want len=%d tag=1", len(sinkFrames[1]), sinkFrames[1][OutputBytes], OutputBytes+2)
	}

	// 3. Verify 25-frame (500 ms) playbackHold keeps PlaybackActive() true across post-playback echo tail
	desc, err := ReadDescriptor(bytes.NewReader(descBytes))
	if err != nil {
		t.Fatal(err)
	}
	proc := NewProcessor(desc)
	outPkt := make([]byte, OutputBytes)
	proc.Process(activeFrame, outPkt)
	if !proc.PlaybackActive() {
		t.Fatal("expected PlaybackActive() == true on active ref frame")
	}
	for i := 0; i < 25; i++ {
		proc.Process(silentFrame, outPkt)
		if !proc.PlaybackActive() {
			t.Fatalf("expected PlaybackActive() to hold true during tail frame %d", i)
		}
	}
	proc.Process(silentFrame, outPkt)
	if proc.PlaybackActive() {
		t.Fatal("expected PlaybackActive() == false after 25-frame hold expires")
	}
}

