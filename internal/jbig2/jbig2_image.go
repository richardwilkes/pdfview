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

// Image 图像结构体
type Image struct {
	width  int32
	height int32
	stride int32
	data   []byte
}

// NewImage 创建新图像
// 入参: width 宽度, height 高度
// 返回: *Image 图像对象
func NewImage(width, height int32) *Image {
	if width <= 0 || height <= 0 {
		return nil
	}
	stride := (width + 7) / 8
	if stride <= 0 || height > 2147483647/stride {
		return nil
	}
	size := stride * height
	data := make([]byte, size)
	return &Image{
		width:  width,
		height: height,
		stride: stride,
		data:   data,
	}
}

// reuseImage 复用图像缓冲
// 入参: image 图像对象, width 宽度, height 高度
// 返回: *Image 图像对象
func reuseImage(image *Image, width, height int32) *Image {
	if image == nil || width <= 0 || height <= 0 {
		return NewImage(width, height)
	}
	stride := (width + 7) / 8
	size := int(stride * height)
	if size > cap(image.data) {
		image.data = make([]byte, size)
	} else {
		image.data = image.data[:size]
		clear(image.data)
	}
	image.width = width
	image.height = height
	image.stride = stride
	return image
}

// Width 获取宽度
// 返回: int32 宽度
func (i *Image) Width() int32 {
	return i.width
}

// Height 获取高度
// 返回: int32 高度
func (i *Image) Height() int32 {
	return i.height
}

// Stride 获取跨度
// 返回: int32 跨度
func (i *Image) Stride() int32 {
	return i.stride
}

// Data 获取数据
// 返回: []byte 数据切片
func (i *Image) Data() []byte {
	return i.data
}

// row 获取图像行数据
// 入参: y 纵坐标
// 返回: []byte 行数据
func (i *Image) row(y int32) []byte {
	if uint32(y) >= uint32(i.height) {
		return nil
	}
	start := y * i.stride
	return i.data[start : start+i.stride]
}

// getPixelFromRow 获取行内像素
// 入参: row 行数据, x 横坐标, width 行宽度
// 返回: uint32 像素值
func getPixelFromRow(row []byte, x, width int32) uint32 {
	if row == nil || uint32(x) >= uint32(width) {
		return 0
	}
	return uint32((row[x>>3] >> uint(7-(x&7))) & 1)
}

// setPixelInRow 设置行内前景像素
// 入参: row 行数据, x 横坐标
func setPixelInRow(row []byte, x int32) {
	row[x>>3] |= 1 << uint(7-(x&7))
}

// GetPixel 获取像素值
// 入参: x 轴坐标, y 轴坐标
// 返回: int 像素值
func (i *Image) GetPixel(x, y int32) int {
	if x < 0 || x >= i.width || y < 0 || y >= i.height {
		return 0
	}
	byteIdx := y*i.stride + (x >> 3)
	bitIdx := 7 - (x & 7)
	return int((i.data[byteIdx] >> bitIdx) & 1)
}

// SetPixel 设置像素值
// 入参: x 轴坐标, y 轴坐标, v 像素值
func (i *Image) SetPixel(x, y int32, v int) {
	if x < 0 || x >= i.width || y < 0 || y >= i.height {
		return
	}
	byteIdx := y*i.stride + (x >> 3)
	bitIdx := 7 - (x & 7)
	mask := byte(1 << bitIdx)
	if v != 0 {
		i.data[byteIdx] |= mask
	} else {
		i.data[byteIdx] &^= mask
	}
}

// Fill 填充图像
// 入参: v 填充值
func (i *Image) Fill(v bool) {
	if !v {
		clear(i.data)
		return
	}
	for idx := range i.data {
		i.data[idx] = 0xFF
	}
}

// Invert 反转图像像素
func (i *Image) Invert() {
	for idx := range i.data {
		i.data[idx] = ^i.data[idx]
	}
}

// ComposeTo 将当前图像组合到目标图像
// 入参: dst 目标图像, x 轴坐标, y 轴坐标, op 组合操作
func (i *Image) ComposeTo(dst *Image, x, y int32, op ComposeOp) {
	if i == nil || dst == nil {
		return
	}
	if op < ComposeOr || op > ComposeReplace {
		return
	}
	srcX0 := int64(0)
	srcY0 := int64(0)
	srcX1 := int64(i.width)
	srcY1 := int64(i.height)
	if x < 0 {
		srcX0 = -int64(x)
	}
	if y < 0 {
		srcY0 = -int64(y)
	}
	if right := int64(x) + srcX1; right > int64(dst.width) {
		srcX1 = int64(dst.width) - int64(x)
	}
	if bottom := int64(y) + srcY1; bottom > int64(dst.height) {
		srcY1 = int64(dst.height) - int64(y)
	}
	if srcX0 >= srcX1 || srcY0 >= srcY1 {
		return
	}
	startX := int32(srcX0)
	endX := int32(srcX1)
	for srcY := int32(srcY0); srcY < int32(srcY1); srcY++ {
		dstY := y + srcY
		srcX := startX
		if x&7 == 0 && srcX&7 == 0 {
			byteCount := (endX - srcX) >> 3
			if byteCount > 0 {
				srcIndex := srcY*i.stride + (srcX >> 3)
				dstIndex := dstY*dst.stride + ((x + srcX) >> 3)
				composeBytes(
					dst.data[dstIndex:dstIndex+byteCount],
					i.data[srcIndex:srcIndex+byteCount],
					op,
				)
				srcX += byteCount << 3
			}
		}
		for srcX < endX {
			dstX := x + srcX
			count := int32(8 - (dstX & 7))
			if remaining := endX - srcX; count > remaining {
				count = remaining
			}
			shift := uint(8 - (dstX & 7) - count)
			mask := byte((uint16(1<<count) - 1) << shift)
			srcBits := i.readBits(srcX, srcY, count) << shift
			dstIndex := dstY*dst.stride + (dstX >> 3)
			dst.data[dstIndex] = composeByte(dst.data[dstIndex], srcBits, mask, op)
			srcX += count
		}
	}
}

// composeBytes 组合连续字节
// 入参: dst 目标数据, src 源数据, op 组合操作
func composeBytes(dst, src []byte, op ComposeOp) {
	switch op {
	case ComposeOr:
		for idx, value := range src {
			dst[idx] |= value
		}
	case ComposeAnd:
		for idx, value := range src {
			dst[idx] &= value
		}
	case ComposeXor:
		for idx, value := range src {
			dst[idx] ^= value
		}
	case ComposeXnor:
		for idx, value := range src {
			dst[idx] = ^(dst[idx] ^ value)
		}
	case ComposeReplace:
		copy(dst, src)
	}
}

// readBits 读取同一行内最多8位
// 入参: x 横坐标, y 纵坐标, count 位数
// 返回: byte 位数据
func (i *Image) readBits(x, y, count int32) byte {
	index := y*i.stride + (x >> 3)
	shift := x & 7
	bits := uint16(i.data[index]) << 8
	if shift+count > 8 {
		bits |= uint16(i.data[index+1])
	}
	return byte(bits >> uint(16-shift-count))
}

// composeByte 组合一个字节中的有效位
// 入参: dst 目标数据, src 源数据, mask 有效位, op 组合操作
// 返回: byte 组合结果
func composeByte(dst, src, mask byte, op ComposeOp) byte {
	var result byte
	switch op {
	case ComposeOr:
		result = dst | src
	case ComposeAnd:
		result = dst & src
	case ComposeXor:
		result = dst ^ src
	case ComposeXnor:
		result = ^(dst ^ src)
	case ComposeReplace:
		result = src
	}
	return (dst &^ mask) | (result & mask)
}

// ComposeFrom 从源图像组合到当前图像
// 入参: x 轴坐标, y 轴坐标, src 源图像, op 组合操作
func (i *Image) ComposeFrom(x, y int32, src *Image, op ComposeOp) {
	if src != nil {
		src.ComposeTo(i, x, y, op)
	}
}

// SubImage 获取子图像
// 入参: x 轴坐标, y 轴坐标, w 宽度, h 高度
// 返回: *Image 子图像对象
func (i *Image) SubImage(x, y, w, h int32) *Image {
	if w <= 0 || h <= 0 {
		return nil
	}
	sub := NewImage(w, h)
	if sub == nil {
		return nil
	}
	i.ComposeTo(sub, -x, -y, ComposeReplace)
	return sub
}

// Expand 扩展图像高度
// 入参: height 新高度, defaultPixel 默认填充值
func (i *Image) Expand(height int32, defaultPixel bool) {
	if height <= i.height || height > 2147483647/i.stride {
		return
	}
	oldSize := len(i.data)
	newSize := int(i.stride * height)
	if newSize > cap(i.data) {
		capacity := cap(i.data) * 2
		if capacity < newSize {
			capacity = newSize
		}
		newData := make([]byte, newSize, capacity)
		copy(newData, i.data)
		i.data = newData
	} else {
		i.data = i.data[:newSize]
	}
	if defaultPixel {
		for idx := oldSize; idx < newSize; idx++ {
			i.data[idx] = 0xFF
		}
	} else {
		clear(i.data[oldSize:newSize])
	}
	i.height = height
}

// Duplicate 复制图像
// 返回: *Image 新图像
func (i *Image) Duplicate() *Image {
	if i == nil {
		return nil
	}
	newImg := NewImage(i.width, i.height)
	if newImg != nil {
		copy(newImg.data, i.data)
	}
	return newImg
}

// CopyLine 复制行
// 入参: h 目标行号, srcH 源行号
func (i *Image) CopyLine(h, srcH int32) {
	if h < 0 || h >= i.height || srcH < 0 || srcH >= i.height {
		return
	}
	start := h * i.stride
	end := start + i.stride
	srcStart := srcH * i.stride
	srcEnd := srcStart + i.stride
	copy(i.data[start:end], i.data[srcStart:srcEnd])
}
