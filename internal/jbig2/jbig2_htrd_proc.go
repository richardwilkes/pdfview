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
//   - Charges the skip plane, each MMR-coded gray-code plane, the halftone region bitmap, and the grid the region is
//     assembled from against the decode's cumulative pixel budget (embedded.go), and hands that budget to the
//     generic-region proc that decodes the arithmetic gray-code planes, which charges them there. The grid is charged
//     separately because a pattern dictionary of one entry needs no gray-code plane at all, which would otherwise
//     leave an HGW by HGH assembly loop running against nothing charged.
//
// Derived from PDFium's core/fxcodec/jbig2 (Copyright 2014 The PDFium Authors; original code copyright 2014 Foxit
// Software Inc.), BSD-3-Clause; see LICENSE-pdfium in this directory.

package jbig2

import (
	"errors"
)

// HTRDProc 半色调区域解码过程
type HTRDProc struct {
	budget      *budget
	HBW, HBH    uint32
	HMMR        bool
	HTEMPLATE   uint8
	HNUMPATS    uint32
	HPATS       []*Image
	HDEFPIXEL   bool
	HCOMBOP     ComposeOp
	HENABLESKIP bool
	HGW, HGH    uint32
	HGX, HGY    int32
	HRX, HRY    uint16
	HPW, HPH    uint8
}

// NewHTRDProc 创建半色调区域解码过程对象
// 返回: *HTRDProc 对象
func NewHTRDProc() *HTRDProc {
	return &HTRDProc{}
}

// DecodeArith 算术解码
// 入参: arithDecoder 算术解码器, gbContexts 上下文
// 返回: *Image 图像, error 错误信息
func (h *HTRDProc) DecodeArith(arithDecoder *ArithDecoder, gbContexts []ArithCtx) (*Image, error) {
	var hSkip *Image
	if h.HENABLESKIP {
		if err := h.budget.charge(int64(h.HGW), int64(h.HGH)); err != nil {
			return nil, err
		}
		hSkip = NewImage(int32(h.HGW), int32(h.HGH))
		if hSkip == nil {
			return nil, errors.New("failed to create skip image")
		}
		for mg := uint32(0); mg < h.HGH; mg++ {
			xPosition := int64(h.HGX) + int64(mg)*int64(h.HRY)
			yPosition := int64(h.HGY) + int64(mg)*int64(h.HRX)
			for ng := uint32(0); ng < h.HGW; ng++ {
				x := xPosition >> 8
				y := yPosition >> 8
				if (x+int64(h.HPW) <= 0) || (x >= int64(h.HBW)) || (y+int64(h.HPH) <= 0) || (y >= int64(h.HBH)) {
					hSkip.SetPixel(int32(ng), int32(mg), 1)
				}
				xPosition += int64(h.HRX)
				yPosition -= int64(h.HRY)
			}
		}
	}
	hbpp := ceilLog2(h.HNUMPATS)
	grd := NewGRDProc()
	grd.budget = h.budget
	grd.MMR = h.HMMR
	grd.GBW = h.HGW
	grd.GBH = h.HGH
	grd.GBTEMPLATE = h.HTEMPLATE
	grd.TPGDON = false
	grd.USESKIP = h.HENABLESKIP
	grd.SKIP = hSkip
	if h.HTEMPLATE <= 1 {
		grd.GBAT[0] = 3
	} else {
		grd.GBAT[0] = 2
	}
	grd.GBAT[1] = -1
	if grd.GBTEMPLATE == 0 {
		grd.GBAT[2] = -3
		grd.GBAT[3] = -1
		grd.GBAT[4] = 2
		grd.GBAT[5] = -2
		grd.GBAT[6] = -2
		grd.GBAT[7] = -2
	}
	gsbpp := int(hbpp)
	gsplanes := make([]*Image, gsbpp)
	for i := gsbpp - 1; i >= 0; i-- {
		var pImage *Image
		state := &ProgressiveArithDecodeState{
			Image:        &pImage,
			ArithDecoder: arithDecoder,
			GbContexts:   gbContexts,
		}
		status := grd.StartDecodeArith(state)
		if status == JBig2SegmentError {
			return nil, errors.New("arith decoding failure")
		}
		if pImage == nil {
			return nil, errors.New("failed to decode plane")
		}
		gsplanes[i] = pImage
		if i < gsbpp-1 {
			gsplanes[i].ComposeFrom(0, 0, gsplanes[i+1], ComposeXor)
		}
	}
	return h.decodeImage(gsplanes)
}

// DecodeMMR MMR解码
// 入参: stream 位流
// 返回: *Image 半色调区域图像, error 错误信息
func (h *HTRDProc) DecodeMMR(stream *BitStream) (*Image, error) {
	hbpp := ceilLog2(h.HNUMPATS)
	gsbpp := int(hbpp)
	gsplanes := make([]*Image, gsbpp)
	if gsbpp == 0 {
		return h.decodeImage(gsplanes)
	}
	j := gsbpp - 1
	if err := h.budget.charge(int64(h.HGW), int64(h.HGH)); err != nil {
		return nil, err
	}
	decoder := NewMMRDecompressor(int(h.HGW), int(h.HGH), stream)
	pImage, err := decoder.Uncompress()
	if err != nil {
		return nil, err
	}
	gsplanes[j] = pImage
	for j > 0 {
		j--
		if err := h.budget.charge(int64(h.HGW), int64(h.HGH)); err != nil {
			return nil, err
		}
		decoder = NewMMRDecompressor(int(h.HGW), int(h.HGH), stream)
		pImg, err := decoder.Uncompress()
		if err != nil {
			return nil, err
		}
		gsplanes[j] = pImg
		gsplanes[j].ComposeFrom(0, 0, gsplanes[j+1], ComposeXor)
	}
	return h.decodeImage(gsplanes)
}

// decodeImage 解码图像
// 入参: gsplanes 图像平面集合
// 返回: *Image 图像, error 错误信息
func (h *HTRDProc) decodeImage(gsplanes []*Image) (*Image, error) {
	if err := h.budget.charge(int64(h.HBW), int64(h.HBH)); err != nil {
		return nil, err
	}
	// 网格循环的工作量与 HGW×HGH 成正比；灰度平面为空时不会有其它计费，故在此计费
	if err := h.budget.charge(int64(h.HGW), int64(h.HGH)); err != nil {
		return nil, err
	}
	htReg := NewImage(int32(h.HBW), int32(h.HBH))
	if htReg == nil {
		return nil, errors.New("failed to create target image")
	}
	if h.HDEFPIXEL {
		htReg.Fill(true)
	}
	for mg := uint32(0); mg < h.HGH; mg++ {
		xPosition := int64(h.HGX) + int64(mg)*int64(h.HRY)
		yPosition := int64(h.HGY) + int64(mg)*int64(h.HRX)
		for ng := uint32(0); ng < h.HGW; ng++ {
			gsval := uint32(0)
			for i := 0; i < len(gsplanes); i++ {
				bit := gsplanes[i].GetPixel(int32(ng), int32(mg))
				gsval |= uint32(bit) << i
			}
			patIndex := gsval
			if patIndex >= h.HNUMPATS {
				patIndex = h.HNUMPATS - 1
			}
			x := xPosition >> 8
			y := yPosition >> 8
			pat := h.HPATS[patIndex]
			if pat != nil {
				pat.ComposeTo(htReg, int32(x), int32(y), h.HCOMBOP)
			}
			xPosition += int64(h.HRX)
			yPosition -= int64(h.HRY)
		}
	}
	return htReg, nil
}
