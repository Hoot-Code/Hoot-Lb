package l4

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildClientHelloRecord constructs a minimal TLS record containing a
// ClientHello with the given SNI hostname. If sni is empty, no SNI
// extension is added.
func buildClientHelloRecord(sni string) []byte {
	var hs []byte

	// Handshake type: ClientHello (1).
	hs = append(hs, tlsHandshakeClientHello)

	// Handshake length placeholder (3 bytes) — filled later.
	hsLenPos := len(hs)
	hs = append(hs, 0, 0, 0)

	// Client version: TLS 1.2.
	hs = append(hs, 0x03, 0x03)

	// Random: 32 bytes.
	for i := 0; i < 32; i++ {
		hs = append(hs, byte(i))
	}

	// Session ID length: 0.
	hs = append(hs, 0)

	// Cipher suites: one entry (TLS_AES_128_GCM_SHA256 = 0x1301).
	hs = append(hs, 0, 2) // length
	hs = append(hs, 0x13, 0x01)

	// Compression methods: 1 entry (null = 0).
	hs = append(hs, 1, 0)

	// Extensions.
	var ext []byte

	if sni != "" {
		// SNI extension (type 0x0000).
		ext = append(ext, 0x00, 0x00) // type

		// Server name list: name type (1) + name length (2) + name.
		var sniEntry []byte
		sniEntry = append(sniEntry, 0) // name type: hostname
		nameBytes := []byte(sni)
		sniEntry = append(sniEntry, byte(len(nameBytes)>>8), byte(len(nameBytes)))
		sniEntry = append(sniEntry, nameBytes...)

		// Extension data length = list length field (2) + entry.
		extDataLen := len(sniEntry) + 2
		ext = append(ext, byte(extDataLen>>8), byte(extDataLen))
		// List length.
		ext = append(ext, byte(len(sniEntry)>>8), byte(len(sniEntry)))
		ext = append(ext, sniEntry...)
	}

	// Extensions length.
	extLen := len(ext)
	hs = append(hs, byte(extLen>>8), byte(extLen))
	hs = append(hs, ext...)

	// Fill handshake length.
	hsLen := len(hs) - 4 // minus type + 3 length bytes
	hs[hsLenPos] = byte(hsLen >> 16)
	hs[hsLenPos+1] = byte(hsLen >> 8)
	hs[hsLenPos+2] = byte(hsLen)

	// Wrap in a TLS record.
	var rec []byte
	rec = append(rec, tlsContentTypeHandshake) // content type: handshake
	rec = append(rec, 0x03, 0x03)              // version: TLS 1.2
	rec = append(rec, byte(len(hs)>>8), byte(len(hs)))
	rec = append(rec, hs...)

	return rec
}

func TestParseSNISimple(t *testing.T) {
	data := buildClientHelloRecord("example.com")
	sni, err := ParseSNI(data)
	if err != nil {
		t.Fatalf("ParseSNI: %v", err)
	}
	if sni != "example.com" {
		t.Errorf("got %q, want example.com", sni)
	}
}

func TestParseSNINoExtension(t *testing.T) {
	data := buildClientHelloRecord("")
	sni, err := ParseSNI(data)
	if err != nil {
		t.Fatalf("ParseSNI: %v", err)
	}
	if sni != "" {
		t.Errorf("got %q, want empty string", sni)
	}
}

func TestParseSNITooShort(t *testing.T) {
	_, err := ParseSNI([]byte{0x16})
	if err == nil {
		t.Fatal("expected error for too-short data")
	}
}

func TestParseSNIWrongContentType(t *testing.T) {
	data := make([]byte, 10)
	data[0] = 21 // alert, not handshake
	_, err := ParseSNI(data)
	if err == nil {
		t.Fatal("expected error for wrong content type")
	}
}

func TestParseSNIOversizeRecord(t *testing.T) {
	data := make([]byte, 5)
	data[0] = tlsContentTypeHandshake
	data[3] = 0xFF
	data[4] = 0xFF // 65535 bytes
	_, err := ParseSNI(data)
	if err == nil {
		t.Fatal("expected error for oversized record")
	}
}

func TestParseSNITruncatedRecord(t *testing.T) {
	// Header says 200 bytes but we only have the header.
	data := make([]byte, 5)
	data[0] = tlsContentTypeHandshake
	data[3] = 0
	data[4] = 200
	_, err := ParseSNI(data)
	if err == nil {
		t.Fatal("expected error for truncated record")
	}
}

func TestParseSNITruncatedHandshake(t *testing.T) {
	// Valid record header but ClientHello is truncated inside.
	var hs []byte
	hs = append(hs, tlsHandshakeClientHello)
	hs = append(hs, 0, 0, 100) // claims 100 bytes
	hs = append(hs, 0x03, 0x03)
	// Only 2 bytes of random — way too short.
	var rec []byte
	rec = append(rec, tlsContentTypeHandshake)
	rec = append(rec, 0x03, 0x03)
	rec = append(rec, byte(len(hs)>>8), byte(len(hs)))
	rec = append(rec, hs...)

	_, err := ParseSNI(rec)
	if err == nil {
		t.Fatal("expected error for truncated handshake")
	}
}

func TestParseSNISplitReads(t *testing.T) {
	// Verify that readClientHello handles data split across multiple
	// reads by simulating a reader that returns byte-by-byte.
	full := buildClientHelloRecord("split.example.com")
	br := &byteReader{data: full}

	raw, err := readClientHello(br)
	if err != nil {
		t.Fatalf("readClientHello: %v", err)
	}

	sni, err := ParseSNI(raw)
	if err != nil {
		t.Fatalf("ParseSNI: %v", err)
	}
	if sni != "split.example.com" {
		t.Errorf("got %q, want split.example.com", sni)
	}
}

// byteReader returns data one byte at a time.
type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, bytes.ErrTooLarge
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

func TestParseSNILongHostname(t *testing.T) {
	// 253-byte hostname (max DNS name length).
	name := ""
	for len(name) < 253 {
		name += "a"
	}
	data := buildClientHelloRecord(name)
	sni, err := ParseSNI(data)
	if err != nil {
		t.Fatalf("ParseSNI: %v", err)
	}
	if sni != name {
		t.Errorf("hostname mismatch: got %d bytes, want 253", len(sni))
	}
}

func TestParseSNIEmptyHostname(t *testing.T) {
	// Construct a ClientHello with an SNI extension that has an empty hostname.
	var hs []byte
	hs = append(hs, tlsHandshakeClientHello)
	hsLenPos := len(hs)
	hs = append(hs, 0, 0, 0) // length placeholder
	hs = append(hs, 0x03, 0x03)
	for i := 0; i < 32; i++ {
		hs = append(hs, byte(i))
	}
	hs = append(hs, 0)    // session ID len
	hs = append(hs, 0, 2) // cipher suites len
	hs = append(hs, 0x13, 0x01)
	hs = append(hs, 1, 0) // compression len + method

	// SNI extension with empty hostname.
	var ext []byte
	ext = append(ext, 0x00, 0x00) // SNI type
	ext = append(ext, 0, 5)       // data len
	ext = append(ext, 0, 3)       // list len
	ext = append(ext, 0)          // name type
	ext = append(ext, 0, 0)       // name len = 0
	ext = append(ext, byte(0))    // empty name

	extLen := len(ext)
	hs = append(hs, byte(extLen>>8), byte(extLen))
	hs = append(hs, ext...)

	hsLen := len(hs) - 4
	hs[hsLenPos] = byte(hsLen >> 16)
	hs[hsLenPos+1] = byte(hsLen >> 8)
	hs[hsLenPos+2] = byte(hsLen)

	var rec []byte
	rec = append(rec, tlsContentTypeHandshake)
	rec = append(rec, 0x03, 0x03)
	rec = append(rec, byte(len(hs)>>8), byte(len(hs)))
	rec = append(rec, hs...)

	_, err := ParseSNI(rec)
	if err == nil {
		t.Fatal("expected error for empty SNI hostname")
	}
}

func TestParseSNIExtensionBoundsCheck(t *testing.T) {
	// Extension data length exceeds the actual data available.
	var hs []byte
	hs = append(hs, tlsHandshakeClientHello)
	hsLenPos := len(hs)
	hs = append(hs, 0, 0, 0)
	hs = append(hs, 0x03, 0x03)
	for i := 0; i < 32; i++ {
		hs = append(hs, byte(i))
	}
	hs = append(hs, 0)
	hs = append(hs, 0, 2)
	hs = append(hs, 0x13, 0x01)
	hs = append(hs, 1, 0)

	// Extensions section: claim 100 bytes but only give 2.
	extLenField := make([]byte, 2)
	binary.BigEndian.PutUint16(extLenField, 100)
	hs = append(hs, extLenField...)
	hs = append(hs, 0, 0) // just 2 bytes of actual extension data

	hsLen := len(hs) - 4
	hs[hsLenPos] = byte(hsLen >> 16)
	hs[hsLenPos+1] = byte(hsLen >> 8)
	hs[hsLenPos+2] = byte(hsLen)

	var rec []byte
	rec = append(rec, tlsContentTypeHandshake)
	rec = append(rec, 0x03, 0x03)
	rec = append(rec, byte(len(hs)>>8), byte(len(hs)))
	rec = append(rec, hs...)

	_, err := ParseSNI(rec)
	if err == nil {
		t.Fatal("expected error for extension exceeding bounds")
	}
}
