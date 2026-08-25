package detect

import "packer/internal/crypto"

// IsGzip reports whether b begins with the gzip magic bytes.
func IsGzip(b []byte) bool {
	return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b
}

// IsPackenc reports whether b begins with the PACKENC encryption magic.
func IsPackenc(b []byte) bool {
	m := crypto.Magic
	if len(b) < len(m) {
		return false
	}
	for i := range m {
		if b[i] != m[i] {
			return false
		}
	}
	return true
}
