// Package plist implements bounded parsing and binary serialization of Apple
// property lists, the data format used by several AirPlay control-plane
// responses and parameters. It deliberately validates object counts, depth,
// and element sizes before allocating so untrusted input cannot force
// unbounded work.
package plist

import (
	"errors"
	"sort"
	"time"
)

var (
	// ErrTooDeep is returned when nesting exceeds Limits.MaxDepth.
	ErrTooDeep = errors.New("plist: nesting too deep")
	// ErrTooManyObjects is returned when the object count exceeds Limits.MaxObjects.
	ErrTooManyObjects = errors.New("plist: too many objects")
	// ErrTooLarge is returned when input exceeds Limits.MaxBytes.
	ErrTooLarge = errors.New("plist: input too large")
	// ErrMalformed is returned for structurally invalid plists.
	ErrMalformed = errors.New("plist: malformed input")
)

// Kind identifies the type of a Value.
type Kind uint8

const (
	KindBool Kind = iota
	KindInt
	KindReal
	KindDate
	KindData
	KindString
	KindArray
	KindDict
)

func (k Kind) String() string {
	switch k {
	case KindBool:
		return "bool"
	case KindInt:
		return "int"
	case KindReal:
		return "real"
	case KindDate:
		return "date"
	case KindData:
		return "data"
	case KindString:
		return "string"
	case KindArray:
		return "array"
	case KindDict:
		return "dict"
	default:
		return "unknown"
	}
}

// Value is one plist node. Exactly one field is meaningful per Kind.
type Value struct {
	Kind Kind

	Bool   bool
	Int    int64
	Real   float64
	Date   time.Time
	Data   []byte
	String string
	Array  []Value
	Dict   map[string]Value
}

// Bool returns a boolean plist value.
func Bool(b bool) *Value { return &Value{Kind: KindBool, Bool: b} }

// Int returns an integer plist value.
func Int(v int64) *Value { return &Value{Kind: KindInt, Int: v} }

// Real returns a floating-point plist value.
func Real(v float64) *Value { return &Value{Kind: KindReal, Real: v} }

// String returns a string plist value.
func String(s string) *Value { return &Value{Kind: KindString, String: s} }

// Data returns a data plist value.
func Data(b []byte) *Value { return &Value{Kind: KindData, Data: b} }

// Array returns an array plist value.
func Array(items ...*Value) *Value {
	v := &Value{Kind: KindArray, Array: make([]Value, len(items))}
	for i, it := range items {
		v.Array[i] = *it
	}
	return v
}

// Dict returns a dictionary plist value.
func Dict(pairs map[string]*Value) *Value {
	v := &Value{Kind: KindDict, Dict: make(map[string]Value, len(pairs))}
	for k, val := range pairs {
		v.Dict[k] = *val
	}
	return v
}

// Limits bounds plist parsing and serialization.
type Limits struct {
	MaxDepth     int
	MaxObjects   int
	MaxBytes     int64
	MaxKeyLen    int
	MaxDataLen   int
	MaxStringLen int
}

// DefaultLimits returns conservative plist limits.
func DefaultLimits() Limits {
	return Limits{
		MaxDepth:     32,
		MaxObjects:   10000,
		MaxBytes:     16 << 20,
		MaxKeyLen:    1024,
		MaxDataLen:   8 << 20,
		MaxStringLen: 1 << 20,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxDepth <= 0 {
		l.MaxDepth = d.MaxDepth
	}
	if l.MaxObjects <= 0 {
		l.MaxObjects = d.MaxObjects
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxKeyLen <= 0 {
		l.MaxKeyLen = d.MaxKeyLen
	}
	if l.MaxDataLen <= 0 {
		l.MaxDataLen = d.MaxDataLen
	}
	if l.MaxStringLen <= 0 {
		l.MaxStringLen = d.MaxStringLen
	}
	return l
}

func sortedKeys(m map[string]Value) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
