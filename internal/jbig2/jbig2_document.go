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
//   - Charges every bitmap this file sizes from stream-controlled values against the decode's cumulative pixel
//     budget (embedded.go) before allocating: the page bitmap, a striped page's growth, and the refinement region's
//     page sub-image. The procs charge their own bitmaps and are handed the same budget here.
//   - Pruned the organizations outside the PDF embedded profile: the grouped-segment decode, the random-access flag
//     (a sequential stream may only refer backwards, which is now unconditional), and the endianness flag.
//   - Restored PDFium's checked arithmetic on the next-segment offset. PDFium accumulates it through FX_SAFE_UINT32
//     and fails when the sum is invalid; this translation added it in int64 and then truncated to uint32, so a
//     segment declaring a data length near 2^32 wrapped the offset backwards and the segment loop re-parsed the same
//     header forever — a hang on a few bytes of hostile input, found by fuzzing.
//   - Dropped an ineffectual index advance in the symbol dictionary's custom Huffman table selection.
//   - Restored PDFium's cap on SDNUMEXSYMS and SDNUMNEWSYMS at 65535 (jbig2_context.cpp:440-441); this translation
//     read both as raw integers, so a declared new-symbol count near 2^32 sized make([]*Image, SDNUMNEWSYMS) and the
//     IAID arithmetic context table before any symbol data was read.
//   - Restored PDFium's bound on a direct text region's SBNUMINSTANCES at 32 instances per remaining stream byte
//     (jbig2_context.cpp:662-675); each instance must be coded, so a count past what the stream can hold otherwise
//     left the text-region instance loop to spin.
//   - Restored PDFium's per-region IsValidImageSize check (0 < w,h <= 65535; jbig2_image.cpp:127-129, applied at
//     jbig2_context.cpp:631/933/1116) once in ParseRegionInfo, covering the text, halftone, generic, and refinement
//     regions this translation dropped it from; a value >= 2^31 also reads back negative here and is rejected.
//   - Restored PDFium's IsValidImageSize check on the halftone grid dimensions HGW/HGH (jbig2_context.cpp:933), the
//     pattern grid the halftone MMR path sizes its row buffers from.
//
// Derived from PDFium's core/fxcodec/jbig2 (Copyright 2014 The PDFium Authors; original code copyright 2014 Foxit
// Software Inc.), BSD-3-Clause; see LICENSE-pdfium in this directory.

package jbig2

// Result 解析结果
type Result int

const (
	// ResultSuccess 成功
	ResultSuccess Result = 0
	// ResultFailure 失败
	ResultFailure Result = 1
	// ResultEndReached 到达终点
	ResultEndReached Result = 2
	// ResultDecodeToBeContinued 继续解码
	ResultDecodeToBeContinued Result = 3
	// ResultPageCompleted 页面完成
	ResultPageCompleted Result = 4
)

// Document 文档上下文
type Document struct {
	stream        *BitStream
	globalContext *Document
	segmentList   []*Segment
	page          *Image
	pageInfoList  []*PageInfo
	segment       *Segment
	budget        *budget
	offset        uint32
	inPage        bool
	bufSpecified  bool
}

// GetSegments 获取段列表
// 返回: []*Segment 段列表
func (d *Document) GetSegments() []*Segment {
	return d.segmentList
}

// GetGlobalContext 获取全局上下文
// 返回: *Document 全局上下文
func (d *Document) GetGlobalContext() *Document {
	return d.globalContext
}

// PageInfo 页面信息
type PageInfo struct {
	Width             uint32
	Height            uint32
	ResolutionX       uint32
	ResolutionY       uint32
	DefaultPixelValue bool
	IsStriped         bool
	MaxStripeSize     uint16
}

// NewDocument 创建文档对象
// 入参: data 数据流, globalData 全局数据
// 返回: *Document 文档对象
func NewDocument(data []byte, globalData []byte) *Document {
	doc := &Document{
		stream: NewBitStream(data, 0),
	}
	if len(globalData) > 0 {
		doc.globalContext = &Document{
			stream: NewBitStream(globalData, 0),
		}
	}
	return doc
}

// ParseSegmentHeader 解析段头
// 入参: segment 段对象
// 返回: Result 结果
func (d *Document) ParseSegmentHeader(segment *Segment) Result {
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		segment.Number = val
	}
	var flags byte
	if val, err := d.stream.Read1Byte(); err != nil {
		return ResultFailure
	} else {
		flags = val
	}
	segment.Flags.Type = flags & 0x3F
	segment.Flags.PageAssociationSize = (flags & 0x40) != 0
	segment.Flags.DeferredNonRetain = (flags & 0x80) != 0
	cTemp := d.stream.GetCurByte()
	if (cTemp >> 5) == 7 {
		var count uint32
		if val, err := d.stream.ReadInteger(); err != nil {
			return ResultFailure
		} else {
			count = val
		}
		count &= 0x1FFFFFFF
		segment.ReferredToSegmentCount = int32(count)
		if segment.ReferredToSegmentCount > 1024 {
			return ResultFailure
		}
		retentionBits := segment.ReferredToSegmentCount + 1
		bytesToSkip := (retentionBits + 7) / 8
		d.stream.AddOffset(uint32(bytesToSkip))
	} else {
		if val, err := d.stream.Read1Byte(); err != nil {
			return ResultFailure
		} else {
			cTemp = val
		}
		segment.ReferredToSegmentCount = int32(cTemp >> 5)
	}
	cSSize := 1
	if segment.Number > 65536 {
		cSSize = 4
	} else if segment.Number > 256 {
		cSSize = 2
	}
	cPSize := 1
	if segment.Flags.PageAssociationSize {
		cPSize = 4
	}
	if segment.ReferredToSegmentCount > 0 {
		segment.ReferredToSegmentNumbers = make([]uint32, segment.ReferredToSegmentCount)
		for i := int32(0); i < segment.ReferredToSegmentCount; i++ {
			switch cSSize {
			case 1:
				if val, err := d.stream.Read1Byte(); err != nil {
					return ResultFailure
				} else {
					segment.ReferredToSegmentNumbers[i] = uint32(val)
				}
			case 2:
				if val, err := d.stream.ReadShortInteger(); err != nil {
					return ResultFailure
				} else {
					segment.ReferredToSegmentNumbers[i] = uint32(val)
				}
			case 4:
				if val, err := d.stream.ReadInteger(); err != nil {
					return ResultFailure
				} else {
					segment.ReferredToSegmentNumbers[i] = val
				}
			}
			if segment.ReferredToSegmentNumbers[i] >= segment.Number {
				return ResultFailure
			}
		}
	}
	if cPSize == 1 {
		if val, err := d.stream.Read1Byte(); err != nil {
			return ResultFailure
		} else {
			segment.PageAssociation = uint32(val)
		}
	} else {
		if val, err := d.stream.ReadInteger(); err != nil {
			return ResultFailure
		} else {
			segment.PageAssociation = val
		}
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		segment.DataLength = val
	}
	segment.Key = d.stream.GetKey()
	segment.DataOffset = d.stream.GetOffset()
	segment.State = JBig2SegmentDataUnparsed
	return ResultSuccess
}

// FindSegmentByNumber 查找段
// 入参: number 段编号
// 返回: *Segment 段对象
func (d *Document) FindSegmentByNumber(number uint32) *Segment {
	if d.globalContext != nil {
		if seg := d.globalContext.FindSegmentByNumber(number); seg != nil {
			return seg
		}
	}
	for _, seg := range d.segmentList {
		if seg.Number == number {
			return seg
		}
	}
	return nil
}

// ParseSegmentData 解析段数据
// 入参: segment 段对象
// 返回: Result 结果
func (d *Document) ParseSegmentData(segment *Segment) Result {
	switch segment.Flags.Type {
	case 0:
		return d.parseSymbolDict(segment)
	case 4, 6, 7:
		if !d.inPage {
			return ResultFailure
		}
		return d.parseTextRegion(segment)
	case 16:
		return d.parsePatternDict(segment)
	case 20, 22, 23:
		if !d.inPage {
			return ResultFailure
		}
		return d.parseHalftoneRegion(segment)
	case 36, 38, 39:
		if !d.inPage {
			return ResultFailure
		}
		return d.parseGenericRegion(segment)
	case 40, 42, 43:
		if !d.inPage {
			return ResultFailure
		}
		return d.parseGenericRefinementRegion(segment)
	case 48:
		return d.parsePageInfo(segment)
	case 49:
		d.inPage = false
		return ResultPageCompleted
	case 50:
		d.stream.AddOffset(segment.DataLength)
	case 51:
		return ResultEndReached
	case 52:
		d.stream.AddOffset(segment.DataLength)
	case 53:
		return d.parseTable(segment)
	case 62:
		d.stream.AddOffset(segment.DataLength)
	default:
		d.stream.AddOffset(segment.DataLength)
	}
	return ResultSuccess
}

// DecodeSequential 顺序解码
// 返回: Result 结果
func (d *Document) DecodeSequential() Result {
	if d.stream.GetByteLeft() <= 0 {
		return ResultEndReached
	}
	for d.stream.GetByteLeft() > 0 {
		if d.segment == nil {
			d.segment = NewSegment()
			ret := d.ParseSegmentHeader(d.segment)
			if ret != ResultSuccess {
				d.segment = nil
				return ret
			}
			d.offset = d.stream.GetOffset()
		}
		ret := d.ParseSegmentData(d.segment)
		if ret == ResultEndReached {
			d.segmentList = append(d.segmentList, d.segment)
			d.segment = nil
			return ResultEndReached
		}
		if ret == ResultPageCompleted {
			d.segmentList = append(d.segmentList, d.segment)
			d.segment = nil
			return ResultPageCompleted
		}
		if ret != ResultSuccess {
			d.segment = nil
			return ret
		}
		if d.segment.DataLength != 0xFFFFFFFF {
			// 段尾偏移必须在 uint32 内；截断回绕会使偏移倒退并陷入死循环
			newOffset := uint64(d.offset) + uint64(d.segment.DataLength)
			if newOffset > 0xFFFFFFFF {
				d.segment = nil
				return ResultFailure
			}
			d.stream.SetOffset(uint32(newOffset))
		} else {
			d.stream.AddOffset(4)
		}
		d.segmentList = append(d.segmentList, d.segment)
		d.segment = nil
	}
	return ResultSuccess
}

// parseSymbolDict 解析符号字典段
// 入参: segment 段对象
// 返回: Result 解析结果
func (d *Document) parseSymbolDict(segment *Segment) Result {
	var flags uint16
	if val, err := d.stream.ReadShortInteger(); err != nil {
		return ResultFailure
	} else {
		flags = val
	}
	sdd := NewSDDProc()
	sdd.budget = d.budget
	sdd.SDHUFF = (flags & 0x0001) != 0
	sdd.SDREFAGG = (flags & 0x0002) != 0
	sdd.SDTEMPLATE = uint8((flags >> 10) & 0x0003)
	sdd.SDRTEMPLATE = (flags & 0x1000) != 0
	if !sdd.SDHUFF {
		dwTemp := 2
		if sdd.SDTEMPLATE == 0 {
			dwTemp = 8
		}
		for i := 0; i < dwTemp; i++ {
			if val, err := d.stream.Read1Byte(); err != nil {
				return ResultFailure
			} else {
				sdd.SDAT[i] = int8(val)
			}
		}
	}
	if sdd.SDREFAGG && !sdd.SDRTEMPLATE {
		for i := 0; i < 4; i++ {
			if val, err := d.stream.Read1Byte(); err != nil {
				return ResultFailure
			} else {
				sdd.SDRAT[i] = int8(val)
			}
		}
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		sdd.SDNUMEXSYMS = val
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		sdd.SDNUMNEWSYMS = val
	}
	// PDFium rejects an export or new-symbol count over 65535 (jbig2_context.cpp:440-441) before either sizes an
	// allocation. Both are raw integers here, so without this a hostile count sizes SDNEWSYMS and the IAID context
	// table before any data is read.
	if sdd.SDNUMEXSYMS > JBig2MaxExportSymbols || sdd.SDNUMNEWSYMS > JBig2MaxNewSymbols {
		return ResultFailure
	}
	var inputSymbols []*Image
	if segment.ReferredToSegmentCount > 0 {
		for _, refNum := range segment.ReferredToSegmentNumbers {
			seg := d.FindSegmentByNumber(refNum)
			if seg == nil {
				return ResultFailure
			}
			if seg.Flags.Type == 0 && seg.SymbolDict != nil {
				inputSymbols = append(inputSymbols, seg.SymbolDict.Images...)
			}
		}
	}
	sdd.SDINSYMS = inputSymbols
	sdd.SDNUMINSYMS = uint32(len(inputSymbols))
	if sdd.SDHUFF {
		cSDHUFFDH := (flags >> 2) & 0x0003
		cSDHUFFDW := (flags >> 4) & 0x0003
		cSDHUFFBMSIZE := (flags >> 6) & 0x0001
		cSDHUFFAGGINST := (flags >> 7) & 0x0001
		if cSDHUFFDH == 2 || cSDHUFFDW == 2 {
			return ResultFailure
		}
		var tableSegments []*Segment
		for _, refNum := range segment.ReferredToSegmentNumbers {
			seg := d.FindSegmentByNumber(refNum)
			if seg != nil && seg.Flags.Type == 53 {
				tableSegments = append(tableSegments, seg)
			}
		}
		tableIdx := 0
		if cSDHUFFDH == 0 {
			sdd.SDHUFFDH = NewStandardTable(4)
		} else if cSDHUFFDH == 1 {
			sdd.SDHUFFDH = NewStandardTable(5)
		} else {
			if tableIdx < len(tableSegments) {
				sdd.SDHUFFDH = tableSegments[tableIdx].HuffmanTable
				tableIdx++
			} else {
				return ResultFailure
			}
		}
		if cSDHUFFDW == 0 {
			sdd.SDHUFFDW = NewStandardTable(2)
		} else if cSDHUFFDW == 1 {
			sdd.SDHUFFDW = NewStandardTable(3)
		} else {
			if tableIdx < len(tableSegments) {
				sdd.SDHUFFDW = tableSegments[tableIdx].HuffmanTable
				tableIdx++
			} else {
				return ResultFailure
			}
		}
		if cSDHUFFBMSIZE == 0 {
			sdd.SDHUFFBMSIZE = NewStandardTable(1)
		} else {
			if tableIdx < len(tableSegments) {
				sdd.SDHUFFBMSIZE = tableSegments[tableIdx].HuffmanTable
				tableIdx++
			} else {
				return ResultFailure
			}
		}
		if sdd.SDREFAGG {
			if cSDHUFFAGGINST == 0 {
				sdd.SDHUFFAGGINST = NewStandardTable(1)
			} else {
				// SDHUFFAGGINST 是本段最后一张自定义表，索引不再推进
				if tableIdx < len(tableSegments) {
					sdd.SDHUFFAGGINST = tableSegments[tableIdx].HuffmanTable
				} else {
					return ResultFailure
				}
			}
		}
	}
	gbContextSize := 0
	grContextSize := 0
	if !sdd.SDHUFF {
		if sdd.SDTEMPLATE == 0 {
			gbContextSize = 65536
		} else {
			gbContextSize = 8192
		}
	}
	if sdd.SDREFAGG {
		if sdd.SDRTEMPLATE {
			grContextSize = 1024
		} else {
			grContextSize = 8192
		}
	}
	var gbContexts, grContexts []ArithCtx
	contextUsed := (flags & 0x0100) != 0
	if contextUsed {
		for i := len(segment.ReferredToSegmentNumbers) - 1; i >= 0; i-- {
			refSeg := d.FindSegmentByNumber(segment.ReferredToSegmentNumbers[i])
			if refSeg == nil || refSeg.Flags.Type != 0 {
				continue
			}
			if len(refSeg.GBContexts) == gbContextSize {
				gbContexts = append([]ArithCtx(nil), refSeg.GBContexts...)
			}
			if len(refSeg.GRContexts) == grContextSize {
				grContexts = append([]ArithCtx(nil), refSeg.GRContexts...)
			}
			break
		}
	}
	if gbContexts == nil {
		gbContexts = make([]ArithCtx, gbContextSize)
	}
	if grContexts == nil {
		grContexts = make([]ArithCtx, grContextSize)
	}
	var err error
	if sdd.SDHUFF {
		segment.SymbolDict, err = sdd.DecodeHuffman(d.stream, gbContexts, grContexts)
		d.stream.AlignByte()
	} else {
		arithDecoder := NewArithDecoder(d.stream)
		segment.SymbolDict, err = sdd.DecodeArith(arithDecoder, gbContexts, grContexts)
		d.stream.AlignByte()
		d.stream.AddOffset(2)
	}
	if err != nil {
		return ResultFailure
	}
	if (flags & 0x0200) != 0 {
		segment.GBContexts = gbContexts
		segment.GRContexts = grContexts
	}
	segment.ResultType = JBig2SymbolDictPointer
	return ResultSuccess
}

// ParseRegionInfo 解析区域信息
// 入参: ri 区域信息
// 返回: Result 结果
func (d *Document) ParseRegionInfo(ri *RegionInfo) Result {
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		ri.Width = int32(val)
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		ri.Height = int32(val)
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		ri.X = int32(val)
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		ri.Y = int32(val)
	}
	if val, err := d.stream.Read1Byte(); err != nil {
		return ResultFailure
	} else {
		ri.Flags = val
	}
	// PDFium validates every region rectangle with IsValidImageSize (0 < w,h <= 65535; jbig2_image.cpp:127-129) at
	// each region site (jbig2_context.cpp:631/933/1116); doing it once here covers the text, halftone, generic, and
	// refinement paths. Width/Height are read from raw integers into int32, so a value >= 2^31 reads back negative and
	// is caught by the <= 0 test.
	if ri.Width <= 0 || ri.Height <= 0 || ri.Width > JBig2MaxImageSize || ri.Height > JBig2MaxImageSize {
		return ResultFailure
	}
	return ResultSuccess
}

// composeOpFromRegionFlags 从区域标志获取组合操作
// 入参: flags 区域标志
// 返回: ComposeOp 组合操作
func composeOpFromRegionFlags(flags uint8) ComposeOp {
	if (flags & 0x07) == 4 {
		return ComposeReplace
	}
	return ComposeOp(flags & 0x03)
}

// expandPageForRegion 根据区域扩展页面
// 入参: ri 区域信息
// 返回: Result 结果
func (d *Document) expandPageForRegion(ri *RegionInfo) Result {
	if d.bufSpecified || d.page == nil || len(d.pageInfoList) == 0 {
		return ResultSuccess
	}
	pi := d.pageInfoList[len(d.pageInfoList)-1]
	if !pi.IsStriped {
		return ResultSuccess
	}
	newHeight := int64(ri.Y) + int64(ri.Height)
	if newHeight <= int64(d.page.Height()) || newHeight > int64(1<<31-1) {
		return ResultSuccess
	}
	if d.budget.charge(int64(d.page.Width()), newHeight-int64(d.page.Height())) != nil {
		return ResultFailure
	}
	d.page.Expand(int32(newHeight), pi.DefaultPixelValue)
	return ResultSuccess
}

// GetHuffmanTable 获取霍夫曼表
// 入参: idx 索引
// 返回: *HuffmanTable 霍夫曼表
func (d *Document) GetHuffmanTable(idx int) *HuffmanTable {
	return NewStandardTable(idx)
}

// DecodeSymbolIDHuffmanTable 解码符号ID霍夫曼表
// 入参: SBNUMSYMS 符号数
// 返回: []HuffmanCode 霍夫曼编码切片
func (d *Document) DecodeSymbolIDHuffmanTable(SBNUMSYMS uint32) []HuffmanCode {
	kRunCodesSize := 35
	huffmanCodes := make([]HuffmanCode, kRunCodesSize)
	for i := 0; i < kRunCodesSize; i++ {
		val, err := d.stream.ReadNBits(4)
		if err != nil {
			return nil
		}
		huffmanCodes[i].Codelen = int32(val)
	}
	if err := HuffmanAssignCode(huffmanCodes); err != nil {
		return nil
	}
	runDecoder, err := newHuffmanCodeIndex(huffmanCodes)
	if err != nil {
		return nil
	}
	SBSYMCODES := make([]HuffmanCode, SBNUMSYMS)
	i := int32(0)
	for i < int32(SBNUMSYMS) {
		j, err := runDecoder.Decode(d.stream)
		if err != nil {
			return nil
		}
		runcode := int32(j)
		var run int32
		if runcode < 32 {
			SBSYMCODES[i].Codelen = runcode
			run = 0
		} else if runcode == 32 {
			val, err := d.stream.ReadNBits(2)
			if err != nil {
				return nil
			}
			run = int32(val) + 3
		} else if runcode == 33 {
			val, err := d.stream.ReadNBits(3)
			if err != nil {
				return nil
			}
			run = int32(val) + 3
		} else if runcode == 34 {
			val, err := d.stream.ReadNBits(7)
			if err != nil {
				return nil
			}
			run = int32(val) + 11
		}
		if run > 0 {
			if i+run > int32(SBNUMSYMS) {
				return nil
			}
			for k := int32(0); k < run; k++ {
				if runcode == 32 && i > 0 {
					SBSYMCODES[i+k].Codelen = SBSYMCODES[i-1].Codelen
				} else {
					SBSYMCODES[i+k].Codelen = 0
				}
			}
			i += run
		} else {
			i++
		}
	}
	if err := HuffmanAssignCode(SBSYMCODES); err != nil {
		return nil
	}
	return SBSYMCODES
}

// parseTextRegion 解析文本区域段
// 入参: segment 段对象
// 返回: Result 解析结果
func (d *Document) parseTextRegion(segment *Segment) Result {
	var ri RegionInfo
	if d.ParseRegionInfo(&ri) != ResultSuccess {
		return ResultFailure
	}
	var flags uint16
	if val, err := d.stream.ReadShortInteger(); err != nil {
		return ResultFailure
	} else {
		flags = val
	}
	pTRD := NewTRDProc()
	pTRD.budget = d.budget
	pTRD.SBW = uint32(ri.Width)
	pTRD.SBH = uint32(ri.Height)
	pTRD.SBHUFF = (flags & 0x0001) != 0
	pTRD.SBREFINE = ((flags >> 1) & 0x0001) != 0
	dwTemp := (flags >> 2) & 0x0003
	pTRD.SBSTRIPS = 1 << dwTemp
	pTRD.REFCORNER = JBig2Corner((flags >> 4) & 0x0003)
	pTRD.TRANSPOSED = ((flags >> 6) & 0x0001) != 0
	pTRD.SBCOMBOP = ComposeOp((flags >> 7) & 0x0003)
	pTRD.SBDEFPIXEL = ((flags >> 9) & 0x0001) != 0
	pTRD.SBDSOFFSET = int8((flags >> 10) & 0x001F)
	if pTRD.SBDSOFFSET >= 0x10 {
		pTRD.SBDSOFFSET = pTRD.SBDSOFFSET - 0x20
	}
	pTRD.SBRTEMPLATE = ((flags >> 15) & 0x0001) != 0
	var huffFlags uint16
	if pTRD.SBHUFF {
		if val, err := d.stream.ReadShortInteger(); err != nil {
			return ResultFailure
		} else {
			huffFlags = val
		}
	}
	if pTRD.SBREFINE && !pTRD.SBRTEMPLATE {
		for i := 0; i < 4; i++ {
			if val, err := d.stream.Read1Byte(); err != nil {
				return ResultFailure
			} else {
				pTRD.SBRAT[i] = int8(val)
			}
		}
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		pTRD.SBNUMINSTANCES = val
	}
	// PDFium bounds SBNUMINSTANCES by 32 instances per remaining stream byte (jbig2_context.cpp:662-675): each
	// instance needs bits to code, so a count past what the stream can hold is malformed. This translation dropped the
	// check, leaving the instance loop (jbig2_trd_proc.go) to spin on a hostile count.
	if uint64(pTRD.SBNUMINSTANCES) > uint64(d.stream.GetByteLeft())*32 {
		return ResultFailure
	}
	if segment.ReferredToSegmentCount > 0 {
		for _, refNum := range segment.ReferredToSegmentNumbers {
			if d.FindSegmentByNumber(refNum) == nil {
				return ResultFailure
			}
		}
	}
	dwNumSyms := uint32(0)
	var singleDict *SymbolDict
	dictCount := 0
	for _, refNum := range segment.ReferredToSegmentNumbers {
		seg := d.FindSegmentByNumber(refNum)
		if seg != nil && seg.Flags.Type == 0 && seg.SymbolDict != nil {
			dwNumSyms += uint32(seg.SymbolDict.NumImages())
			singleDict = seg.SymbolDict
			dictCount++
		}
	}
	pTRD.SBNUMSYMS = dwNumSyms
	if dictCount == 1 {
		pTRD.SBSYMS = singleDict.Images
	} else {
		SBSYMS := make([]*Image, pTRD.SBNUMSYMS)
		dwNumSyms = 0
		for _, refNum := range segment.ReferredToSegmentNumbers {
			seg := d.FindSegmentByNumber(refNum)
			if seg != nil && seg.Flags.Type == 0 && seg.SymbolDict != nil {
				dict := seg.SymbolDict
				for j := 0; j < dict.NumImages(); j++ {
					SBSYMS[dwNumSyms+uint32(j)] = dict.GetImage(j)
				}
				dwNumSyms += uint32(dict.NumImages())
			}
		}
		pTRD.SBSYMS = SBSYMS
	}
	if pTRD.SBHUFF {
		if encodedTable := d.DecodeSymbolIDHuffmanTable(pTRD.SBNUMSYMS); encodedTable != nil {
			d.stream.AlignByte()
			pTRD.SBSYMCODES = encodedTable
		} else {
			return ResultFailure
		}
	} else {
		pTRD.SBSYMCODELEN = ceilLog2(pTRD.SBNUMSYMS)
	}
	if pTRD.SBHUFF {
		cSBHUFFFS := huffFlags & 0x0003
		cSBHUFFDS := (huffFlags >> 2) & 0x0003
		cSBHUFFDT := (huffFlags >> 4) & 0x0003
		cSBHUFFRDW := (huffFlags >> 6) & 0x0003
		cSBHUFFRDH := (huffFlags >> 8) & 0x0003
		cSBHUFFRDX := (huffFlags >> 10) & 0x0003
		cSBHUFFRDY := (huffFlags >> 12) & 0x0003
		cSBHUFFRSIZE := (huffFlags >> 14) & 0x0001
		if !pTRD.SBREFINE {
			cSBHUFFRDW = 0
			cSBHUFFRDH = 0
			cSBHUFFRDX = 0
			cSBHUFFRDY = 0
			cSBHUFFRSIZE = 0
		}
		if cSBHUFFFS == 2 || cSBHUFFRDW == 2 || cSBHUFFRDH == 2 || cSBHUFFRDX == 2 || cSBHUFFRDY == 2 {
			return ResultFailure
		}
		tableIdx := 0
		var tableSegments []*Segment
		for _, refNum := range segment.ReferredToSegmentNumbers {
			seg := d.FindSegmentByNumber(refNum)
			if seg != nil && seg.Flags.Type == 53 {
				tableSegments = append(tableSegments, seg)
			}
		}
		getUserTable := func() *HuffmanTable {
			if tableIdx < len(tableSegments) {
				t := tableSegments[tableIdx].HuffmanTable
				tableIdx++
				return t
			}
			return nil
		}
		if cSBHUFFFS == 0 {
			pTRD.SBHUFFFS = d.GetHuffmanTable(6)
		} else if cSBHUFFFS == 1 {
			pTRD.SBHUFFFS = d.GetHuffmanTable(7)
		} else {
			pTRD.SBHUFFFS = getUserTable()
		}
		if cSBHUFFDS == 0 {
			pTRD.SBHUFFDS = d.GetHuffmanTable(8)
		} else if cSBHUFFDS == 1 {
			pTRD.SBHUFFDS = d.GetHuffmanTable(9)
		} else if cSBHUFFDS == 2 {
			pTRD.SBHUFFDS = d.GetHuffmanTable(10)
		} else {
			pTRD.SBHUFFDS = getUserTable()
		}
		if cSBHUFFDT == 0 {
			pTRD.SBHUFFDT = d.GetHuffmanTable(11)
		} else if cSBHUFFDT == 1 {
			pTRD.SBHUFFDT = d.GetHuffmanTable(12)
		} else if cSBHUFFDT == 2 {
			pTRD.SBHUFFDT = d.GetHuffmanTable(13)
		} else {
			pTRD.SBHUFFDT = getUserTable()
		}
		if cSBHUFFRDW == 0 {
			pTRD.SBHUFFRDW = d.GetHuffmanTable(14)
		} else if cSBHUFFRDW == 1 {
			pTRD.SBHUFFRDW = d.GetHuffmanTable(15)
		} else {
			pTRD.SBHUFFRDW = getUserTable()
		}
		if cSBHUFFRDH == 0 {
			pTRD.SBHUFFRDH = d.GetHuffmanTable(14)
		} else if cSBHUFFRDH == 1 {
			pTRD.SBHUFFRDH = d.GetHuffmanTable(15)
		} else {
			pTRD.SBHUFFRDH = getUserTable()
		}
		if cSBHUFFRDX == 0 {
			pTRD.SBHUFFRDX = d.GetHuffmanTable(14)
		} else if cSBHUFFRDX == 1 {
			pTRD.SBHUFFRDX = d.GetHuffmanTable(15)
		} else {
			pTRD.SBHUFFRDX = getUserTable()
		}
		if cSBHUFFRDY == 0 {
			pTRD.SBHUFFRDY = d.GetHuffmanTable(14)
		} else if cSBHUFFRDY == 1 {
			pTRD.SBHUFFRDY = d.GetHuffmanTable(15)
		} else {
			pTRD.SBHUFFRDY = getUserTable()
		}
		if cSBHUFFRSIZE == 0 {
			pTRD.SBHUFFRSIZE = d.GetHuffmanTable(1)
		} else {
			pTRD.SBHUFFRSIZE = getUserTable()
		}
	}
	var grContexts []ArithCtx
	if pTRD.SBREFINE {
		size := 8192
		if pTRD.SBRTEMPLATE {
			size = 1024
		}
		grContexts = make([]ArithCtx, size)
	}
	segment.ResultType = JBig2ImagePointer
	var err error
	if pTRD.SBHUFF {
		var img *Image
		img, err = pTRD.DecodeHuffman(d.stream, grContexts)
		if err == nil {
			segment.Image = img
			d.stream.AlignByte()
		}
	} else {
		arithDecoder := NewArithDecoder(d.stream)
		var img *Image
		img, err = pTRD.DecodeArith(arithDecoder, grContexts, nil)
		if err == nil {
			segment.Image = img
			d.stream.AlignByte()
			d.stream.AddOffset(2)
		}
	}
	if err != nil || segment.Image == nil {
		return ResultFailure
	}
	if segment.Flags.Type != 4 {
		if d.expandPageForRegion(&ri) != ResultSuccess {
			return ResultFailure
		}
		d.page.ComposeFrom(ri.X, ri.Y, segment.Image, composeOpFromRegionFlags(ri.Flags))
		segment.Image = nil
	}
	return ResultSuccess
}

// parsePatternDict 解析模式字典段
// 入参: segment 段对象
// 返回: Result 解析结果
func (d *Document) parsePatternDict(segment *Segment) Result {
	var flags byte
	pPDD := NewPDDProc()
	pPDD.budget = d.budget
	if val, err := d.stream.Read1Byte(); err != nil {
		return ResultFailure
	} else {
		flags = val
	}
	if val, err := d.stream.Read1Byte(); err != nil {
		return ResultFailure
	} else {
		pPDD.HDPW = val
	}
	if val, err := d.stream.Read1Byte(); err != nil {
		return ResultFailure
	} else {
		pPDD.HDPH = val
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		pPDD.GRAYMAX = val
	}
	if pPDD.GRAYMAX > JBig2MaxPatternIndex {
		return ResultFailure
	}
	pPDD.HDMMR = (flags & 0x01) != 0
	pPDD.HDTEMPLATE = (flags >> 1) & 0x03
	segment.ResultType = JBig2PatternDictPointer
	var err error
	if pPDD.HDMMR {
		segment.PatternDict, err = pPDD.DecodeMMR(d.stream)
		if err != nil {
			return ResultFailure
		}
		d.stream.AlignByte()
	} else {
		size := 1024
		if pPDD.HDTEMPLATE == 0 {
			size = 65536
		} else if pPDD.HDTEMPLATE == 1 {
			size = 8192
		}
		gbContexts := make([]ArithCtx, size)
		arithDecoder := NewArithDecoder(d.stream)
		segment.PatternDict, err = pPDD.DecodeArith(arithDecoder, gbContexts)
		if err != nil {
			return ResultFailure
		}
		d.stream.AlignByte()
		d.stream.AddOffset(2)
	}
	return ResultSuccess
}

// parseHalftoneRegion 解析半色调区域段
// 入参: segment 段对象
// 返回: Result 解析结果
func (d *Document) parseHalftoneRegion(segment *Segment) Result {
	var ri RegionInfo
	var flags byte
	pHRD := NewHTRDProc()
	pHRD.budget = d.budget
	if d.ParseRegionInfo(&ri) != ResultSuccess {
		return ResultFailure
	}
	if val, err := d.stream.Read1Byte(); err != nil {
		return ResultFailure
	} else {
		flags = val
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		pHRD.HGW = val
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		pHRD.HGH = val
	}
	// PDFium validates the halftone grid dimensions with IsValidImageSize (jbig2_context.cpp:933); the grid is the
	// pattern grid, separate from the region rectangle. HGW is what lets the MMR path size make([]int, HGW+5), so an
	// unbounded value here reaches that allocation.
	if pHRD.HGW == 0 || pHRD.HGW > JBig2MaxImageSize || pHRD.HGH == 0 || pHRD.HGH > JBig2MaxImageSize {
		return ResultFailure
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		pHRD.HGX = int32(val)
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		pHRD.HGY = int32(val)
	}
	if val, err := d.stream.ReadShortInteger(); err != nil {
		return ResultFailure
	} else {
		pHRD.HRX = uint16(val)
	}
	if val, err := d.stream.ReadShortInteger(); err != nil {
		return ResultFailure
	} else {
		pHRD.HRY = uint16(val)
	}
	pHRD.HBW = uint32(ri.Width)
	pHRD.HBH = uint32(ri.Height)
	pHRD.HMMR = (flags & 0x01) != 0
	pHRD.HTEMPLATE = (flags >> 1) & 0x03
	pHRD.HENABLESKIP = ((flags >> 3) & 0x01) != 0
	pHRD.HCOMBOP = ComposeOp((flags >> 4) & 0x07)
	pHRD.HDEFPIXEL = ((flags >> 7) & 0x01) != 0
	if segment.ReferredToSegmentCount != 1 {
		return ResultFailure
	}
	seg := d.FindSegmentByNumber(segment.ReferredToSegmentNumbers[0])
	if seg == nil || seg.Flags.Type != 16 || seg.PatternDict == nil {
		return ResultFailure
	}
	pPatternDict := seg.PatternDict
	if pPatternDict.NUMPATS == 0 {
		return ResultFailure
	}
	pHRD.HNUMPATS = pPatternDict.NUMPATS
	pHRD.HPATS = pPatternDict.HDPATS
	pHRD.HPW = uint8(pPatternDict.HDPATS[0].Width())
	pHRD.HPH = uint8(pPatternDict.HDPATS[0].Height())
	segment.ResultType = JBig2ImagePointer
	var err error
	if pHRD.HMMR {
		d.stream.AlignByte()
		segment.Image, err = pHRD.DecodeMMR(d.stream)
		if err != nil {
			return ResultFailure
		}
		d.stream.AlignByte()
	} else {
		size := GetHuffContextSize(pHRD.HTEMPLATE)
		gbContexts := make([]ArithCtx, size)
		arithDecoder := NewArithDecoder(d.stream)
		segment.Image, err = pHRD.DecodeArith(arithDecoder, gbContexts)
		if err != nil {
			return ResultFailure
		}
		d.stream.AlignByte()
		d.stream.AddOffset(2)
	}
	if segment.Flags.Type != 20 {
		if d.expandPageForRegion(&ri) != ResultSuccess {
			return ResultFailure
		}
		d.page.ComposeFrom(ri.X, ri.Y, segment.Image, composeOpFromRegionFlags(ri.Flags))
		segment.Image = nil
	}
	return ResultSuccess
}

// parseGenericRegion 解析通用区域段
// 入参: segment 段对象
// 返回: Result 解析结果
func (d *Document) parseGenericRegion(segment *Segment) Result {
	var ri RegionInfo
	var flags byte
	if d.ParseRegionInfo(&ri) != ResultSuccess {
		return ResultFailure
	}
	if val, err := d.stream.Read1Byte(); err != nil {
		return ResultFailure
	} else {
		flags = val
	}
	pGRD := NewGRDProc()
	pGRD.budget = d.budget
	pGRD.GBW = uint32(ri.Width)
	pGRD.GBH = uint32(ri.Height)
	pGRD.MMR = (flags & 0x01) != 0
	pGRD.GBTEMPLATE = (flags >> 1) & 0x03
	pGRD.TPGDON = ((flags >> 3) & 0x01) != 0
	if !pGRD.MMR {
		if pGRD.GBTEMPLATE == 0 {
			for i := 0; i < 8; i++ {
				if val, err := d.stream.Read1Byte(); err != nil {
					return ResultFailure
				} else {
					pGRD.GBAT[i] = int8(val)
				}
			}
		} else {
			for i := 0; i < 2; i++ {
				if val, err := d.stream.Read1Byte(); err != nil {
					return ResultFailure
				} else {
					pGRD.GBAT[i] = int8(val)
				}
			}
		}
	}
	pGRD.USESKIP = false
	segment.ResultType = JBig2ImagePointer
	if pGRD.MMR {
		res := pGRD.StartDecodeMMR(&segment.Image, d.stream)
		if res != JBig2SegmentParseComplete {
			return ResultFailure
		}
		d.stream.AlignByte()
	} else {
		size := GetHuffContextSize(pGRD.GBTEMPLATE)
		gbContexts := make([]ArithCtx, size)
		arithDecoder := NewArithDecoder(d.stream)
		var err error
		segment.Image, err = pGRD.DecodeArith(arithDecoder, gbContexts)
		if err != nil {
			return ResultFailure
		}
		d.stream.AlignByte()
		d.stream.AddOffset(2)
	}
	if segment.Flags.Type != 36 {
		rect := pGRD.GetReplaceRect()
		if d.expandPageForRegion(&ri) != ResultSuccess {
			return ResultFailure
		}
		d.page.ComposeFrom(ri.X+rect.Left, ri.Y+rect.Top, segment.Image, composeOpFromRegionFlags(ri.Flags))
		segment.Image = nil
	}
	return ResultSuccess
}

// GetHuffContextSize 获取上下文大小
// 入参: template 模板号
// 返回: int 大小
func GetHuffContextSize(template byte) int {
	if template == 0 {
		return 65536
	} else if template == 1 {
		return 8192
	}
	return 1024
}

// parseGenericRefinementRegion 解析通用细化区域段
// 入参: segment 段对象
// 返回: Result 解析结果
func (d *Document) parseGenericRefinementRegion(segment *Segment) Result {
	var ri RegionInfo
	var flags byte
	if d.ParseRegionInfo(&ri) != ResultSuccess {
		return ResultFailure
	}
	if val, err := d.stream.Read1Byte(); err != nil {
		return ResultFailure
	} else {
		flags = val
	}
	pGRRD := NewGRRDProc()
	pGRRD.budget = d.budget
	pGRRD.GRW = uint32(ri.Width)
	pGRRD.GRH = uint32(ri.Height)
	pGRRD.GRTEMPLATE = (flags & 0x01) != 0
	pGRRD.TPGRON = ((flags >> 1) & 0x01) != 0
	if !pGRRD.GRTEMPLATE {
		for i := 0; i < 4; i++ {
			if val, err := d.stream.Read1Byte(); err != nil {
				return ResultFailure
			} else {
				pGRRD.GRAT[i] = int8(val)
			}
		}
	}
	var pageSubImage *Image
	if segment.ReferredToSegmentCount > 0 {
		var pSeg *Segment
		for _, refNum := range segment.ReferredToSegmentNumbers {
			pSeg = d.FindSegmentByNumber(refNum)
			if pSeg == nil {
				return ResultFailure
			}
			if pSeg.Flags.Type == 4 || pSeg.Flags.Type == 20 || pSeg.Flags.Type == 36 || pSeg.Flags.Type == 40 {
				break
			}
		}
		if pSeg != nil && pSeg.Image != nil {
			pGRRD.GRREFERENCE = pSeg.Image
		} else {
			return ResultFailure
		}
	} else {
		if d.budget.charge(int64(ri.Width), int64(ri.Height)) != nil {
			return ResultFailure
		}
		pageSubImage = d.page.SubImage(ri.X, ri.Y, ri.Width, ri.Height)
		pGRRD.GRREFERENCE = pageSubImage
	}
	pGRRD.GRREFERENCEDX = 0
	pGRRD.GRREFERENCEDY = 0
	size := 8192
	if pGRRD.GRTEMPLATE {
		size = 1024
	}
	grContexts := make([]ArithCtx, size)
	arithDecoder := NewArithDecoder(d.stream)
	segment.ResultType = JBig2ImagePointer
	var err error
	segment.Image, err = pGRRD.Decode(arithDecoder, grContexts)
	if err != nil {
		return ResultFailure
	}
	d.stream.AlignByte()
	d.stream.AddOffset(2)
	if segment.Flags.Type != 40 {
		if d.expandPageForRegion(&ri) != ResultSuccess {
			return ResultFailure
		}
		d.page.ComposeFrom(ri.X, ri.Y, segment.Image, composeOpFromRegionFlags(ri.Flags))
	}
	return ResultSuccess
}

// parsePageInfo 解析页面信息段
// 入参: segment 段对象
// 返回: Result 解析结果
func (d *Document) parsePageInfo(segment *Segment) Result {
	pi := &PageInfo{}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		pi.Width = val
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		pi.Height = val
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		pi.ResolutionX = val
	}
	if val, err := d.stream.ReadInteger(); err != nil {
		return ResultFailure
	} else {
		pi.ResolutionY = val
	}
	var flags byte
	if val, err := d.stream.Read1Byte(); err != nil {
		return ResultFailure
	} else {
		flags = val
	}
	var striping uint16
	if val, err := d.stream.ReadShortInteger(); err != nil {
		return ResultFailure
	} else {
		striping = val
	}
	pi.DefaultPixelValue = (flags & 4) != 0
	pi.IsStriped = (striping & 0x8000) != 0
	pi.MaxStripeSize = striping & 0x7FFF
	height := pi.Height
	if height == 0xFFFFFFFF {
		height = uint32(pi.MaxStripeSize)
	}
	if d.budget.charge(int64(pi.Width), int64(height)) != nil {
		return ResultFailure
	}
	d.page = NewImage(int32(pi.Width), int32(height))
	if d.page == nil {
		return ResultFailure
	}
	if pi.DefaultPixelValue {
		d.page.Fill(true)
	}
	d.pageInfoList = append(d.pageInfoList, pi)
	d.inPage = true
	return ResultSuccess
}

// parseTable 解析表段
// 入参: segment 段对象
// 返回: Result 解析结果
func (d *Document) parseTable(segment *Segment) Result {
	segment.ResultType = JBig2HuffmanTablePointer
	huff := NewTableFromStream(d.stream)
	if !huff.IsOK() {
		return ResultFailure
	}
	segment.HuffmanTable = huff
	d.stream.AlignByte()
	return ResultSuccess
}

// ReleasePageSegments 释放页面段数据
// 入参: pageNumber 页面编号
func (d *Document) ReleasePageSegments(pageNumber uint32) {
	n := 0
	for _, seg := range d.segmentList {
		if seg.PageAssociation != pageNumber {
			d.segmentList[n] = seg
			n++
		} else {
			seg.Image = nil
			seg.PatternDict = nil
			seg.SymbolDict = nil
			seg.HuffmanTable = nil
		}
	}
	for i := n; i < len(d.segmentList); i++ {
		d.segmentList[i] = nil
	}
	d.segmentList = d.segmentList[:n]
}
