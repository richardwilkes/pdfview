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
//   - Restored PDFium's checked arithmetic in both export-run loops. PDFium accumulates EXINDEX + EXRUNLENGTH through
//     FX_SAFE_UINT32 and rejects an invalid sum; this translation added them as plain uint32, so a run length that
//     the arithmetic (or Huffman) decoder returns as a large value wraps past the bounds test and the fill loop then
//     indexes EXFLAGS out of range — a panic on hostile input. The sums are now taken in uint64 against the flag
//     slice's own length.
//   - Bounded the height-class loop by the declared new-symbol count. Each height class yields at least one symbol,
//     so the bound rejects nothing legitimate; without it a stream whose width decoder reports out-of-band on every
//     class spins forever, the arithmetic decoder having no end of input to report.
//   - Restored PDFium's export-count guard on the loop that fills the dictionary (`j >= SDNUMEXSYMS`), which bounds
//     what is exported by what the segment declared even if the run lengths say otherwise.
//   - Bounded the arithmetic export-flag loop by the decoder's end of input (the GRD/GRRD IsComplete pattern, which
//     PDFium does not apply to this loop): a zero-length export run is legitimate mid-stream, but an exhausted
//     decoder can return zero-length runs forever, and EXINDEX then never advances — a live CPU spin on hostile
//     input. The Huffman export loop needs no guard; its bit reads fail at end of stream.
//   - Charges every bitmap this file allocates — the Huffman height-class collective bitmap, each symbol split out of
//     it, and each input symbol duplicated on export — against the decode's cumulative pixel budget (embedded.go),
//     and hands that budget to the procs it drives, whose own bitmaps are charged there.
//
// Derived from PDFium's core/fxcodec/jbig2 (Copyright 2014 The PDFium Authors; original code copyright 2014 Foxit
// Software Inc.), BSD-3-Clause; see LICENSE-pdfium in this directory.

package jbig2

import (
	"errors"
)

// SDDProc 符号字典解码过程
type SDDProc struct {
	budget        *budget
	SDHUFF        bool
	SDREFAGG      bool
	SDMMR         bool
	SDRTEMPLATE   bool
	SDTEMPLATE    uint8
	SDNUMINSYMS   uint32
	SDNUMNEWSYMS  uint32
	SDNUMEXSYMS   uint32
	SDINSYMS      []*Image
	SDHUFFDH      *HuffmanTable
	SDHUFFDW      *HuffmanTable
	SDHUFFBMSIZE  *HuffmanTable
	SDHUFFAGGINST *HuffmanTable
	SDAT          [8]int8
	SDRAT         [4]int8
}

// NewSDDProc 创建符号字典解码过程对象
// 返回: *SDDProc 对象
func NewSDDProc() *SDDProc {
	return &SDDProc{}
}

// DecodeArith 算术解码
// 入参: arithDecoder 算术解码器, gbContexts 通用上下文, grContexts 细化上下文
// 返回: *SymbolDict 符号字典, error 错误信息
func (s *SDDProc) DecodeArith(arithDecoder *ArithDecoder, gbContexts, grContexts []ArithCtx) (*SymbolDict, error) {
	IADH := NewArithIntDecoder()
	IADW := NewArithIntDecoder()
	IAAI := NewArithIntDecoder()
	IARDX := NewArithIntDecoder()
	IARDY := NewArithIntDecoder()
	IAEX := NewArithIntDecoder()
	IADT := NewArithIntDecoder()
	IAFS := NewArithIntDecoder()
	IADS := NewArithIntDecoder()
	IAIT := NewArithIntDecoder()
	IARI := NewArithIntDecoder()
	IARDW := NewArithIntDecoder()
	IARDH := NewArithIntDecoder()
	SBSYMCODELENA := ceilLog2(s.SDNUMINSYMS + s.SDNUMNEWSYMS)
	IAID := NewArithIaidDecoder(SBSYMCODELENA)
	SDNEWSYMS := make([]*Image, s.SDNUMNEWSYMS)
	HCHEIGHT := uint32(0)
	NSYMSDECODED := uint32(0)
	// 每个高度类至少产生一个符号，类数不会超过 SDNUMNEWSYMS；算术解码器在数据耗尽后仍会持续产出，
	// 空类若不设上限便可无限循环
	HCLASSES := uint32(0)
	for NSYMSDECODED < s.SDNUMNEWSYMS {
		if HCLASSES > s.SDNUMNEWSYMS {
			return nil, errors.New("too many height classes")
		}
		HCLASSES++
		var BS *Image
		HCDH, ok := IADH.Decode(arithDecoder)
		if !ok {
			return nil, errors.New("failed to decode hcdh")
		}
		HCHEIGHT = uint32(int32(HCHEIGHT) + HCDH)
		if HCHEIGHT > JBig2MaxImageSize {
			return nil, errors.New("image height too large")
		}
		SYMWIDTH := uint32(0)
		for {
			DW, ok := IADW.Decode(arithDecoder)
			if !ok {
				break
			}
			if NSYMSDECODED >= s.SDNUMNEWSYMS {
				return nil, errors.New("too many symbols decoded")
			}
			SYMWIDTH = uint32(int32(SYMWIDTH) + DW)
			if SYMWIDTH > JBig2MaxImageSize {
				return nil, errors.New("image width too large")
			}
			if HCHEIGHT == 0 || SYMWIDTH == 0 {
				NSYMSDECODED++
				continue
			}
			if !s.SDREFAGG {
				pGRD := NewGRDProc()
				pGRD.budget = s.budget
				pGRD.MMR = false
				pGRD.GBW = SYMWIDTH
				pGRD.GBH = HCHEIGHT
				pGRD.GBTEMPLATE = s.SDTEMPLATE
				pGRD.TPGDON = false
				pGRD.USESKIP = false
				copy(pGRD.GBAT[:], s.SDAT[:])
				var err error
				BS, err = pGRD.DecodeArith(arithDecoder, gbContexts)
				if err != nil {
					return nil, err
				}
			} else {
				REFAGGNINST, ok := IAAI.Decode(arithDecoder)
				if !ok {
					return nil, errors.New("failed to decode refaggninst")
				}
				if REFAGGNINST > 1 {
					pDecoder := NewTRDProc()
					pDecoder.budget = s.budget
					pDecoder.SBHUFF = s.SDHUFF
					pDecoder.SBREFINE = true
					pDecoder.SBW = SYMWIDTH
					pDecoder.SBH = HCHEIGHT
					pDecoder.SBNUMINSTANCES = uint32(REFAGGNINST)
					pDecoder.SBSTRIPS = 1
					pDecoder.SBNUMSYMS = s.SDNUMINSYMS + NSYMSDECODED
					pDecoder.SBSYMCODELEN = ceilLog2(pDecoder.SBNUMSYMS)
					pDecoder.SBSYMS = make([]*Image, pDecoder.SBNUMSYMS)
					copy(pDecoder.SBSYMS, s.SDINSYMS)
					for i := 0; i < int(NSYMSDECODED); i++ {
						pDecoder.SBSYMS[int(s.SDNUMINSYMS)+i] = SDNEWSYMS[i]
					}
					pDecoder.SBDEFPIXEL = false
					pDecoder.SBCOMBOP = ComposeOr
					pDecoder.TRANSPOSED = false
					pDecoder.REFCORNER = JBig2CornerTopLeft
					pDecoder.SBDSOFFSET = 0
					pDecoder.SBRTEMPLATE = s.SDRTEMPLATE
					pDecoder.SBRAT = s.SDRAT
					ids := &IntDecoderState{
						IADT: IADT, IAFS: IAFS, IADS: IADS, IAIT: IAIT,
						IARI: IARI, IARDW: IARDW, IARDH: IARDH, IARDX: IARDX, IARDY: IARDY,
						IAID: IAID,
					}
					var err error
					BS, err = pDecoder.DecodeArith(arithDecoder, grContexts, ids)
					if err != nil {
						return nil, err
					}
				} else if REFAGGNINST == 1 {
					SBNUMSYMS := s.SDNUMINSYMS + NSYMSDECODED
					IDI, err := IAID.Decode(arithDecoder)
					if err != nil {
						return nil, err
					}
					if uint32(IDI) >= SBNUMSYMS {
						return nil, errors.New("idi out of bounds")
					}
					var sbsyms_idi *Image
					if uint32(IDI) < s.SDNUMINSYMS {
						sbsyms_idi = s.SDINSYMS[IDI]
					} else {
						sbsyms_idi = SDNEWSYMS[uint32(IDI)-s.SDNUMINSYMS]
					}
					if sbsyms_idi == nil {
						return nil, errors.New("referenced symbol is nil")
					}
					RDXI, ok := IARDX.Decode(arithDecoder)
					if !ok {
						return nil, errors.New("failed to decode refinement x")
					}
					RDYI, ok := IARDY.Decode(arithDecoder)
					if !ok {
						return nil, errors.New("failed to decode refinement y")
					}
					pGRRD := NewGRRDProc()
					pGRRD.budget = s.budget
					pGRRD.GRW = SYMWIDTH
					pGRRD.GRH = HCHEIGHT
					pGRRD.GRTEMPLATE = s.SDRTEMPLATE
					pGRRD.GRREFERENCE = sbsyms_idi
					pGRRD.GRREFERENCEDX = RDXI
					pGRRD.GRREFERENCEDY = RDYI
					pGRRD.TPGRON = false
					pGRRD.GRAT = s.SDRAT
					BS, err = pGRRD.Decode(arithDecoder, grContexts)
					if err != nil {
						return nil, err
					}
				}
			}
			SDNEWSYMS[NSYMSDECODED] = BS
			NSYMSDECODED++
		}
	}
	EXFLAGS := make([]bool, s.SDNUMINSYMS+s.SDNUMNEWSYMS)
	CUREXFLAG := false
	EXINDEX := uint32(0)
	num_ex_syms := uint32(0)
	for EXINDEX < s.SDNUMINSYMS+s.SDNUMNEWSYMS {
		// 零长度游程本身合法，但数据耗尽后解码器可能恒返回零游程，EXINDEX 便停滞不前；
		// 与 GRD/GRRD 相同，先检查数据是否已经读完，避免无限循环
		if arithDecoder.IsComplete() {
			return nil, errors.New("data exhausted before export flags completed")
		}
		EXRUNLENGTH, ok := IAEX.Decode(arithDecoder)
		if !ok {
			return nil, errors.New("failed to decode exrunlength")
		}
		newExSize := uint64(EXINDEX) + uint64(uint32(EXRUNLENGTH))
		if newExSize > uint64(len(EXFLAGS)) {
			return nil, errors.New("exrunlength out of bounds")
		}
		if CUREXFLAG {
			num_ex_syms += uint32(EXRUNLENGTH)
		}
		for i := uint32(0); i < uint32(EXRUNLENGTH); i++ {
			EXFLAGS[EXINDEX+i] = CUREXFLAG
		}
		EXINDEX = uint32(newExSize)
		CUREXFLAG = !CUREXFLAG
	}
	if num_ex_syms > s.SDNUMEXSYMS {
		return nil, errors.New("too many exported symbols")
	}
	dict := NewSymbolDict()
	exported := uint32(0)
	for i := uint32(0); i < s.SDNUMINSYMS+s.SDNUMNEWSYMS; i++ {
		if !EXFLAGS[i] || exported >= s.SDNUMEXSYMS {
			continue
		}
		if i < s.SDNUMINSYMS {
			img := s.SDINSYMS[i]
			if img != nil {
				if err := s.budget.charge(int64(img.width), int64(img.height)); err != nil {
					return nil, err
				}
				newImg := img.Duplicate()
				dict.AddImage(newImg)
			} else {
				dict.AddImage(nil)
			}
		} else {
			dict.AddImage(SDNEWSYMS[i-s.SDNUMINSYMS])
		}
		exported++
	}
	return dict, nil
}

// DecodeHuffman 霍夫曼解码
// 入参: stream 位流, gbContexts 通用上下文, grContexts 细化上下文
// 返回: *SymbolDict 符号字典, error 错误信息
func (s *SDDProc) DecodeHuffman(stream *BitStream, gbContexts, grContexts []ArithCtx) (*SymbolDict, error) {
	huffmanDecoder := NewHuffmanDecoder(stream)
	SDNEWSYMS := make([]*Image, s.SDNUMNEWSYMS)
	var SDNEWSYMWIDTHS []uint32
	if !s.SDREFAGG {
		SDNEWSYMWIDTHS = make([]uint32, s.SDNUMNEWSYMS)
	}
	HCHEIGHT := uint32(0)
	NSYMSDECODED := uint32(0)
	HCLASSES := uint32(0)
	var aggregateTables [8]*HuffmanTable
	if s.SDREFAGG {
		aggregateTables = [8]*HuffmanTable{
			NewStandardTable(6), NewStandardTable(8), NewStandardTable(11),
			NewStandardTable(15), NewStandardTable(15), NewStandardTable(15),
			NewStandardTable(15), NewStandardTable(1),
		}
	}
	for NSYMSDECODED < s.SDNUMNEWSYMS {
		if HCLASSES > s.SDNUMNEWSYMS {
			return nil, errors.New("too many height classes")
		}
		HCLASSES++
		var HCDH int32
		if res := huffmanDecoder.DecodeAValue(s.SDHUFFDH, &HCDH); res != 0 {
			return nil, errors.New("failed to decode hcdh")
		}
		HCHEIGHT = uint32(int32(HCHEIGHT) + HCDH)
		if HCHEIGHT > JBig2MaxImageSize {
			return nil, errors.New("image height too large")
		}
		SYMWIDTH := uint32(0)
		TOTWIDTH := uint32(0)
		HCFIRSTSYM := NSYMSDECODED
		for {
			var DW int32
			res := huffmanDecoder.DecodeAValue(s.SDHUFFDW, &DW)
			if res == JBig2OOB {
				break
			}
			if res != 0 {
				return nil, errors.New("failed to decode dw")
			}
			if NSYMSDECODED >= s.SDNUMNEWSYMS {
				return nil, errors.New("too many symbols decoded")
			}
			SYMWIDTH = uint32(int32(SYMWIDTH) + DW)
			if SYMWIDTH > JBig2MaxImageSize {
				return nil, errors.New("image width too large")
			}
			TOTWIDTH += SYMWIDTH
			if HCHEIGHT == 0 || SYMWIDTH == 0 {
				NSYMSDECODED++
				continue
			}
			var BS *Image
			if s.SDREFAGG {
				var REFAGGNINST int32
				if huffmanDecoder.DecodeAValue(s.SDHUFFAGGINST, &REFAGGNINST) != 0 {
					return nil, errors.New("failed to decode refaggninst")
				}
				if REFAGGNINST > 1 {
					pDecoder := NewTRDProc()
					pDecoder.budget = s.budget
					pDecoder.SBHUFF = s.SDHUFF
					pDecoder.SBREFINE = true
					pDecoder.SBW = SYMWIDTH
					pDecoder.SBH = HCHEIGHT
					pDecoder.SBNUMINSTANCES = uint32(REFAGGNINST)
					pDecoder.SBSTRIPS = 1
					pDecoder.SBNUMSYMS = s.SDNUMINSYMS + NSYMSDECODED
					pDecoder.SBSYMCODES = make([]HuffmanCode, pDecoder.SBNUMSYMS)
					nTmp := uint32(ceilLog2(pDecoder.SBNUMSYMS))
					if nTmp == 0 {
						nTmp = 1
					}
					for i := uint32(0); i < pDecoder.SBNUMSYMS; i++ {
						pDecoder.SBSYMCODES[i].Codelen = int32(nTmp)
						pDecoder.SBSYMCODES[i].Code = int32(i)
					}
					pDecoder.SBSYMS = make([]*Image, pDecoder.SBNUMSYMS)
					copy(pDecoder.SBSYMS, s.SDINSYMS)
					for i := 0; i < int(NSYMSDECODED); i++ {
						pDecoder.SBSYMS[int(s.SDNUMINSYMS)+i] = SDNEWSYMS[i]
					}
					pDecoder.SBDEFPIXEL = false
					pDecoder.SBCOMBOP = ComposeOr
					pDecoder.TRANSPOSED = false
					pDecoder.REFCORNER = JBig2CornerTopLeft
					pDecoder.SBDSOFFSET = 0
					pDecoder.SBHUFFFS = aggregateTables[0]
					pDecoder.SBHUFFDS = aggregateTables[1]
					pDecoder.SBHUFFDT = aggregateTables[2]
					pDecoder.SBHUFFRDW = aggregateTables[3]
					pDecoder.SBHUFFRDH = aggregateTables[4]
					pDecoder.SBHUFFRDX = aggregateTables[5]
					pDecoder.SBHUFFRDY = aggregateTables[6]
					pDecoder.SBHUFFRSIZE = aggregateTables[7]
					pDecoder.SBRTEMPLATE = s.SDRTEMPLATE
					pDecoder.SBRAT = s.SDRAT
					var err error
					BS, err = pDecoder.DecodeHuffman(stream, grContexts)
					if err != nil {
						return nil, err
					}
				} else if REFAGGNINST == 1 {
					SBNUMSYMS := s.SDNUMINSYMS + NSYMSDECODED
					nTmp := uint32(ceilLog2(SBNUMSYMS))
					if nTmp == 0 {
						nTmp = 1
					}
					SBSYMCODELEN := nTmp
					IDI := uint32(0)
					for n := uint32(0); n < SBSYMCODELEN; n++ {
						val, err := stream.Read1Bit()
						if err != nil {
							return nil, err
						}
						IDI = (IDI << 1) | val
					}
					if IDI >= SBNUMSYMS {
						return nil, errors.New("idi out of bounds")
					}
					var sbsyms_idi *Image
					if IDI < s.SDNUMINSYMS {
						sbsyms_idi = s.SDINSYMS[IDI]
					} else {
						sbsyms_idi = SDNEWSYMS[IDI-s.SDNUMINSYMS]
					}
					if sbsyms_idi == nil {
						return nil, errors.New("referenced symbol is nil")
					}
					var RDXI, RDYI, nVal int32
					if huffmanDecoder.DecodeAValue(aggregateTables[3], &RDXI) != 0 ||
						huffmanDecoder.DecodeAValue(aggregateTables[3], &RDYI) != 0 ||
						huffmanDecoder.DecodeAValue(aggregateTables[7], &nVal) != 0 {
						return nil, errors.New("failed to decode refinement values")
					}
					stream.AlignByte()
					pGRRD := NewGRRDProc()
					pGRRD.budget = s.budget
					pGRRD.GRW = SYMWIDTH
					pGRRD.GRH = HCHEIGHT
					pGRRD.GRTEMPLATE = s.SDRTEMPLATE
					pGRRD.GRREFERENCE = sbsyms_idi
					pGRRD.GRREFERENCEDX = RDXI
					pGRRD.GRREFERENCEDY = RDYI
					pGRRD.TPGRON = false
					pGRRD.GRAT = s.SDRAT
					arithDecoder := NewArithDecoder(stream)
					var err error
					BS, err = pGRRD.Decode(arithDecoder, grContexts)
					if err != nil {
						return nil, err
					}
					stream.AlignByte()
					stream.AddOffset(2)
				}
				SDNEWSYMS[NSYMSDECODED] = BS
			}
			if !s.SDREFAGG {
				SDNEWSYMWIDTHS[NSYMSDECODED] = SYMWIDTH
			}
			NSYMSDECODED++
		}
		if !s.SDREFAGG {
			var BMSIZE int32
			if huffmanDecoder.DecodeAValue(s.SDHUFFBMSIZE, &BMSIZE) != 0 {
				return nil, errors.New("failed to decode bmsize")
			}
			stream.AlignByte()
			var BHC *Image
			if BMSIZE == 0 {
				stride := (TOTWIDTH + 7) / 8
				if stream.GetByteLeft() < stride*HCHEIGHT {
					return nil, errors.New("insufficient data for grid")
				}
				if err := s.budget.charge(int64(TOTWIDTH), int64(HCHEIGHT)); err != nil {
					return nil, err
				}
				BHC = NewImage(int32(TOTWIDTH), int32(HCHEIGHT))
				if BHC == nil {
					return nil, errors.New("failed to create grid image")
				}
				dataSize := stride * HCHEIGHT
				copy(BHC.Data(), stream.GetPointer()[:dataSize])
				stream.AddOffset(dataSize)
			} else {
				pGRD := NewGRDProc()
				pGRD.budget = s.budget
				pGRD.MMR = true
				pGRD.GBW = TOTWIDTH
				pGRD.GBH = HCHEIGHT
				if stream.GetByteLeft() < uint32(BMSIZE) {
					return nil, errors.New("insufficient data for mmr")
				}
				mmrData := stream.GetPointer()[:BMSIZE]
				stream.AddOffset(uint32(BMSIZE))
				mmrStream := NewBitStream(mmrData, 0)
				if pGRD.StartDecodeMMR(&BHC, mmrStream) == JBig2SegmentError || BHC == nil {
					return nil, errors.New("mmr decoding failure")
				}
			}
			if BHC != nil {
				nTmp := uint32(0)
				currentSym := HCFIRSTSYM
				for i := uint32(0); i < NSYMSDECODED-HCFIRSTSYM; i++ {
					idx := currentSym + i
					if err := s.budget.charge(int64(SDNEWSYMWIDTHS[idx]), int64(HCHEIGHT)); err != nil {
						return nil, err
					}
					SDNEWSYMS[idx] = BHC.SubImage(int32(nTmp), 0, int32(SDNEWSYMWIDTHS[idx]), int32(HCHEIGHT))
					nTmp += SDNEWSYMWIDTHS[idx]
				}
			}
		}
	}
	EXFLAGS := make([]bool, s.SDNUMINSYMS+s.SDNUMNEWSYMS)
	CUREXFLAG := false
	EXINDEX := uint32(0)
	num_ex_syms := uint32(0)
	pTable := NewStandardTable(1)
	for EXINDEX < s.SDNUMINSYMS+s.SDNUMNEWSYMS {
		var EXRUNLENGTH int32
		if res := huffmanDecoder.DecodeAValue(pTable, &EXRUNLENGTH); res != 0 {
			return nil, errors.New("failed to decode exrunlength")
		}
		newExSize := uint64(EXINDEX) + uint64(uint32(EXRUNLENGTH))
		if newExSize > uint64(len(EXFLAGS)) {
			return nil, errors.New("exrunlength out of bounds")
		}
		if CUREXFLAG {
			num_ex_syms += uint32(EXRUNLENGTH)
		}
		for i := uint32(0); i < uint32(EXRUNLENGTH); i++ {
			EXFLAGS[EXINDEX+i] = CUREXFLAG
		}
		EXINDEX = uint32(newExSize)
		CUREXFLAG = !CUREXFLAG
	}
	if num_ex_syms > s.SDNUMEXSYMS {
		return nil, errors.New("too many exported symbols")
	}
	dict := NewSymbolDict()
	exported := uint32(0)
	for i := uint32(0); i < s.SDNUMINSYMS+s.SDNUMNEWSYMS; i++ {
		if !EXFLAGS[i] || exported >= s.SDNUMEXSYMS {
			continue
		}
		if i < s.SDNUMINSYMS {
			img := s.SDINSYMS[i]
			if img != nil {
				if err := s.budget.charge(int64(img.width), int64(img.height)); err != nil {
					return nil, err
				}
				newImg := img.Duplicate()
				dict.AddImage(newImg)
			} else {
				dict.AddImage(nil)
			}
		} else {
			dict.AddImage(SDNEWSYMS[i-s.SDNUMINSYMS])
		}
		exported++
	}
	return dict, nil
}
