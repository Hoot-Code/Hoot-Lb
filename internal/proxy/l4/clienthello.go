package l4

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// maxClientHelloSize is the maximum number of bytes we will read
	// to accumulate a complete ClientHello message. A legitimate
	// ClientHello is typically under 512 bytes; 16 KiB provides ample
	// headroom while bounding memory usage against a malicious peer
	// that claims an enormous handshake length.
	maxClientHelloSize = 16 * 1024

	// tlsRecordHeaderSize is the fixed size of a TLS record header:
	// 1 byte content type + 2 bytes version + 2 bytes length.
	tlsRecordHeaderSize = 5

	// tlsContentTypeHandshake is the TLS record content type for
	// handshake messages.
	tlsContentTypeHandshake = 22

	// tlsHandshakeClientHello is the handshake message type for
	// ClientHello.
	tlsHandshakeClientHello = 1

	// sniExtensionType is the TLS extension type for server_name.
	sniExtensionType = 0x0000
)

// ParseSNI extracts the SNI hostname from a TLS ClientHello message
// without performing any cryptographic operations. The caller must
// provide the raw bytes starting from the TLS record header (i.e. the
// bytes as they appear on the wire).
//
// ParseSNI reads the record framing and handshake structure to locate
// the server_name extension, then returns the hostname. If the ClientHello
// is well-formed but contains no SNI extension, it returns ("", nil).
// If the data is malformed, truncated, or oversized, it returns a
// descriptive error.
func ParseSNI(data []byte) (string, error) {
	if len(data) < tlsRecordHeaderSize {
		return "", errors.New("TLS record header too short")
	}

	// Read record header.
	contentType := data[0]
	if contentType != tlsContentTypeHandshake {
		return "", fmt.Errorf("unexpected TLS content type %d, expected %d (handshake)", contentType, tlsContentTypeHandshake)
	}

	// data[1:3] is the legacy record version; we don't validate it
	// because a passthrough proxy doesn't care about version
	// negotiation.
	recordLen := int(binary.BigEndian.Uint16(data[3:5]))
	if recordLen < 4 {
		return "", fmt.Errorf("TLS record payload too short (%d bytes)", recordLen)
	}
	if recordLen > maxClientHelloSize {
		return "", fmt.Errorf("TLS record payload exceeds maximum (%d > %d)", recordLen, maxClientHelloSize)
	}

	// The record payload starts at offset 5. Ensure we have enough
	// bytes.
	totalLen := tlsRecordHeaderSize + recordLen
	if len(data) < totalLen {
		return "", fmt.Errorf("TLS record claims %d bytes but only %d available", recordLen, len(data)-tlsRecordHeaderSize)
	}

	payload := data[tlsRecordHeaderSize:totalLen]

	// Parse handshake header: 1 byte type + 3 bytes length.
	if len(payload) < 4 {
		return "", errors.New("handshake header too short")
	}
	hsType := payload[0]
	if hsType != tlsHandshakeClientHello {
		return "", fmt.Errorf("expected ClientHello (type %d), got type %d", tlsHandshakeClientHello, hsType)
	}
	hsLen := int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	if hsLen < 34 { // 2 version + 32 random is minimum
		return "", fmt.Errorf("ClientHello handshake length too short (%d bytes)", hsLen)
	}
	if hsLen > len(payload)-4 {
		return "", fmt.Errorf("ClientHello handshake claims %d bytes but only %d available in record", hsLen, len(payload)-4)
	}

	hs := payload[4 : 4+hsLen]

	// Skip: client_version (2) + random (32) = 34 bytes.
	if len(hs) < 34 {
		return "", errors.New("ClientHello too short for version + random")
	}
	off := 34

	// Session ID: 1 byte length + that many bytes.
	if off >= len(hs) {
		return "", errors.New("ClientHello truncated at session ID length")
	}
	sessIDLen := int(hs[off])
	off++
	if off+sessIDLen > len(hs) {
		return "", errors.New("ClientHello truncated in session ID")
	}
	off += sessIDLen

	// Cipher suites: 2 byte length + that many bytes.
	if off+2 > len(hs) {
		return "", errors.New("ClientHello truncated at cipher suites length")
	}
	cipherLen := int(binary.BigEndian.Uint16(hs[off : off+2]))
	off += 2
	if off+cipherLen > len(hs) {
		return "", errors.New("ClientHello truncated in cipher suites")
	}
	off += cipherLen

	// Compression methods: 1 byte length + that many bytes.
	if off >= len(hs) {
		return "", errors.New("ClientHello truncated at compression methods length")
	}
	compLen := int(hs[off])
	off++
	if off+compLen > len(hs) {
		return "", errors.New("ClientHello truncated in compression methods")
	}
	off += compLen

	// Extensions: 2 byte length + that many bytes.
	if off+2 > len(hs) {
		// No extensions present.
		return "", nil
	}
	extLen := int(binary.BigEndian.Uint16(hs[off : off+2]))
	off += 2
	if off+extLen > len(hs) {
		return "", errors.New("ClientHello truncated in extensions")
	}
	extEnd := off + extLen

	// Walk extensions looking for server_name (type 0x0000).
	for off+4 <= extEnd {
		extType := binary.BigEndian.Uint16(hs[off : off+2])
		extDataLen := int(binary.BigEndian.Uint16(hs[off+2 : off+4]))
		off += 4

		if off+extDataLen > extEnd {
			return "", errors.New("extension data exceeds extensions boundary")
		}

		if extType == sniExtensionType {
			sni, err := parseSNIExtension(hs[off : off+extDataLen])
			if err != nil {
				return "", fmt.Errorf("parsing SNI extension: %w", err)
			}
			return sni, nil
		}

		off += extDataLen
	}

	// No SNI extension found — valid, just means routing must fall back.
	return "", nil
}

// parseSNIExtension parses the server_name extension data and returns
// the first hostname entry. The extension format is:
//
//	2 bytes: server_name_list length
//	For each entry:
//	  1 byte: name type (0 = hostname)
//	  2 bytes: name length
//	  N bytes: name
func parseSNIExtension(data []byte) (string, error) {
	if len(data) < 2 {
		return "", errors.New("SNI extension too short for list length")
	}
	listLen := int(binary.BigEndian.Uint16(data[0:2]))
	if listLen > len(data)-2 {
		return "", errors.New("SNI extension list length exceeds data")
	}

	off := 2
	listEnd := 2 + listLen

	for off+3 <= listEnd {
		nameType := data[off]
		off++
		nameLen := int(binary.BigEndian.Uint16(data[off : off+2]))
		off += 2

		if off+nameLen > listEnd {
			return "", errors.New("SNI hostname extends beyond list boundary")
		}

		if nameType == 0 { // hostname
			name := string(data[off : off+nameLen])
			if len(name) == 0 {
				return "", errors.New("SNI hostname is empty")
			}
			return name, nil
		}

		off += nameLen
	}

	return "", errors.New("SNI extension has no hostname entry")
}

// readClientHello reads from conn until a complete TLS record
// containing a ClientHello is available, or until the read budget is
// exhausted. It returns the accumulated bytes (from the start of the
// TLS record) and any error. The returned bytes are suitable for
// passing to ParseSNI.
//
// readClientHello handles the case where the ClientHello arrives split
// across multiple TCP reads. It reads byte-by-byte from the record
// header to determine the record length, then reads the remaining
// payload.
func readClientHello(conn io.Reader) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1)

	// Read the 5-byte TLS record header one byte at a time to avoid
	// over-reading into the handshake payload (which we must replay
	// to the backend).
	for len(buf) < tlsRecordHeaderSize {
		n, err := conn.Read(tmp)
		if err != nil {
			return buf, fmt.Errorf("reading TLS record header: %w", err)
		}
		if n > 0 {
			buf = append(buf, tmp[0])
		}
	}

	// Extract record length from header.
	recordLen := int(binary.BigEndian.Uint16(buf[3:5]))
	if recordLen > maxClientHelloSize {
		return buf, fmt.Errorf("TLS record too large (%d bytes, max %d)", recordLen, maxClientHelloSize)
	}

	// Read the rest of the record.
	needed := tlsRecordHeaderSize + recordLen
	for len(buf) < needed {
		grow := needed - len(buf)
		if grow > 4096 {
			grow = 4096
		}
		chunk := make([]byte, grow)
		n, err := conn.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			return buf, fmt.Errorf("reading TLS record payload: %w", err)
		}
	}

	return buf, nil
}
