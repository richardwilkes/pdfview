package cos

import (
	"math"
)

// Object is a COS object: one of Null, Boolean, Integer, Real, String, Name, Array, Dict, Ref, or *Stream. The
// marker method keeps arbitrary types out of containers at compile time.
type Object interface {
	isObject()
}

// Null is the PDF null object. Absent dictionary entries and references to missing objects also behave as null.
type Null struct{}

// Boolean is the PDF boolean object.
type Boolean bool

// Integer is the PDF integer object.
type Integer int64

// Real is the PDF real-number object.
type Real float64

// String is the PDF string object, holding the raw bytes after escape/hex decoding. Interpretation (text versus
// binary) depends on context; DecodeTextString converts text strings to Go strings.
type String []byte

// Name is the PDF name object, holding the name's bytes after #xx escape decoding, without the leading solidus.
type Name string

// Array is the PDF array object. Elements may be Refs; resolve them through a Document.
type Array []Object

// Dict is the PDF dictionary object, keyed by Name. Values may be Refs; resolve them through a Document. Absent
// keys read as Go nil, which every consumer in this package treats as null.
type Dict map[Name]Object

// Ref is an indirect object reference ("N G R").
type Ref struct {
	// Num is the object number.
	Num int
	// Gen is the generation number.
	Gen int
}

// Stream is the PDF stream object: a dictionary plus the raw, still-encoded bytes exactly as stored in the file.
// Use Document.StreamData to apply the /Filter chain.
type Stream struct {
	// Dict is the stream dictionary.
	Dict Dict
	// Raw is the raw stream payload, sliced out of the document buffer without decoding.
	Raw []byte
}

func (Null) isObject()    {}
func (Boolean) isObject() {}
func (Integer) isObject() {}
func (Real) isObject()    {}
func (String) isObject()  {}
func (Name) isObject()    {}
func (Array) isObject()   {}
func (Dict) isObject()    {}
func (Ref) isObject()     {}
func (*Stream) isObject() {}

// AsInt returns obj as an integer. Reals are truncated toward zero, matching the tolerance PDF consumers extend
// to keys that formally require integers; non-finite reals and every other type report false.
func AsInt(obj Object) (int64, bool) {
	switch v := obj.(type) {
	case Integer:
		return int64(v), true
	case Real:
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return int64(f), true
	default:
		return 0, false
	}
}

// AsReal returns obj as a float64, accepting both Real and Integer.
func AsReal(obj Object) (float64, bool) {
	switch v := obj.(type) {
	case Real:
		return float64(v), true
	case Integer:
		return float64(v), true
	default:
		return 0, false
	}
}

// AsName returns obj as a Name.
func AsName(obj Object) (Name, bool) {
	v, ok := obj.(Name)
	return v, ok
}

// AsString returns obj as a String.
func AsString(obj Object) (String, bool) {
	v, ok := obj.(String)
	return v, ok
}

// AsBool returns obj as a bool.
func AsBool(obj Object) (value, ok bool) {
	v, ok := obj.(Boolean)
	return bool(v), ok
}

// AsArray returns obj as an Array.
func AsArray(obj Object) (Array, bool) {
	v, ok := obj.(Array)
	return v, ok
}

// AsDict returns obj as a Dict, also accepting a Stream (whose dictionary is returned), since many consumers
// accept either.
func AsDict(obj Object) (Dict, bool) {
	switch v := obj.(type) {
	case Dict:
		return v, true
	case *Stream:
		return v.Dict, true
	default:
		return nil, false
	}
}

// AsStream returns obj as a *Stream.
func AsStream(obj Object) (*Stream, bool) {
	v, ok := obj.(*Stream)
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}
