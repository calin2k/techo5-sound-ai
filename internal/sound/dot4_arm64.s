//go:build arm64 && !noasm

#include "textflag.h"

// func dot4I8ASM(x, w0, w1, w2, w3 *int8, n int, out *[4]int32)
//
// Accumulates four int8 dot products (each a multiple of 8 bytes) in parallel.
// Schedules all 5 signed-byte loads (R11..R15) ahead of the 4 MADD instructions
// so every load has >= 3 instructions of separation before use on in-order
// Cortex-A53 / Cortex-A35 pipelines (zero load-use stalls).
// Never touches R18 (platform register) or R28 (g).
TEXT ·dot4I8ASM(SB), NOSPLIT, $0-56
	MOVD	x+0(FP), R0
	MOVD	w0+8(FP), R1
	MOVD	w1+16(FP), R2
	MOVD	w2+24(FP), R3
	MOVD	w3+32(FP), R4
	MOVD	n+40(FP), R5
	ADD	R0, R5, R5

	MOVD	out+48(FP), R6
	MOVW	0(R6), R7
	MOVW	4(R6), R8
	MOVW	8(R6), R9
	MOVW	12(R6), R10

loop8:
	MOVB	0(R0), R11
	MOVB	0(R1), R12
	MOVB	0(R2), R13
	MOVB	0(R3), R14
	MOVB	0(R4), R15
	MADD	R12, R7, R11, R7
	MADD	R13, R8, R11, R8
	MADD	R14, R9, R11, R9
	MADD	R15, R10, R11, R10

	MOVB	1(R0), R11
	MOVB	1(R1), R12
	MOVB	1(R2), R13
	MOVB	1(R3), R14
	MOVB	1(R4), R15
	MADD	R12, R7, R11, R7
	MADD	R13, R8, R11, R8
	MADD	R14, R9, R11, R9
	MADD	R15, R10, R11, R10

	MOVB	2(R0), R11
	MOVB	2(R1), R12
	MOVB	2(R2), R13
	MOVB	2(R3), R14
	MOVB	2(R4), R15
	MADD	R12, R7, R11, R7
	MADD	R13, R8, R11, R8
	MADD	R14, R9, R11, R9
	MADD	R15, R10, R11, R10

	MOVB	3(R0), R11
	MOVB	3(R1), R12
	MOVB	3(R2), R13
	MOVB	3(R3), R14
	MOVB	3(R4), R15
	MADD	R12, R7, R11, R7
	MADD	R13, R8, R11, R8
	MADD	R14, R9, R11, R9
	MADD	R15, R10, R11, R10

	MOVB	4(R0), R11
	MOVB	4(R1), R12
	MOVB	4(R2), R13
	MOVB	4(R3), R14
	MOVB	4(R4), R15
	MADD	R12, R7, R11, R7
	MADD	R13, R8, R11, R8
	MADD	R14, R9, R11, R9
	MADD	R15, R10, R11, R10

	MOVB	5(R0), R11
	MOVB	5(R1), R12
	MOVB	5(R2), R13
	MOVB	5(R3), R14
	MOVB	5(R4), R15
	MADD	R12, R7, R11, R7
	MADD	R13, R8, R11, R8
	MADD	R14, R9, R11, R9
	MADD	R15, R10, R11, R10

	MOVB	6(R0), R11
	MOVB	6(R1), R12
	MOVB	6(R2), R13
	MOVB	6(R3), R14
	MOVB	6(R4), R15
	MADD	R12, R7, R11, R7
	MADD	R13, R8, R11, R8
	MADD	R14, R9, R11, R9
	MADD	R15, R10, R11, R10

	MOVB	7(R0), R11
	MOVB	7(R1), R12
	MOVB	7(R2), R13
	MOVB	7(R3), R14
	MOVB	7(R4), R15
	MADD	R12, R7, R11, R7
	MADD	R13, R8, R11, R8
	MADD	R14, R9, R11, R9
	MADD	R15, R10, R11, R10

	ADD	$8, R0
	ADD	$8, R1
	ADD	$8, R2
	ADD	$8, R3
	ADD	$8, R4

	CMP	R5, R0
	BLT	loop8

	MOVW	R7, 0(R6)
	MOVW	R8, 4(R6)
	MOVW	R9, 8(R6)
	MOVW	R10, 12(R6)
	RET
