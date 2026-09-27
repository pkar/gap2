package plist

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestDecodeBinaryHandBuilt(t *testing.T) {
	fixture := []byte{
		'b', 'p', 'l', 'i', 's', 't', '0', '0',
		0x52, 'h', 'i',
		0x08,                               // offset table: object 0 at byte 8
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // trailer unused
		0x01,                                           // offsetIntSize
		0x01,                                           // objectRefSize
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, // numObjects
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // topObject
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x0B, // offsetTableOffset
	}
	v, err := Decode(fixture, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != KindString || v.String != "hi" {
		t.Fatalf("decoded = %+v", v)
	}
}

func TestBinaryRoundTrip(t *testing.T) {
	want := Dict(map[string]*Value{
		"name":    String("gap2"),
		"ports":   Array(Int(7000), Int(7001), Int(-1)),
		"ratio":   Real(1.5),
		"enabled": Bool(true),
		"blob":    Data([]byte{0, 1, 2, 3, 4}),
		"unicode": String("héllo → 世界"),
		"when":    {Kind: KindDate, Date: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
	})
	enc, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(enc, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	assertValueEqual(t, got, want)
}

func TestBinaryLargeData(t *testing.T) {
	big := bytes.Repeat([]byte{0xAB}, 300)
	want := Dict(map[string]*Value{"blob": Data(big)})
	enc, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(enc, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Dict["blob"].Kind != KindData || !bytes.Equal(got.Dict["blob"].Data, big) {
		t.Fatal("large data round trip failed")
	}
}

func TestEncodeDeterministic(t *testing.T) {
	v := Dict(map[string]*Value{
		"b": String("two"),
		"a": String("one"),
		"c": String("three"),
	})
	e1, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(e1, e2) {
		t.Fatal("encoding is not deterministic")
	}
}

func TestDecodeXML(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>name</key><string>gap2</string>
  <key>ports</key><array><integer>7000</integer><integer>7001</integer></array>
  <key>enabled</key><true/>
  <key>off</key><false/>
  <key>ratio</key><real>1.5</real>
  <key>blob</key><data>AAECAwQ=</data>
  <key>when</key><date>2026-01-02T03:04:05Z</date>
</dict>
</plist>`
	v, err := Decode([]byte(xml), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != KindDict {
		t.Fatalf("kind = %s", v.Kind)
	}
	if v.Dict["name"].String != "gap2" {
		t.Fatalf("name = %+v", v.Dict["name"])
	}
	if len(v.Dict["ports"].Array) != 2 || v.Dict["ports"].Array[0].Int != 7000 {
		t.Fatalf("ports = %+v", v.Dict["ports"])
	}
	if !v.Dict["enabled"].Bool || v.Dict["off"].Bool {
		t.Fatalf("bool values wrong")
	}
	if v.Dict["ratio"].Real != 1.5 {
		t.Fatalf("ratio = %v", v.Dict["ratio"].Real)
	}
	if !bytes.Equal(v.Dict["blob"].Data, []byte{0, 1, 2, 3, 4}) {
		t.Fatalf("blob = %v", v.Dict["blob"].Data)
	}
	if !v.Dict["when"].Date.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("when = %v", v.Dict["when"].Date)
	}
}

func TestDepthLimit(t *testing.T) {
	deep := Array()
	cur := deep
	for i := 0; i < 10; i++ {
		cur.Array = append(cur.Array, *Array())
		cur = &cur.Array[0]
	}
	enc, err := Encode(deep)
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxDepth = 2
	if _, err := Decode(enc, limits); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("err = %v, want ErrTooDeep", err)
	}
}

func TestObjectLimit(t *testing.T) {
	arr := Array()
	for i := 0; i < 20; i++ {
		arr.Array = append(arr.Array, *Int(int64(i)))
	}
	enc, err := Encode(arr)
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxObjects = 5
	if _, err := Decode(enc, limits); !errors.Is(err, ErrTooManyObjects) {
		t.Fatalf("err = %v, want ErrTooManyObjects", err)
	}
}

func TestByteLimit(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxBytes = 4
	if _, err := Decode([]byte("bplist00"), limits); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestMalformedBinary(t *testing.T) {
	if _, err := Decode([]byte{0x00, 0x01}, DefaultLimits()); err == nil {
		t.Fatal("expected error for short input")
	}
}

func TestBinaryOffsetTableOverflow(t *testing.T) {
	encoded, err := Encode(String("ok"))
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint64(encoded[len(encoded)-8:], ^uint64(0))
	if _, err := Decode(encoded, DefaultLimits()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

func TestMalformedXML(t *testing.T) {
	if _, err := Decode([]byte("<notplist/>"), DefaultLimits()); err == nil {
		t.Fatal("expected error for unexpected XML root")
	}
}

func assertValueEqual(t *testing.T, got, want *Value) {
	t.Helper()
	if got.Kind != want.Kind {
		t.Fatalf("kind %s != %s", got.Kind, want.Kind)
	}
	switch want.Kind {
	case KindBool:
		if got.Bool != want.Bool {
			t.Fatalf("bool %v != %v", got.Bool, want.Bool)
		}
	case KindInt:
		if got.Int != want.Int {
			t.Fatalf("int %d != %d", got.Int, want.Int)
		}
	case KindReal:
		if got.Real != want.Real {
			t.Fatalf("real %v != %v", got.Real, want.Real)
		}
	case KindDate:
		if !got.Date.Equal(want.Date) {
			t.Fatalf("date %v != %v", got.Date, want.Date)
		}
	case KindData:
		if !bytes.Equal(got.Data, want.Data) {
			t.Fatalf("data mismatch")
		}
	case KindString:
		if got.String != want.String {
			t.Fatalf("string %q != %q", got.String, want.String)
		}
	case KindArray:
		if len(got.Array) != len(want.Array) {
			t.Fatalf("array len %d != %d", len(got.Array), len(want.Array))
		}
		for i := range want.Array {
			assertValueEqual(t, &got.Array[i], &want.Array[i])
		}
	case KindDict:
		if len(got.Dict) != len(want.Dict) {
			t.Fatalf("dict len %d != %d", len(got.Dict), len(want.Dict))
		}
		for k, wv := range want.Dict {
			gv, ok := got.Dict[k]
			if !ok {
				t.Fatalf("missing key %q", k)
			}
			assertValueEqual(t, &gv, &wv)
		}
	}
}

// buildBinary assembles a binary plist from raw object encodings, using
// 4-byte offsets and 2-byte object references. Object 0 is the top object.
func buildBinary(objects ...[]byte) []byte {
	out := []byte("bplist00")
	offsets := make([]uint32, len(objects))
	for i, o := range objects {
		offsets[i] = uint32(len(out))
		out = append(out, o...)
	}
	table := len(out)
	for _, off := range offsets {
		out = binary.BigEndian.AppendUint32(out, off)
	}
	out = append(out, 0, 0, 0, 0, 0, 0, 4, 2)
	out = binary.BigEndian.AppendUint64(out, uint64(len(objects)))
	out = binary.BigEndian.AppendUint64(out, 0)
	return binary.BigEndian.AppendUint64(out, uint64(table))
}

// A fill byte as the top object used to decode to (nil, nil), which callers
// dereferenced.
func TestDecodeBinaryTopLevelFill(t *testing.T) {
	v, err := Decode(buildBinary([]byte{0x0F}), DefaultLimits())
	if v != nil || !errors.Is(err, ErrMalformed) {
		t.Fatalf("Decode = %v, %v; want nil, ErrMalformed", v, err)
	}
}

// An array whose entries all reference one large data object must not
// expand to many copies of it.
func TestDecodeBinaryReferenceAmplification(t *testing.T) {
	const refs, size = 5000, 64 << 10
	arr := []byte{0xAF, 0x11}
	arr = binary.BigEndian.AppendUint16(arr, refs)
	for range refs {
		arr = binary.BigEndian.AppendUint16(arr, 1)
	}
	data := []byte{0x4F, 0x12}
	data = binary.BigEndian.AppendUint32(data, size)
	data = append(data, make([]byte, size)...)

	input := buildBinary(arr, data)
	if _, err := Decode(input, DefaultLimits()); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Decode of %d-byte input expanding to %d MiB: err = %v, want ErrTooLarge",
			len(input), refs*size>>20, err)
	}

	// Modest sharing, as Apple's encoder does for repeated strings, still works.
	small := []byte{0xA4, 0, 1, 0, 1, 0, 1, 0, 1}
	v, err := Decode(buildBinary(small, []byte{0x52, 'h', 'i'}), DefaultLimits())
	if err != nil || len(v.Array) != 4 || v.Array[3].String != "hi" {
		t.Fatalf("shared refs: %v, %v", v, err)
	}
}

// A UID marker 0x8n carries n+1 bytes.
func TestDecodeBinaryUIDLength(t *testing.T) {
	for _, tc := range []struct {
		obj  []byte
		want int64
	}{
		{[]byte{0x80, 0x2A}, 0x2A},
		{[]byte{0x81, 0x01, 0x02}, 0x0102},
		{[]byte{0x83, 0x01, 0x02, 0x03, 0x04}, 0x01020304},
	} {
		v, err := Decode(buildBinary(tc.obj), DefaultLimits())
		if err != nil || v.Int != tc.want {
			t.Fatalf("UID %x = %v, %v; want %d", tc.obj, v, err, tc.want)
		}
	}
}
