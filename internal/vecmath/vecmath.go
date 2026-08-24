// Copyright (c) 2026 by Richard A. Wilkes. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, version 2.0. If a copy of the MPL was not distributed with
// this file, You can obtain one at http://mozilla.org/MPL/2.0/.
//
// This Source Code Form is "Incompatible With Secondary Licenses", as
// defined by the Mozilla Public License, version 2.0.

//go:build goexperiment.simd

package vecmath

import "simd"

// Reciprocal magic numbers for the two divisors in this module's pixel math. Each pair (M, S) turns a division into
// one 32-bit multiply plus one shift and is exact over the input domain documented on the function that uses it.
// TestUDiv255 and TestUDiv252 verify both exhaustively.
//
// 255 admits the plain floor(n*M>>S) form. 252 does not: no (M, S) with a 32-bit-safe product reproduces
// floor(n/252) for every n <= 0xFFFF, so it uses the biased floor((n+1)*M>>S) form at the cost of one add.
const (
	div255Magic = 32897 // 0x8081 = ceil(2^23/255)
	div255Shift = 23
	div252Magic = 4161 // 0x1041 = floor(2^20/252), applied to n+1
	div252Shift = 20
)

// UDiv255 returns the truncated quotient n/255 in every lane.
//
// The result is exact for every lane value n in [0, 0xFFFF], which covers every product of two 8-bit channel values.
// Larger lane values are not supported: the identity first breaks at n = 66299, and from n = 130559 the multiply
// itself overflows 32 bits. Callers that can exceed 0xFFFF must clamp or split first.
//
// The lane operation is (n * 32897) >> 23, whose largest intermediate is 0xFFFF*32897 = 2155904895, inside uint32.
func UDiv255(n simd.Uint32s) simd.Uint32s {
	return n.Mul(simd.BroadcastUint32s(div255Magic)).ShiftAllRight(div255Shift)
}

// UDiv252 returns the truncated quotient n/252 in every lane.
//
// The result is exact for every lane value n in [0, 0xFFFF]. 252 is the denominator of the MuPDF-matched luminosity
// weighting in internal/render/mask.go, (78*r + 159*g + 15*b + 126) / 252, whose largest numerator is 252*255 + 126 =
// 64386. Larger lane values are not supported: the identity first breaks at n = 262332.
//
// The lane operation is ((n + 1) * 4161) >> 20. The +1 bias makes the identity exact (see the constant block above);
// the largest intermediate is 0x10000*4161 = 272695296, inside uint32.
func UDiv252(n simd.Uint32s) simd.Uint32s {
	return n.Add(simd.BroadcastUint32s(1)).Mul(simd.BroadcastUint32s(div252Magic)).ShiftAllRight(div252Shift)
}
