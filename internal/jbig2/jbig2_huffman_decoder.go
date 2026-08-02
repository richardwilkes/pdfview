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

package jbig2

import "errors"

// maxHuffmanCodeLength 最大霍夫曼码长
const maxHuffmanCodeLength = 32

// TableLine 霍夫曼表行定义
type TableLine struct {
	PrefLen  int32
	RangeLen int32
	RangeLow int32
}

// HuffmanTable 霍夫曼表
type HuffmanTable struct {
	HTOOB    bool
	NTEMP    uint32
	CODES    []HuffmanCode
	RANGELEN []int32
	RANGELOW []int32
	Ok       bool
	decoder  *huffmanCodeIndex
}

// huffmanCodeIndex 规范霍夫曼解码索引
type huffmanCodeIndex struct {
	maxCodeLen   uint32
	firstCodes   [maxHuffmanCodeLength + 1]uint32
	codeCounts   [maxHuffmanCodeLength + 1]uint32
	firstIndexes [maxHuffmanCodeLength + 1]int
	indexes      []int
}

// NewStandardTable 从标准表创建霍夫曼表
// 入参: idx 表索引
// 返回: *HuffmanTable 霍夫曼表
func NewStandardTable(idx int) *HuffmanTable {
	ht := &HuffmanTable{}
	ht.parseFromStandardTable(idx)
	return ht
}

// NewTableFromStream 从流创建霍夫曼表
// 入参: stream 位流
// 返回: *HuffmanTable 霍夫曼表
func NewTableFromStream(stream *BitStream) *HuffmanTable {
	ht := &HuffmanTable{}
	ht.parseFromCodedBuffer(stream)
	return ht
}

// Size 获取霍夫曼表大小
// 返回: uint32 大小
func (h *HuffmanTable) Size() uint32 {
	return uint32(len(h.CODES))
}

// IsHTOOB 是否包含越界符
// 返回: bool 是否包含
func (h *HuffmanTable) IsHTOOB() bool {
	return h.HTOOB
}

// IsOK 霍夫曼表是否有效
// 返回: bool 是否有效
func (h *HuffmanTable) IsOK() bool {
	return h.Ok
}

// parseFromStandardTable 从标准表解析
// 入参: idx 表索引
// 返回: bool 是否成功
func (h *HuffmanTable) parseFromStandardTable(idx int) bool {
	if idx < 1 || idx >= len(kHuffmanTables) {
		return false
	}
	def := kHuffmanTables[idx]
	h.HTOOB = def.HTOOB
	h.NTEMP = uint32(len(def.Lines))
	h.CODES = make([]HuffmanCode, h.NTEMP)
	for i := 0; i < int(h.NTEMP); i++ {
		h.CODES[i].Codelen = def.Lines[i].PrefLen
		h.CODES[i].Val1 = def.Lines[i].RangeLen
		h.CODES[i].Val2 = def.Lines[i].RangeLow
	}
	return h.finalize()
}

// parseFromCodedBuffer 从编码位流解析
// 入参: stream 位流
// 返回: bool 是否成功
func (h *HuffmanTable) parseFromCodedBuffer(stream *BitStream) bool {
	flags, err := stream.Read1Byte()
	if err != nil {
		return false
	}
	h.HTOOB = (flags & 0x01) != 0
	prefixSizeBits := uint32((flags>>1)&0x07) + 1
	rangeSizeBits := uint32((flags>>4)&0x07) + 1
	val, err := stream.ReadInteger()
	if err != nil {
		return false
	}
	lowestValue := int32(val)
	val, err = stream.ReadInteger()
	if err != nil {
		return false
	}
	highestValue := int32(val)
	currentRangeLow := int64(lowestValue)
	for currentRangeLow < int64(highestValue) {
		prefixLength, err := stream.ReadNBits(prefixSizeBits)
		if err != nil {
			return false
		}
		rangeLength, err := stream.ReadNBits(rangeSizeBits)
		if err != nil {
			return false
		}
		if rangeLength > maxHuffmanCodeLength {
			return false
		}
		h.CODES = append(h.CODES, HuffmanCode{
			Codelen: int32(prefixLength),
			Val1:    int32(rangeLength),
			Val2:    int32(currentRangeLow),
		})
		currentRangeLow += int64(1) << rangeLength
	}
	prefixLength, err := stream.ReadNBits(prefixSizeBits)
	if err != nil {
		return false
	}
	h.CODES = append(h.CODES, HuffmanCode{
		Codelen:    int32(prefixLength),
		Val1:       32,
		Val2:       lowestValue - 1,
		LowerRange: true,
	})
	prefixLength, err = stream.ReadNBits(prefixSizeBits)
	if err != nil {
		return false
	}
	h.CODES = append(h.CODES, HuffmanCode{
		Codelen: int32(prefixLength),
		Val1:    32,
		Val2:    highestValue,
	})
	if h.HTOOB {
		prefixLength, err = stream.ReadNBits(prefixSizeBits)
		if err != nil {
			return false
		}
		h.CODES = append(h.CODES, HuffmanCode{
			Codelen: int32(prefixLength),
		})
	}
	h.NTEMP = uint32(len(h.CODES))
	return h.finalize()
}

// extendBuffers 扩展内部缓冲区
func (h *HuffmanTable) extendBuffers() {
	h.RANGELEN = make([]int32, len(h.CODES))
	h.RANGELOW = make([]int32, len(h.CODES))
	for i := range h.CODES {
		h.RANGELEN[i] = h.CODES[i].Val1
		h.RANGELOW[i] = h.CODES[i].Val2
	}
}

// finalize 完成霍夫曼表构建
// 返回: bool 是否成功
func (h *HuffmanTable) finalize() bool {
	h.extendBuffers()
	maxCodeLen, codeCounts, firstCodes, err := huffmanCodeLayout(h.CODES)
	if err != nil {
		h.Ok = false
		return false
	}
	assignHuffmanCodes(h.CODES, firstCodes)
	h.decoder = buildHuffmanCodeIndex(h.CODES, maxCodeLen, codeCounts, firstCodes)
	h.Ok = true
	return true
}

// HuffmanDecoder 霍夫曼解码器
type HuffmanDecoder struct {
	stream *BitStream
}

// NewHuffmanDecoder 创建新的霍夫曼解码器
// 入参: stream 位流
// 返回: *HuffmanDecoder 解码器对象
func NewHuffmanDecoder(stream *BitStream) *HuffmanDecoder {
	return &HuffmanDecoder{stream: stream}
}

// DecodeAValue 解码一个数值
// 入参: table 霍夫曼表, result 结果指针
// 返回: int 状态码
func (h *HuffmanDecoder) DecodeAValue(table *HuffmanTable, result *int32) int {
	if table.decoder == nil {
		decoder, err := newHuffmanCodeIndex(table.CODES)
		if err != nil {
			return -1
		}
		table.decoder = decoder
	}
	i, err := table.decoder.Decode(h.stream)
	if err != nil {
		return -1
	}
	if table.HTOOB && i == len(table.CODES)-1 {
		return JBig2OOB
	}
	rlen := table.CODES[i].Val1
	rlow := table.CODES[i].Val2
	if rlen < 0 {
		return JBig2OOB
	}
	if rlen > 0 {
		offset, err := h.stream.ReadNBits(uint32(rlen))
		if err != nil {
			return -1
		}
		if table.CODES[i].LowerRange {
			*result = rlow - int32(offset)
		} else {
			*result = rlow + int32(offset)
		}
	} else {
		*result = rlow
	}
	return 0
}

// HuffmanAssignCode 为霍夫曼表分配编码
// 入参: symcodes 霍夫曼编码列表
// 返回: error 错误信息
func HuffmanAssignCode(symcodes []HuffmanCode) error {
	_, _, firstCodes, err := huffmanCodeLayout(symcodes)
	if err != nil {
		return err
	}
	assignHuffmanCodes(symcodes, firstCodes)
	return nil
}

// assignHuffmanCodes 分配规范霍夫曼编码
// 入参: symcodes 霍夫曼编码列表, firstCodes 各码长首码
func assignHuffmanCodes(symcodes []HuffmanCode, firstCodes [maxHuffmanCodeLength + 1]uint32) {
	nextCodes := firstCodes
	for i := range symcodes {
		length := symcodes[i].Codelen
		if length <= 0 {
			continue
		}
		symcodes[i].Code = int32(nextCodes[length])
		nextCodes[length]++
	}
}

// newHuffmanCodeIndex 创建规范霍夫曼解码索引
// 入参: codes 霍夫曼编码
// 返回: *huffmanCodeIndex 解码索引, error 错误信息
func newHuffmanCodeIndex(codes []HuffmanCode) (*huffmanCodeIndex, error) {
	maxCodeLen, codeCounts, firstCodes, err := huffmanCodeLayout(codes)
	if err != nil {
		return nil, err
	}
	return buildHuffmanCodeIndex(codes, maxCodeLen, codeCounts, firstCodes), nil
}

// buildHuffmanCodeIndex 构建规范霍夫曼解码索引
// 入参: codes 霍夫曼编码, maxCodeLen 最大码长, codeCounts 码长数量, firstCodes 首码
// 返回: *huffmanCodeIndex 解码索引
func buildHuffmanCodeIndex(
	codes []HuffmanCode,
	maxCodeLen uint32,
	codeCounts [maxHuffmanCodeLength + 1]uint32,
	firstCodes [maxHuffmanCodeLength + 1]uint32,
) *huffmanCodeIndex {
	var firstIndexes [maxHuffmanCodeLength + 1]int
	total := 0
	for length := uint32(1); length <= maxCodeLen; length++ {
		firstIndexes[length] = total
		total += int(codeCounts[length])
	}
	indexes := make([]int, total)
	nextIndexes := firstIndexes
	for index, code := range codes {
		if code.Codelen <= 0 {
			continue
		}
		length := uint32(code.Codelen)
		indexes[nextIndexes[length]] = index
		nextIndexes[length]++
	}
	return &huffmanCodeIndex{
		maxCodeLen:   maxCodeLen,
		firstCodes:   firstCodes,
		codeCounts:   codeCounts,
		firstIndexes: firstIndexes,
		indexes:      indexes,
	}
}

// Decode 解码一个霍夫曼编码索引
// 入参: stream 位流
// 返回: int 编码索引, error 错误信息
func (h *huffmanCodeIndex) Decode(stream *BitStream) (int, error) {
	var code uint32
	for length := uint32(1); length <= h.maxCodeLen; length++ {
		bit, err := stream.Read1Bit()
		if err != nil {
			return 0, err
		}
		code = (code << 1) | bit
		count := h.codeCounts[length]
		firstCode := h.firstCodes[length]
		if code >= firstCode && code-firstCode < count {
			index := h.firstIndexes[length] + int(code-firstCode)
			return h.indexes[index], nil
		}
	}
	return 0, errors.New("invalid huffman code")
}

// huffmanCodeLayout 计算规范霍夫曼编码布局
// 入参: codes 霍夫曼编码
// 返回: uint32 最大码长, [33]uint32 码长数量, [33]uint32 首码, error 错误信息
func huffmanCodeLayout(codes []HuffmanCode) (
	uint32,
	[maxHuffmanCodeLength + 1]uint32,
	[maxHuffmanCodeLength + 1]uint32,
	error,
) {
	var codeCounts [maxHuffmanCodeLength + 1]uint32
	var firstCodes [maxHuffmanCodeLength + 1]uint32
	var maxCodeLen uint32
	for _, code := range codes {
		if code.Codelen < 0 || code.Codelen > maxHuffmanCodeLength {
			return 0, codeCounts, firstCodes, errors.New("invalid huffman code length")
		}
		if uint32(code.Codelen) > maxCodeLen {
			maxCodeLen = uint32(code.Codelen)
		}
	}
	for _, code := range codes {
		if code.Codelen > 0 {
			codeCounts[code.Codelen]++
		}
	}
	var firstCode uint64
	for length := uint32(1); length <= maxCodeLen; length++ {
		firstCode = (firstCode + uint64(codeCounts[length-1])) << 1
		if firstCode+uint64(codeCounts[length]) > uint64(1)<<length {
			return 0, codeCounts, firstCodes, errors.New("invalid huffman code lengths")
		}
		firstCodes[length] = uint32(firstCode)
	}
	return maxCodeLen, codeCounts, firstCodes, nil
}

// standardTableDef 标准表定义
type standardTableDef struct {
	HTOOB bool
	Lines []TableLine
}

// kHuffmanTables 标准霍夫曼表集
var kHuffmanTables = []standardTableDef{
	{false, nil},
	{false, []TableLine{{1, 4, 0}, {2, 8, 16}, {3, 16, 272}, {3, 32, 65808}}},
	{true, []TableLine{{1, 0, 0}, {2, 0, 1}, {3, 0, 2}, {4, 3, 3}, {5, 6, 11}, {6, 32, 75}, {6, -1, 0}}},
	{true, []TableLine{{8, 8, -256}, {1, 0, 0}, {2, 0, 1}, {3, 0, 2}, {4, 3, 3}, {5, 6, 11}, {8, 32, -257}, {7, 32, 75}, {6, -1, 0}}},
	{false, []TableLine{{1, 0, 1}, {2, 0, 2}, {3, 0, 3}, {4, 3, 4}, {5, 6, 12}, {5, 32, 76}}},
	{false, []TableLine{{7, 8, -255}, {1, 0, 1}, {2, 0, 2}, {3, 0, 3}, {4, 3, 4}, {5, 6, 12}, {7, 32, -256}, {6, 32, 76}}},
	{false, []TableLine{{5, 10, -2048}, {4, 9, -1024}, {4, 8, -512}, {4, 7, -256}, {5, 6, -128}, {5, 5, -64}, {4, 5, -32}, {2, 7, 0}, {3, 7, 128}, {3, 8, 256}, {4, 9, 512}, {4, 10, 1024}, {6, 32, -2049}, {6, 32, 2048}}},
	{false, []TableLine{{4, 9, -1024}, {3, 8, -512}, {4, 7, -256}, {5, 6, -128}, {5, 5, -64}, {4, 5, -32}, {4, 5, 0}, {5, 5, 32}, {5, 6, 64}, {4, 7, 128}, {3, 8, 256}, {3, 9, 512}, {3, 10, 1024}, {5, 32, -1025}, {5, 32, 2048}}},
	{true, []TableLine{{8, 3, -15}, {9, 1, -7}, {8, 1, -5}, {9, 0, -3}, {7, 0, -2}, {4, 0, -1}, {2, 1, 0}, {5, 0, 2}, {6, 0, 3}, {3, 4, 4}, {6, 1, 20}, {4, 4, 22}, {4, 5, 38}, {5, 6, 70}, {5, 7, 134}, {6, 7, 262}, {7, 8, 390}, {6, 10, 646}, {9, 32, -16}, {9, 32, 1670}, {2, -1, 0}}},
	{true, []TableLine{{8, 4, -31}, {9, 2, -15}, {8, 2, -11}, {9, 1, -7}, {7, 1, -5}, {4, 1, -3}, {3, 1, -1}, {3, 1, 1}, {5, 1, 3}, {6, 1, 5}, {3, 5, 7}, {6, 2, 39}, {4, 5, 43}, {4, 6, 75}, {5, 7, 139}, {5, 8, 267}, {6, 8, 523}, {7, 9, 779}, {6, 11, 1291}, {9, 32, -32}, {9, 32, 3339}, {2, -1, 0}}},
	{true, []TableLine{{7, 4, -21}, {8, 0, -5}, {7, 0, -4}, {5, 0, -3}, {2, 2, -2}, {5, 0, 2}, {6, 0, 3}, {7, 0, 4}, {8, 0, 5}, {2, 6, 6}, {5, 5, 70}, {6, 5, 102}, {6, 6, 134}, {6, 7, 198}, {6, 8, 326}, {6, 9, 582}, {6, 10, 1094}, {7, 11, 2118}, {8, 32, -22}, {8, 32, 4166}, {2, -1, 0}}},
	{false, []TableLine{{1, 0, 1}, {2, 1, 2}, {4, 0, 4}, {4, 1, 5}, {5, 1, 7}, {5, 2, 9}, {6, 2, 13}, {7, 2, 17}, {7, 3, 21}, {7, 4, 29}, {7, 5, 45}, {7, 6, 77}, {7, 32, 141}}},
	{false, []TableLine{{1, 0, 1}, {2, 0, 2}, {3, 1, 3}, {5, 0, 5}, {5, 1, 6}, {6, 1, 8}, {7, 0, 10}, {7, 1, 11}, {7, 2, 13}, {7, 3, 17}, {7, 4, 25}, {8, 5, 41}, {8, 32, 73}}},
	{false, []TableLine{{1, 0, 1}, {3, 0, 2}, {4, 0, 3}, {5, 0, 4}, {4, 1, 5}, {3, 3, 7}, {6, 1, 15}, {6, 2, 17}, {6, 3, 21}, {6, 4, 29}, {6, 5, 45}, {7, 6, 77}, {7, 32, 141}}},
	{false, []TableLine{{3, 0, -2}, {3, 0, -1}, {1, 0, 0}, {3, 0, 1}, {3, 0, 2}}},
	{false, []TableLine{{7, 4, -24}, {6, 2, -8}, {5, 1, -4}, {4, 0, -2}, {3, 0, -1}, {1, 0, 0}, {3, 0, 1}, {4, 0, 2}, {5, 1, 3}, {6, 2, 5}, {7, 4, 9}, {7, 32, -25}, {7, 32, 25}}},
}
