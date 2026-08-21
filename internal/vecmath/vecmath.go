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

// Reciprocal magic numbers for the two divisors that show up in this module's pixel math. Each pair (M, S) turns a
// division into one 32-bit multiply plus one shift, and each is exact — not approximate — over the input domain
// documented on the function that uses it. Both are verified exhaustively by TestUDiv255 and TestUDiv252.
//
// 255 admits the plain floor(n*M>>S) form. 252 does not: no (M, S) with a 32-bit-safe product reproduces
// floor(n/252) for every n <= 0xFFFF, so it uses the biased floor((n+1)*M>>S) form instead. The bias costs one add.
const (
	div255Magic = 32897 // 0x8081, with div255Shift: ceil(2^23/255)
	div255Shift = 23
	div252Magic = 4161 // 0x1041, with div252Shift: ceil(2^20/252) applied to n+1
	div252Shift = 20
)

// UDiv255 returns the truncated quotient n/255 in every lane.
//
// The result is exact for every lane value n in [0, 0xFFFF] — the domain that covers every product of two 8-bit
// channel values, so all the usual alpha compositing intermediates land inside it. Lane values above 0xFFFF are not
// supported: the first n for which the identity breaks is 66299, and from 130559 up the multiply itself overflows
// 32 bits. Callers that can exceed 0xFFFF must clamp or split first.
//
// The lane operation is (n * 32897) >> 23, whose largest intermediate is 0xFFFF*32897 = 2155904895, comfortably
// inside uint32.
func UDiv255(n simd.Uint32s) simd.Uint32s {
	return n.Mul(simd.BroadcastUint32s(div255Magic)).ShiftAllRight(div255Shift)
}

// UDiv252 returns the truncated quotient n/252 in every lane.
//
// The result is exact for every lane value n in [0, 0xFFFF]. 252 is the denominator of the MuPDF-matched luminosity
// weighting in internal/render/mask.go, (78*r + 159*g + 15*b + 126) / 252, whose largest possible numerator is
// 252*255 + 126 = 64386 — inside the domain with room to spare. Lane values above 0xFFFF are not supported: the
// first n for which the identity breaks is 262332.
//
// The lane operation is ((n + 1) * 4161) >> 20. The +1 bias is what makes the identity exact — see the constant
// block above — and the largest intermediate is 0x10000*4161 = 272695296, comfortably inside uint32.
func UDiv252(n simd.Uint32s) simd.Uint32s {
	return n.Add(simd.BroadcastUint32s(1)).Mul(simd.BroadcastUint32s(div252Magic)).ShiftAllRight(div252Shift)
}
