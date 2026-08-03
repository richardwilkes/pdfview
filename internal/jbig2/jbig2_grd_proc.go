// Copyright 2026 肖其顿 (XIAO QI DUN)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Modified by the pdfview project (2026-08-02) under section 4(b) of the license above:
//   - Restored PDFium's `&& !USESKIP` conjunct on the three nominal-template predicates. PDFium requires it because
//     its optimized template loops cannot honour a SKIP bitmap; this translation dropped it and re-tested USESKIP
//     inside the loops instead, which leaves the fast paths one edit away from silently ignoring the skip plane.
//   - Charges the region bitmap's area against the decode's cumulative pixel budget (embedded.go) before allocating
//     it. This is the symbol-dictionary work bound: every new symbol bitmap is decoded through here, so charging
//     before the decode loop runs bounds total decode work by total area, which no header scan can do because the
//     dimensions arrive as arithmetic-decoded deltas.
//
// Derived from PDFium's core/fxcodec/jbig2 (Copyright 2014 The PDFium Authors; original code copyright 2014 Foxit
// Software Inc.), BSD-3-Clause; see LICENSE-pdfium in this directory.

package jbig2

import (
	"errors"
)

// GRDProc 通用区域解码过程
type GRDProc struct {
	MMR         bool
	GBW         uint32
	GBH         uint32
	GBTEMPLATE  uint8
	TPGDON      bool
	USESKIP     bool
	SKIP        *Image
	GBAT        [8]int8
	budget      *budget
	loopIndex   uint32
	line        []byte
	decodeType  uint16
	ltp         int
	replaceRect Rect
}

// NewGRDProc 创建通用区域解码过程对象
// 返回: *GRDProc 对象
func NewGRDProc() *GRDProc {
	return &GRDProc{}
}

// ProgressiveArithDecodeState 渐进式算术解码状态
type ProgressiveArithDecodeState struct {
	Image        **Image
	ArithDecoder *ArithDecoder
	GbContexts   []ArithCtx
}

// StartDecodeArith 开始算术解码
// 入参: state 解码状态
// 返回: JBig2SegmentState 状态
func (g *GRDProc) StartDecodeArith(state *ProgressiveArithDecodeState) JBig2SegmentState {
	if g.GBW > JBig2MaxImageSize || g.GBH > JBig2MaxImageSize {
		return JBig2SegmentError
	}
	if g.budget.charge(int64(g.GBW), int64(g.GBH)) != nil {
		return JBig2SegmentError
	}
	created := *state.Image == nil
	if created {
		*state.Image = NewImage(int32(g.GBW), int32(g.GBH))
	}
	if *state.Image == nil {
		return JBig2SegmentError
	}
	if !created {
		(*state.Image).Fill(false)
	}
	g.decodeType = 1
	g.ltp = 0
	g.line = nil
	g.loopIndex = 0
	return g.ProgressiveDecodeArith(state)
}

// StartDecodeMMR 开始MMR解码
// 入参: image 图像指针, stream 位流
// 返回: JBig2SegmentState 状态
func (g *GRDProc) StartDecodeMMR(image **Image, stream *BitStream) JBig2SegmentState {
	if g.GBW > JBig2MaxImageSize || g.GBH > JBig2MaxImageSize {
		return JBig2SegmentError
	}
	if g.budget.charge(int64(g.GBW), int64(g.GBH)) != nil {
		return JBig2SegmentError
	}
	*image = NewImage(int32(g.GBW), int32(g.GBH))
	if *image == nil {
		return JBig2SegmentError
	}
	if err := DecodeG4(stream, *image); err != nil {
		return JBig2SegmentError
	}
	g.replaceRect = Rect{0, 0, int32((*image).Width()), int32((*image).Height())}
	return JBig2SegmentParseComplete
}

// ContinueDecode 继续解码
// 入参: state 解码状态
// 返回: JBig2SegmentState 状态
func (g *GRDProc) ContinueDecode(state *ProgressiveArithDecodeState) JBig2SegmentState {
	if g.decodeType != 1 {
		return JBig2SegmentError
	}
	return g.ProgressiveDecodeArith(state)
}

// DecodeArith 算术解码
// 入参: decoder 解码器, contexts 上下文
// 返回: *Image 图像, error 错误信息
func (g *GRDProc) DecodeArith(decoder *ArithDecoder, contexts []ArithCtx) (*Image, error) {
	state := &ProgressiveArithDecodeState{
		Image:        new(*Image),
		ArithDecoder: decoder,
		GbContexts:   contexts,
	}
	res := g.StartDecodeArith(state)
	if res == JBig2SegmentError {
		return nil, errors.New("decoding error")
	}
	return *state.Image, nil
}

// GetReplaceRect 获取替换区域
// 返回: Rect 区域
func (g *GRDProc) GetReplaceRect() Rect {
	return g.replaceRect
}

// ProgressiveDecodeArith 渐进式算术解码
// 入参: state 解码状态
// 返回: JBig2SegmentState 状态
func (g *GRDProc) ProgressiveDecodeArith(state *ProgressiveArithDecodeState) JBig2SegmentState {
	img := *state.Image
	g.replaceRect = Rect{0, int32(g.loopIndex), int32(img.Width()), int32(g.loopIndex)}
	var res JBig2SegmentState
	switch g.GBTEMPLATE {
	case 0:
		if g.useTemplate0Opt3() {
			res = g.decodeTemplate0Opt3(state)
		} else {
			res = g.decodeTemplate0Unopt(state)
		}
	case 1:
		if g.useTemplate1Opt3() {
			res = g.decodeTemplate1Opt3(state)
		} else {
			res = g.decodeTemplate1Unopt(state)
		}
	case 2:
		if g.useTemplate23Opt3() {
			res = g.decodeTemplate23Opt3(state, 2)
		} else {
			res = g.decodeTemplateUnopt(state, 2)
		}
	default:
		if g.useTemplate23Opt3() {
			res = g.decodeTemplate23Opt3(state, 3)
		} else {
			res = g.decodeTemplate3Unopt(state)
		}
	}
	g.replaceRect.Bottom = int32(g.loopIndex)
	if res == JBig2SegmentParseComplete {
		g.loopIndex = 0
	}
	return res
}

// useTemplate0Opt3 检查是否可用模板0优化3
// 返回: bool 是否可用
func (g *GRDProc) useTemplate0Opt3() bool {
	return g.GBAT[0] == 3 && g.GBAT[1] == -1 && g.GBAT[2] == -3 &&
		g.GBAT[3] == -1 && g.GBAT[4] == 2 && g.GBAT[5] == -2 &&
		g.GBAT[6] == -2 && g.GBAT[7] == -2 && !g.USESKIP
}

// useTemplate1Opt3 检查是否可用模板1优化3
// 返回: bool 是否可用
func (g *GRDProc) useTemplate1Opt3() bool {
	return g.GBAT[0] == 3 && g.GBAT[1] == -1 && !g.USESKIP
}

// useTemplate23Opt3 检查是否可用模板23优化3
// 返回: bool 是否可用
func (g *GRDProc) useTemplate23Opt3() bool {
	return g.GBAT[0] == 2 && g.GBAT[1] == -1 && !g.USESKIP
}
