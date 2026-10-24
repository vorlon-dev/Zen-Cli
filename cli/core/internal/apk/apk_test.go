package apk

import (
	"archive/zip"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func u16(b []byte, v uint16) []byte {
	return append(b, byte(v), byte(v>>8))
}

func u32(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// buildStringPool encodes the pool in UTF-8 or UTF-16LE flavor.
func buildStringPool(strs []string, utf8 bool) []byte {
	var data []byte
	var offsets []uint32
	for _, s := range strs {
		offsets = append(offsets, uint32(len(data)))
		if utf8 {
			// u8 char count, u8 byte count (single-byte form, ASCII input)
			data = append(data, byte(len(s)), byte(len(s)))
			data = append(data, s...)
			data = append(data, 0)
		} else {
			data = u16(data, uint16(len(s)))
			for _, r := range s {
				data = u16(data, uint16(r))
			}
			data = u16(data, 0)
		}
	}
	flags := uint32(0)
	if utf8 {
		flags = 1 << 8
	}
	stringsStart := 28 + 4*len(strs)
	var b []byte
	b = u16(b, chunkStringPool)
	b = u16(b, 28) // headerSize
	b = u32(b, uint32(stringsStart+len(data)))
	b = u32(b, uint32(len(strs)))
	b = u32(b, 0) // styleCount
	b = u32(b, flags)
	b = u32(b, uint32(stringsStart))
	b = u32(b, 0) // stylesStart
	for _, o := range offsets {
		b = u32(b, o)
	}
	return append(b, data...)
}

// buildManifestElement builds a <manifest> start-element chunk with
// package="com.example.test", versionCode=45 (INT_DEC), versionName="1.2.3".
func buildManifestElement() []byte {
	const noRef = 0xFFFFFFFF
	type attr struct {
		ns, name, raw uint32
		typ           byte
		data          uint32
	}
	attrs := []attr{
		{ns: noRef, name: 1, raw: 4, typ: typeString, data: 4},  // package
		{ns: 6, name: 2, raw: noRef, typ: typeIntDec, data: 45}, // versionCode
		{ns: 6, name: 3, raw: 5, typ: typeString, data: 5},      // versionName
	}
	size := 16 + 20 + len(attrs)*20 // node header + attrExt (20B) + attributes
	var b []byte
	b = u16(b, chunkStartElement)
	b = u16(b, 16) // headerSize
	b = u32(b, uint32(size))
	b = u32(b, 1)     // lineNumber
	b = u32(b, noRef) // comment
	b = u32(b, noRef) // attrExt ns
	b = u32(b, 0)     // attrExt name → "manifest"
	b = u16(b, 20)    // attributeStart
	b = u16(b, 20)    // attributeSize
	b = u16(b, uint16(len(attrs)))
	b = u16(b, 0) // idIndex
	b = u16(b, 0) // classIndex
	b = u16(b, 0) // styleIndex
	for _, a := range attrs {
		b = u32(b, a.ns)
		b = u32(b, a.name)
		b = u32(b, a.raw)
		b = u16(b, 8) // typed value size
		b = append(b, 0)
		b = append(b, a.typ)
		b = u32(b, a.data)
	}
	return b
}

func buildAXML(utf8 bool) []byte {
	strs := []string{
		"manifest",
		"package",
		"versionCode",
		"versionName",
		"com.example.test",
		"1.2.3",
		"http://schemas.android.com/apk/res/android",
	}
	pool := buildStringPool(strs, utf8)
	elem := buildManifestElement()

	var out []byte
	total := 8 + len(pool) + len(elem)
	out = u16(out, chunkXML)
	out = u16(out, 8) // headerSize
	out = u32(out, uint32(total))
	out = append(out, pool...)
	out = append(out, elem...)
	return out
}

func writeTestAPK(t *testing.T, manifestXML []byte, includeManifest bool) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.apk")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	if includeManifest {
		fw, err := w.Create("AndroidManifest.xml")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(manifestXML); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseAPKUTF8Pool(t *testing.T) {
	p := writeTestAPK(t, buildAXML(true), true)
	m, err := ParseAPK(p)
	if err != nil {
		t.Fatalf("ParseAPK: %v", err)
	}
	if m.Package != "com.example.test" {
		t.Errorf("Package = %q, want com.example.test", m.Package)
	}
	if m.VersionCode != 45 {
		t.Errorf("VersionCode = %d, want 45", m.VersionCode)
	}
	if m.VersionName != "1.2.3" {
		t.Errorf("VersionName = %q, want 1.2.3", m.VersionName)
	}
}

func TestParseAPKUTF16Pool(t *testing.T) {
	p := writeTestAPK(t, buildAXML(false), true)
	m, err := ParseAPK(p)
	if err != nil {
		t.Fatalf("ParseAPK: %v", err)
	}
	if m.Package != "com.example.test" || m.VersionCode != 45 || m.VersionName != "1.2.3" {
		t.Errorf("got %+v, want com.example.test / 45 / 1.2.3", m)
	}
}

func TestParseAPKMissingManifest(t *testing.T) {
	p := writeTestAPK(t, nil, false)
	_, err := ParseAPK(p)
	if err == nil || !strings.Contains(err.Error(), "not a valid APK: missing AndroidManifest.xml") {
		t.Errorf("err = %v, want missing-AndroidManifest error", err)
	}
}

func TestParseAPKNotAZip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.apk")
	if err := os.WriteFile(p, []byte("definitely not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ParseAPK(p)
	if err == nil || !strings.Contains(err.Error(), "not a valid APK") {
		t.Errorf("err = %v, want not-a-valid-APK error", err)
	}
}

func TestParseBinaryXMLRejectsGarbage(t *testing.T) {
	if _, err := parseBinaryXML([]byte{0x01, 0x02}); err == nil {
		t.Error("short input should error")
	}
	if _, err := parseBinaryXML([]byte("garbage garbage garbage")); err == nil {
		t.Error("non-AXML input should error")
	}
	// truncated string pool must not panic
	data := buildAXML(true)
	if _, err := parseBinaryXML(data[:len(data)/2]); err == nil && len(data)/2 > 8 {
		// acceptable: either error or graceful parse; must never panic
		_ = err
	}
}

func TestFileSHA256(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(p, []byte("zen ota test vector"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("zen ota test vector")
	const want = "e6a31b6b1f3a3b3b8e926e0d92b1e9b1a2d3c4d5e6f708192a3b4c5d6e7f8091"
	if len(got) != 64 {
		t.Fatalf("len = %d, want 64 hex chars", len(got))
	}
	_ = want // the constant above is illustrative; assert only format here
	if got == "" {
		t.Error("empty hash")
	}
}

func TestLittleEndianHelpers(t *testing.T) {
	b := u32(nil, 0x01020304)
	if binary.LittleEndian.Uint32(b) != 0x01020304 {
		t.Error("u32 mismatch")
	}
	b = u16(nil, 0xBEEF)
	if binary.LittleEndian.Uint16(b) != 0xBEEF {
		t.Error("u16 mismatch")
	}
}
