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
//   - Pruned everything outside the PDF embedded profile: the file-signature probe (probeConfigs, the random-access
//     and grouped organizations), the CWS/SWF container unwrapping (normalizeDecoderData, skipSWFHeader, the zlib
//     dependency), the io.Reader constructors and the image.RegisterFormat registration with the package-level
//     Decode/DecodeConfig it needs, DecodeAll, GetDocument, and the 8-bit gray expansion (ToGoImage, grayByteTable,
//     expandGrayByte) that a caller reading the packed page does not need. embedded.go replaces the constructors.
//
// Derived from PDFium's core/fxcodec/jbig2 (Copyright 2014 The PDFium Authors; original code copyright 2014 Foxit
// Software Inc.), BSD-3-Clause; see LICENSE-pdfium in this directory.

// Package jbig2 一个高性能、零依赖的纯 Go 语言 JBIG2 解码器
package jbig2

import (
	"errors"
)

// Decoder JBIG2解码器
type Decoder struct {
	doc       *Document
	pageIndex uint32
}

// parseGlobalSegments 解析全局段
// 返回: error 错误信息
func (d *Document) parseGlobalSegments() error {
	if d == nil || d.globalContext == nil {
		return nil
	}
	for {
		res := d.globalContext.DecodeSequential()
		if res == ResultEndReached {
			return nil
		}
		if res == ResultFailure {
			return errors.New("failed to parse global segments")
		}
	}
}
