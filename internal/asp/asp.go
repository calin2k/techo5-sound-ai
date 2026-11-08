// Package asp implements TECHO5's External ASP v1 ("ASP1") stdin/stdout wire protocol.
//
// When installed at /data/techo5-linux/external-asp/bin/external-asp, TECHO5 launches
// this helper automatically whenever "Microphone ASP selected" is set to "External".
// If an upstream processor is present at external-asp.orig, this helper chains to it
// transparently and tees the resulting 16 kHz mono S16LE frames to the techo5-sound-ai
// classifier over a non-blocking Unix datagram socket. Otherwise it mixes the raw
// microphone channels (with lightweight NLMS echo cancellation against the playback
// reference channels) and returns 640-byte 16 kHz mono frames to TECHO5 in < 100 µs.
package asp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	Magic         = "ASP1"
	Version       = 1
	HeaderSize    = 16
	BufferSize    = 28
	ChannelSize   = 8
	ReadyByte     = 0xa5
	OutputSamples = 320
	OutputBytes   = OutputSamples * 2

	FormatS16   uint8 = 1
	FormatS24_3 uint8 = 2
	FormatS32   uint8 = 3

	RoleMIC uint8 = 1
	RoleREF uint8 = 2

	DefaultSocketPath = "/run/techo5-sound-ai.sock"
)

type Buffer struct {
	Offset, Length, SampleRate, Frames, Stride uint32
	Format, ValidBits, LSBShift, Channels      uint8
	FirstChannel                               uint16
}

type Channel struct {
	BufferIndex, Role uint8
	RoleIndex         uint16
	FrameOffset       uint32
}

type Descriptor struct {
	Raw       []byte
	InputSize uint32
	Buffers   []Buffer
	Channels  []Channel
}

// ReadDescriptor reads and validates an ASP1 wire descriptor from r.
func ReadDescriptor(r io.Reader) (Descriptor, error) {
	var hdr [HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Descriptor{}, fmt.Errorf("read ASP1 header: %w", err)
	}
	if string(hdr[0:4]) != Magic {
		return Descriptor{}, errors.New("bad ASP1 magic")
	}
	if ver := binary.LittleEndian.Uint16(hdr[4:6]); ver != Version {
		return Descriptor{}, fmt.Errorf("unsupported ASP1 version %d", ver)
	}
	nb, nc := int(hdr[6]), int(hdr[7])
	if nb < 1 || nc < 1 {
		return Descriptor{}, errors.New("empty ASP1 buffer/channel table")
	}
	sz := int(binary.LittleEndian.Uint32(hdr[8:12]))
	wantSz := HeaderSize + nb*BufferSize + nc*ChannelSize
	if sz != wantSz {
		return Descriptor{}, fmt.Errorf("ASP1 descriptor size %d != %d", sz, wantSz)
	}
	inputSize := binary.LittleEndian.Uint32(hdr[12:16])
	if inputSize == 0 || inputSize > 4*1024*1024 {
		return Descriptor{}, fmt.Errorf("invalid ASP1 input size %d", inputSize)
	}

	raw := make([]byte, sz)
	copy(raw[:HeaderSize], hdr[:])
	if _, err := io.ReadFull(r, raw[HeaderSize:]); err != nil {
		return Descriptor{}, fmt.Errorf("read ASP1 body: %w", err)
	}

	d := Descriptor{
		Raw:       raw,
		InputSize: inputSize,
		Buffers:   make([]Buffer, nb),
		Channels:  make([]Channel, nc),
	}
	p := HeaderSize
	for i := range d.Buffers {
		b := Buffer{
			Offset:       binary.LittleEndian.Uint32(raw[p:]),
			Length:       binary.LittleEndian.Uint32(raw[p+4:]),
			SampleRate:   binary.LittleEndian.Uint32(raw[p+8:]),
			Frames:       binary.LittleEndian.Uint32(raw[p+12:]),
			Stride:       binary.LittleEndian.Uint32(raw[p+16:]),
			Format:       raw[p+20],
			ValidBits:    raw[p+21],
			LSBShift:     raw[p+22],
			Channels:     raw[p+23],
			FirstChannel: binary.LittleEndian.Uint16(raw[p+24:]),
		}
		if b.Length == 0 || b.SampleRate == 0 || b.Frames == 0 || b.Stride == 0 || b.Channels == 0 {
			return Descriptor{}, fmt.Errorf("invalid ASP1 buffer[%d] geometry", i)
		}
		if uint64(b.Offset)+uint64(b.Length) > uint64(inputSize) {
			return Descriptor{}, fmt.Errorf("ASP1 buffer[%d] exceeds input size", i)
		}
		if formatByteWidth(b.Format) == 0 {
			return Descriptor{}, fmt.Errorf("unsupported ASP1 buffer[%d] format %d", i, b.Format)
		}
		d.Buffers[i] = b
		p += BufferSize
	}
	micChannels := 0
	for i := range d.Channels {
		ch := Channel{
			BufferIndex: raw[p],
			Role:        raw[p+1],
			RoleIndex:   binary.LittleEndian.Uint16(raw[p+2:]),
			FrameOffset: binary.LittleEndian.Uint32(raw[p+4:]),
		}
		if int(ch.BufferIndex) >= len(d.Buffers) {
			return Descriptor{}, fmt.Errorf("ASP1 channel[%d] buffer index %d out of range", i, ch.BufferIndex)
		}
		if ch.Role != RoleMIC && ch.Role != RoleREF {
			return Descriptor{}, fmt.Errorf("ASP1 channel[%d] unknown role %d", i, ch.Role)
		}
		if ch.Role == RoleMIC {
			micChannels++
		}
		b := d.Buffers[ch.BufferIndex]
		w := uint64(formatByteWidth(b.Format))
		if uint64(ch.FrameOffset)+w > uint64(b.Stride) ||
			(uint64(b.Frames)-1)*uint64(b.Stride)+uint64(ch.FrameOffset)+w > uint64(b.Length) {
			return Descriptor{}, fmt.Errorf("ASP1 channel[%d] frame offset %d out of buffer bounds", i, ch.FrameOffset)
		}
		d.Channels[i] = ch
		p += ChannelSize
	}
	if micChannels == 0 {
		return Descriptor{}, errors.New("ASP1 descriptor has no MIC channels")
	}
	return d, nil
}

func formatByteWidth(format uint8) int {
	switch format {
	case FormatS16:
		return 2
	case FormatS24_3:
		return 3
	case FormatS32:
		return 4
	default:
		return 0
	}
}

// ExtractSample reads one channel sample from buf at byte offset pos and scales it to int16.
func ExtractSample(buf []byte, pos int, format, validBits, lsbShift uint8) int16 {
	if pos < 0 {
		return 0
	}
	switch format {
	case FormatS16:
		if pos+2 > len(buf) {
			return 0
		}
		v := int32(int16(binary.LittleEndian.Uint16(buf[pos:])))
		if lsbShift > 0 && lsbShift < 16 {
			v >>= lsbShift
		}
		return int16(v)
	case FormatS24_3:
		if pos+3 > len(buf) {
			return 0
		}
		// Sign-extend 24-bit little-endian into int32, then narrow top 16 valid bits.
		v := int32(uint32(buf[pos]) | uint32(buf[pos+1])<<8 | uint32(int8(buf[pos+2]))<<16)
		shift := int(lsbShift) + int(validBits) - 16
		if shift > 0 && shift < 24 {
			v >>= shift
		} else {
			v >>= 8
		}
		return clamp16(v)
	case FormatS32:
		if pos+4 > len(buf) {
			return 0
		}
		v := int32(binary.LittleEndian.Uint32(buf[pos:]))
		shift := int(lsbShift) + int(validBits) - 16
		if shift > 0 && shift < 32 {
			v >>= shift
		} else {
			v >>= 16
		}
		return clamp16(v)
	default:
		return 0
	}
}

func clamp16(v int32) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// Processor mixes multi-channel ASP1 buffers into 320-sample (20 ms @ 16 kHz) mono S16LE frames
// and applies a lightweight 64-tap NLMS adaptive echo canceller when playback reference channels
// carry audio.
type Processor struct {
	desc     Descriptor
	Leveling bool
	gain     float32
	floor    float32
	w        [64]float32
	x        [64]float32
	pos      int
}

func NewProcessor(d Descriptor) *Processor {
	return &Processor{
		desc:     d,
		Leveling: true,
		gain:     10.0, // +20 dB initial gain matching TECHO5's leveler
		floor:    32768.0,
	}
}

// Process converts one ASP1 input block into a 640-byte (320 int16) mono S16LE frame.
func (p *Processor) Process(in []byte, out []byte) {
	if p == nil || len(out) < OutputBytes {
		return
	}
	var micSum [OutputSamples]int32
	var refSum [OutputSamples]int32
	micCount := 0
	refCount := 0

	for _, ch := range p.desc.Channels {
		if int(ch.BufferIndex) >= len(p.desc.Buffers) {
			continue
		}
		b := p.desc.Buffers[ch.BufferIndex]
		if uint64(b.Offset)+uint64(b.Length) > uint64(len(in)) || b.Frames == 0 {
			continue
		}
		raw := in[b.Offset : b.Offset+b.Length]
		step := int(b.Frames) / OutputSamples
		if step < 1 {
			step = 1
		}
		switch ch.Role {
		case RoleMIC:
			micCount++
			for i := 0; i < OutputSamples; i++ {
				var acc int32
				for s := 0; s < step; s++ {
					f := i*step + s
					if uint32(f) >= b.Frames {
						f = int(b.Frames) - 1
					}
					off := f*int(b.Stride) + int(ch.FrameOffset)
					acc += int32(ExtractSample(raw, off, b.Format, b.ValidBits, b.LSBShift))
				}
				micSum[i] += acc / int32(step)
			}
		case RoleREF:
			refCount++
			for i := 0; i < OutputSamples; i++ {
				var acc int32
				for s := 0; s < step; s++ {
					f := i*step + s
					if uint32(f) >= b.Frames {
						f = int(b.Frames) - 1
					}
					off := f*int(b.Stride) + int(ch.FrameOffset)
					acc += int32(ExtractSample(raw, off, b.Format, b.ValidBits, b.LSBShift))
				}
				refSum[i] += acc / int32(step)
			}
		}
	}

	if micCount == 0 {
		clear(out[:OutputBytes])
		return
	}

	var refEnergy int64
	if refCount > 0 {
		for i := 0; i < OutputSamples; i++ {
			r := refSum[i] / int32(refCount)
			refSum[i] = r
			refEnergy += int64(r) * int64(r)
		}
	}

	for i := 0; i < OutputSamples; i++ {
		m := micSum[i] / int32(micCount)
		if refCount > 0 && refEnergy > int64(OutputSamples)*64 {
			r := float32(refSum[i]) / 32768.0
			d := float32(m) / 32768.0
			p.x[p.pos] = r
			var y, norm float32
			idx := p.pos
			for k := 0; k < len(p.w); k++ {
				xk := p.x[idx]
				y += p.w[k] * xk
				norm += xk * xk
				if idx == 0 {
					idx = len(p.x) - 1
				} else {
					idx--
				}
			}
			e := d - y
			if math.IsNaN(float64(e)) || math.IsInf(float64(e), 0) {
				p.w = [64]float32{}
				p.x = [64]float32{}
				e = d
			} else {
				mu := float32(0.08) / (norm + 1e-4)
				idx = p.pos
				for k := 0; k < len(p.w); k++ {
					p.w[k] += mu * e * p.x[idx]
					if idx == 0 {
						idx = len(p.x) - 1
					} else {
						idx--
					}
				}
			}
			p.pos = (p.pos + 1) % len(p.x)
			m = int32(e * 32768.0)
		}
		s := clamp16(m)
		micSum[i] = int32(s)
	}

	if p.Leveling {
		var sumSq float64
		var peak float32
		for i := 0; i < OutputSamples; i++ {
			v := float32(micSum[i])
			sumSq += float64(v * v)
			if av := float32(math.Abs(float64(v))); av > peak {
				peak = av
			}
		}
		rms := float32(math.Sqrt(sumSq / float64(OutputSamples)))
		stepFloor := float32(0.00033)
		if rms < p.floor {
			stepFloor = 0.095
		}
		p.floor = max(p.floor+(rms-p.floor)*stepFloor, 16.0)
		if rms > p.floor*3.98 {
			want := min(max(2319.0/rms, 1.0), 22.38)
			stepGain := float32(0.004)
			if want < p.gain {
				stepGain = 0.039
			}
			p.gain += (want - p.gain) * stepGain
		}
		if peak > 0 {
			p.gain = min(p.gain, 23197.0/peak)
		}
		for i := 0; i < OutputSamples; i++ {
			s := clamp16(int32(float32(micSum[i]) * p.gain))
			binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
		}
		return
	}

	for i := 0; i < OutputSamples; i++ {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(micSum[i])))
	}
}

// Run executes the ASP1 stdin->stdout loop, optionally chaining to origPath if non-empty and
// executable, and teeing every 640-byte mono S16LE frame to sink (if non-nil).
func Run(in io.Reader, out io.Writer, origPath string, sink func([]byte)) error {
	desc, err := ReadDescriptor(in)
	if err != nil {
		return err
	}

	if origPath != "" {
		if st, err := os.Stat(origPath); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return runChained(in, out, desc, origPath, sink)
		}
	}

	if _, err := out.Write([]byte{ReadyByte}); err != nil {
		return err
	}

	proc := NewProcessor(desc)
	inBuf := make([]byte, desc.InputSize)
	outBuf := make([]byte, OutputBytes)

	for {
		if _, err := io.ReadFull(in, inBuf); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		proc.Process(inBuf, outBuf)
		if _, err := out.Write(outBuf); err != nil {
			return err
		}
		if sink != nil {
			sink(outBuf)
		}
	}
}

func runChained(in io.Reader, out io.Writer, desc Descriptor, origPath string, sink func([]byte)) error {
	cmd := exec.Command(origPath)
	childIn, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	childOut, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		_ = childIn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	if _, err := childIn.Write(desc.Raw); err != nil {
		return err
	}
	var ready [1]byte
	if _, err := io.ReadFull(childOut, ready[:]); err != nil {
		return err
	}
	if _, err := out.Write(ready[:]); err != nil {
		return err
	}
	if ready[0] != ReadyByte {
		return fmt.Errorf("upstream ASP returned ready %#x", ready[0])
	}

	inBuf := make([]byte, desc.InputSize)
	outBuf := make([]byte, OutputBytes)
	for {
		if _, err := io.ReadFull(in, inBuf); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if _, err := childIn.Write(inBuf); err != nil {
			return err
		}
		if _, err := io.ReadFull(childOut, outBuf); err != nil {
			return err
		}
		if _, err := out.Write(outBuf); err != nil {
			return err
		}
		if sink != nil {
			sink(outBuf)
		}
	}
}

// NewUnixDatagramSink returns a non-blocking sink function that sends 640-byte mono PCM
// frames to a Unix domain datagram socket (e.g. /run/techo5-sound-ai.sock).
//
// It uses an unconnected unixgram socket with O_NONBLOCK so that:
//  1. Restarting the daemon (which unlinks and recreates sockPath) never breaks a running
//     external-asp process.
//  2. If the daemon is not listening or its socket receive queue is full, frames are dropped
//     immediately without ever delaying TECHO5's 80 ms real-time ASP1 exchange deadline.
func NewUnixDatagramSink(sockPath string) (func([]byte), func()) {
	uconn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: "", Net: "unixgram"})
	if err != nil {
		return func([]byte) {}, func() {}
	}
	if rc, err := uconn.SyscallConn(); err == nil {
		_ = rc.Control(func(fd uintptr) {
			_ = syscall.SetNonblock(int(fd), true)
		})
	}
	dst := &net.UnixAddr{Name: sockPath, Net: "unixgram"}
	return func(frame []byte) {
		_ = uconn.SetWriteDeadline(time.Now().Add(time.Millisecond))
		_, _ = uconn.WriteToUnix(frame, dst)
	}, func() { _ = uconn.Close() }
}
