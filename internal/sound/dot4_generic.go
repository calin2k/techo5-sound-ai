//go:build (!arm && !arm64) || noasm

package sound

// dot4I8 accumulates four int8 dot products of length n into out[0..3].
func dot4I8(x, w0, w1, w2, w3 []int8, out *[4]int32) {
	n := len(x)
	if n == 0 {
		return
	}
	_ = w0[n-1]
	_ = w1[n-1]
	_ = w2[n-1]
	_ = w3[n-1]
	a0, a1, a2, a3 := out[0], out[1], out[2], out[3]
	i := 0
	for ; i+4 <= n; i += 4 {
		x0 := int32(x[i+0])
		x1 := int32(x[i+1])
		x2 := int32(x[i+2])
		x3 := int32(x[i+3])
		a0 += x0*int32(w0[i+0]) + x1*int32(w0[i+1]) + x2*int32(w0[i+2]) + x3*int32(w0[i+3])
		a1 += x0*int32(w1[i+0]) + x1*int32(w1[i+1]) + x2*int32(w1[i+2]) + x3*int32(w1[i+3])
		a2 += x0*int32(w2[i+0]) + x1*int32(w2[i+1]) + x2*int32(w2[i+2]) + x3*int32(w2[i+3])
		a3 += x0*int32(w3[i+0]) + x1*int32(w3[i+1]) + x2*int32(w3[i+2]) + x3*int32(w3[i+3])
	}
	for ; i < n; i++ {
		xv := int32(x[i])
		a0 += xv * int32(w0[i])
		a1 += xv * int32(w1[i])
		a2 += xv * int32(w2[i])
		a3 += xv * int32(w3[i])
	}
	out[0], out[1], out[2], out[3] = a0, a1, a2, a3
}
