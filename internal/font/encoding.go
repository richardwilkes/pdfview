package font

import (
	"github.com/richardwilkes/pdfview/internal/cos"
	"github.com/richardwilkes/pdfview/internal/font/data"
)

// baseEncodingTable maps an encoding name to its Annex D table, nil when unrecognized.
func baseEncodingTable(name cos.Name) *[256]string {
	switch name {
	case "StandardEncoding":
		return &standardEncoding
	case "WinAnsiEncoding":
		return &winAnsiEncoding
	case "MacRomanEncoding":
		return &macRomanEncoding
	case "MacExpertEncoding":
		return &macExpertEncoding
	default:
		return nil
	}
}

// resolveEncoding builds a simple font's code→glyph-name table (ISO 32000-2 9.6.5): the font's built-in
// encoding as the base — the embedded program's own table when it declares one (builtin non-nil, Type 1),
// else for the standard Symbol and ZapfDingbats fonts their generated tables, else StandardEncoding —
// overridden by an /Encoding name or dictionary (whose /BaseEncoding, then /Differences, apply in order).
// The returned table is never mutated after Load; unmodified base tables are shared.
func resolveEncoding(d *cos.Document, dict cos.Dict, std14 string, builtin *[256]string) *[256]string {
	base := &standardEncoding
	switch {
	case builtin != nil:
		base = builtin
	case std14 == stdSymbol || std14 == stdZapfDingbats:
		if table := data.BuiltinEncoding(std14); table != nil {
			base = table
		}
	}
	encObj := d.Resolve(dict["Encoding"])
	if name, ok := cos.AsName(encObj); ok {
		if table := baseEncodingTable(name); table != nil {
			return table
		}
		return base
	}
	encDict, ok := cos.AsDict(encObj)
	if !ok {
		return base
	}
	if name, has := d.GetName(encDict, "BaseEncoding"); has {
		if table := baseEncodingTable(name); table != nil {
			base = table
		}
	}
	diffs, has := d.GetArray(encDict, "Differences")
	if !has || len(diffs) == 0 {
		return base
	}
	table := *base // Copy before applying differences; the base tables are shared.
	code := -1
	for _, entry := range diffs {
		resolved := d.Resolve(entry)
		if v, isInt := cos.AsInt(resolved); isInt {
			code = int(min(max(v, -1), 1<<30))
			continue
		}
		if name, isName := cos.AsName(resolved); isName {
			if code >= 0 && code <= 255 {
				table[code&0xff] = string(name) // The mask is redundant given the guard; it placates gosec G602.
			}
			if code >= 0 {
				code++ // Codes above 255 keep counting but assign nothing, per the array's sequencing.
			}
		}
	}
	return &table
}
