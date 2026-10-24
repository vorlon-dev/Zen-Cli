// Package apk reads the bits of an Android package the Zen CLI needs: the
// manifest basics (package name, versionCode, versionName) and file hashes.
// The binary XML (AXML) reader is a small, dependency-free parser that only
// understands what AndroidManifest.xml contains.
package apk

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Manifest is the subset of AndroidManifest.xml the CLI cares about.
type Manifest struct {
	Package     string
	VersionName string
	VersionCode int64
}

// Chunk types inside a binary XML file.
const (
	chunkXML          = 0x0003
	chunkStringPool   = 0x0001
	chunkStartElement = 0x0102
)

// Attribute value types (TypedValue constants).
const (
	typeString = 0x03
	typeIntDec = 0x10
	typeIntHex = 0x11
)

const noReference = 0xFFFFFFFF

// ParseAPK opens the APK at path and extracts the manifest basics.
func ParseAPK(path string) (*Manifest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("not a valid APK: %w", err)
	}
	defer zr.Close()

	var raw []byte
	for _, f := range zr.File {
		if f.Name != "AndroidManifest.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read AndroidManifest.xml: %w", err)
		}
		raw, err = io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read AndroidManifest.xml: %w", err)
		}
		break
	}
	if raw == nil {
		return nil, errors.New("not a valid APK: missing AndroidManifest.xml")
	}
	m, err := parseBinaryXML(raw)
	if err != nil {
		return nil, fmt.Errorf("AndroidManifest.xml: %w", err)
	}
	if m.Package == "" {
		return nil, errors.New("AndroidManifest.xml: <manifest> has no package attribute")
	}
	return m, nil
}

// FileSHA256 returns the hex-encoded SHA-256 of the file, streamed.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FileSize returns the file size in bytes.
func FileSize(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// NewestAPK returns the most recently modified *.apk under dir (searched
// recursively), or "" when none exist. Used by publish and init.
func NewestAPK(dir string) string {
	var newest string
	var newestMod time.Time
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".apk") {
			return nil
		}
		if info, e := d.Info(); e == nil && info.ModTime().After(newestMod) {
			newest, newestMod = p, info.ModTime()
		}
		return nil
	})
	return newest
}

// parseBinaryXML walks the top-level chunks of an AXML document, captures
// the string pool and pulls the attributes of the first <manifest> element.
func parseBinaryXML(data []byte) (*Manifest, error) {
	if len(data) < 8 {
		return nil, errors.New("file too short to be binary XML")
	}
	topType := binary.LittleEndian.Uint16(data)
	topHeader := int(binary.LittleEndian.Uint16(data[2:]))
	topSize := int(binary.LittleEndian.Uint32(data[4:]))
	if topType != chunkXML {
		return nil, fmt.Errorf("not binary AXML (top chunk type 0x%04x)", topType)
	}
	if topSize > len(data) {
		return nil, errors.New("truncated binary XML")
	}
	data = data[:topSize]

	off := topHeader
	if off < 8 {
		off = 8
	}

	var pool *stringPool
	var manifest *Manifest
	for off+8 <= len(data) {
		typ := binary.LittleEndian.Uint16(data[off:])
		size := int(binary.LittleEndian.Uint32(data[off+4:]))
		if size < 8 || off+size > len(data) {
			return nil, errors.New("corrupt chunk table")
		}
		chunk := data[off : off+size]
		switch typ {
		case chunkStringPool:
			pool = parseStringPool(chunk)
		case chunkStartElement:
			name, attrs := parseStartElement(chunk, pool)
			if name == "manifest" && manifest == nil {
				manifest = manifestFromAttrs(attrs, pool)
			}
		}
		off += size
	}
	if manifest == nil {
		return nil, errors.New("no <manifest> element found")
	}
	return manifest, nil
}

// stringPool is the decoded RES_STRING_POOL_TYPE chunk.
type stringPool struct {
	strs []string
}

const flagUTF8 = 1 << 8

func parseStringPool(b []byte) *stringPool {
	p := &stringPool{}
	if len(b) < 28 {
		return p
	}
	headerSize := int(binary.LittleEndian.Uint16(b[2:]))
	count := int(binary.LittleEndian.Uint32(b[8:]))
	flags := binary.LittleEndian.Uint32(b[16:])
	stringsStart := int(binary.LittleEndian.Uint32(b[20:]))
	utf8 := flags&flagUTF8 != 0
	if headerSize < 8 || headerSize+count*4 > len(b) {
		return p
	}
	offsets := make([]int, count)
	for i := range offsets {
		offsets[i] = int(binary.LittleEndian.Uint32(b[headerSize+i*4:]))
	}
	if stringsStart <= 0 || stringsStart > len(b) {
		return p
	}
	for _, o := range offsets {
		pos := stringsStart + o
		if pos < 0 || pos >= len(b) {
			p.strs = append(p.strs, "")
			continue
		}
		s, ok := decodeString(b[pos:], utf8)
		if !ok {
			s = ""
		}
		p.strs = append(p.strs, s)
	}
	return p
}

func (p *stringPool) at(i uint32) string {
	if p == nil || int(i) >= len(p.strs) {
		return ""
	}
	return p.strs[i]
}

// decodeString reads one string pool entry. UTF-8 entries carry two length
// fields (u16 chars, u8/u16 bytes) before the payload; UTF-16 entries carry
// a u16 (optionally u32) char count followed by UTF-16LE data.
func decodeString(b []byte, utf8 bool) (string, bool) {
	if utf8 {
		_, n := decodeLen8(b)
		if n <= 0 {
			return "", false
		}
		byteLen, n2 := decodeLen8(b[n:])
		if n2 <= 0 {
			return "", false
		}
		start := n + n2
		end := start + byteLen
		if end+1 > len(b) { // +1 for the trailing NUL
			return "", false
		}
		return string(b[start:end]), true
	}
	u16len, n := decodeLen16(b)
	if n <= 0 {
		return "", false
	}
	start := n
	end := start + u16len*2
	if end+2 > len(b) {
		return "", false
	}
	runes := make([]rune, 0, u16len)
	for i := start; i+1 < end; i += 2 {
		runes = append(runes, rune(binary.LittleEndian.Uint16(b[i:])))
	}
	return string(runes), true
}

func decodeLen8(b []byte) (int, int) {
	if len(b) < 1 {
		return 0, 0
	}
	if b[0]&0x80 == 0 {
		return int(b[0]), 1
	}
	if len(b) < 2 {
		return 0, 0
	}
	return int(b[0]&0x7f)<<8 | int(b[1]), 2
}

func decodeLen16(b []byte) (int, int) {
	if len(b) < 2 {
		return 0, 0
	}
	v := int(binary.LittleEndian.Uint16(b))
	if v&0x8000 == 0 {
		return v, 2
	}
	if len(b) < 4 {
		return 0, 0
	}
	return (v&0x7fff)<<16 | int(binary.LittleEndian.Uint16(b[2:])), 4
}

// attribute is one RES_XMLTree_attribute (ns, name, rawValue, typed value).
type attribute struct {
	ns   uint32
	name uint32
	raw  uint32
	typ  uint8
	data uint32
}

// parseStartElement decodes a RES_XML_START_ELEMENT_TYPE chunk into the
// element name and its attributes. All offsets are bounds-checked so a
// malformed file can never panic.
func parseStartElement(b []byte, pool *stringPool) (string, []attribute) {
	if len(b) < 16 {
		return "", nil
	}
	headerSize := int(binary.LittleEndian.Uint16(b[2:]))
	if headerSize < 16 || headerSize+14 > len(b) {
		return "", nil
	}
	nameIdx := binary.LittleEndian.Uint32(b[headerSize+4:])
	attrStart := int(binary.LittleEndian.Uint16(b[headerSize+8:]))
	attrSize := int(binary.LittleEndian.Uint16(b[headerSize+10:]))
	count := int(binary.LittleEndian.Uint16(b[headerSize+12:]))
	if attrSize <= 0 {
		attrSize = 20
	}
	base := headerSize + attrStart
	if count < 0 || base < 0 || base+count*attrSize > len(b) {
		return pool.at(nameIdx), nil
	}
	attrs := make([]attribute, 0, count)
	for i := 0; i < count; i++ {
		a := b[base+i*attrSize : base+(i+1)*attrSize]
		if len(a) < 20 {
			break
		}
		attrs = append(attrs, attribute{
			ns:   binary.LittleEndian.Uint32(a[0:]),
			name: binary.LittleEndian.Uint32(a[4:]),
			raw:  binary.LittleEndian.Uint32(a[8:]),
			typ:  a[15],
			data: binary.LittleEndian.Uint32(a[16:]),
		})
	}
	return pool.at(nameIdx), attrs
}

func manifestFromAttrs(attrs []attribute, pool *stringPool) *Manifest {
	m := &Manifest{}
	for _, a := range attrs {
		switch pool.at(a.name) {
		case "package":
			m.Package = attrString(a, pool)
		case "versionCode":
			m.VersionCode = attrInt(a, pool)
		case "versionName":
			m.VersionName = attrString(a, pool)
		}
	}
	return m
}

func attrString(a attribute, pool *stringPool) string {
	if a.typ == typeString {
		return pool.at(a.data)
	}
	if a.raw != noReference {
		return pool.at(a.raw)
	}
	return ""
}

func attrInt(a attribute, pool *stringPool) int64 {
	switch a.typ {
	case typeIntDec:
		v := int64(int32(a.data))
		if v < 0 {
			v = int64(uint32(a.data))
		}
		return v
	case typeIntHex:
		return int64(a.data)
	}
	if s := pool.at(a.raw); s != "" {
		if v, err := strconv.ParseInt(s, 0, 64); err == nil {
			return v
		}
	}
	return 0
}
