package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// This is a dependency-free implementation of scrypt (RFC 7914) so the Go build
// needs nothing outside the standard library. It is verified byte-for-byte
// against the RFC 7914 test vectors (see scrypt_test.go) and against Python's
// hashlib.scrypt in the conformance suite.

// pbkdf2SHA256 implements PBKDF2 with HMAC-SHA256 (RFC 2898).
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := prf.Size()
	numBlocks := (keyLen + hLen - 1) / hLen
	dk := make([]byte, 0, numBlocks*hLen)
	var buf [4]byte
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf[:], uint32(block))
		prf.Write(buf[:])
		t := prf.Sum(nil)
		u := t
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for x := range t {
				t[x] ^= u[x]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

func rotl(a uint32, b uint) uint32 { return (a << b) | (a >> (32 - b)) }

// salsa208 applies the Salsa20/8 core to a 64-byte block in place.
func salsa208(b *[64]byte) {
	var x [16]uint32
	for i := 0; i < 16; i++ {
		x[i] = binary.LittleEndian.Uint32(b[i*4:])
	}
	in := x
	for i := 0; i < 8; i += 2 {
		x[4] ^= rotl(x[0]+x[12], 7)
		x[8] ^= rotl(x[4]+x[0], 9)
		x[12] ^= rotl(x[8]+x[4], 13)
		x[0] ^= rotl(x[12]+x[8], 18)
		x[9] ^= rotl(x[5]+x[1], 7)
		x[13] ^= rotl(x[9]+x[5], 9)
		x[1] ^= rotl(x[13]+x[9], 13)
		x[5] ^= rotl(x[1]+x[13], 18)
		x[14] ^= rotl(x[10]+x[6], 7)
		x[2] ^= rotl(x[14]+x[10], 9)
		x[6] ^= rotl(x[2]+x[14], 13)
		x[10] ^= rotl(x[6]+x[2], 18)
		x[3] ^= rotl(x[15]+x[11], 7)
		x[7] ^= rotl(x[3]+x[15], 9)
		x[11] ^= rotl(x[7]+x[3], 13)
		x[15] ^= rotl(x[11]+x[7], 18)
		x[1] ^= rotl(x[0]+x[3], 7)
		x[2] ^= rotl(x[1]+x[0], 9)
		x[3] ^= rotl(x[2]+x[1], 13)
		x[0] ^= rotl(x[3]+x[2], 18)
		x[6] ^= rotl(x[5]+x[4], 7)
		x[7] ^= rotl(x[6]+x[5], 9)
		x[4] ^= rotl(x[7]+x[6], 13)
		x[5] ^= rotl(x[4]+x[7], 18)
		x[11] ^= rotl(x[10]+x[9], 7)
		x[8] ^= rotl(x[11]+x[10], 9)
		x[9] ^= rotl(x[8]+x[11], 13)
		x[10] ^= rotl(x[9]+x[8], 18)
		x[12] ^= rotl(x[15]+x[14], 7)
		x[13] ^= rotl(x[12]+x[15], 9)
		x[14] ^= rotl(x[13]+x[12], 13)
		x[15] ^= rotl(x[14]+x[13], 18)
	}
	for i := 0; i < 16; i++ {
		x[i] += in[i]
		binary.LittleEndian.PutUint32(b[i*4:], x[i])
	}
}

// blockMix implements scryptBlockMix (RFC 7914 section 4). src and dst are
// 128*r bytes (2r 64-byte blocks); dst must not alias src.
func blockMix(dst, src []byte, r int) {
	var x [64]byte
	copy(x[:], src[(2*r-1)*64:])
	var t [64]byte
	for i := 0; i < 2*r; i++ {
		for j := 0; j < 64; j++ {
			t[j] = x[j] ^ src[i*64+j]
		}
		salsa208(&t)
		x = t
		if i%2 == 0 {
			copy(dst[(i/2)*64:], x[:])
		} else {
			copy(dst[(r+i/2)*64:], x[:])
		}
	}
}

func integerify(x []byte, r int) uint64 {
	off := (2*r - 1) * 64
	return binary.LittleEndian.Uint64(x[off : off+8])
}

// smix implements scryptROMix (RFC 7914 section 5) on a single 128*r block.
func smix(block []byte, r, n int, v, xy []byte) {
	rr := 128 * r
	x := xy[:rr]
	y := xy[rr : 2*rr]
	copy(x, block[:rr])
	for i := 0; i < n; i++ {
		copy(v[i*rr:i*rr+rr], x)
		blockMix(y, x, r)
		x, y = y, x
	}
	mask := uint64(n - 1) // n is a power of two
	for i := 0; i < n; i++ {
		j := int(integerify(x, r) & mask)
		for k := 0; k < rr; k++ {
			x[k] ^= v[j*rr+k]
		}
		blockMix(y, x, r)
		x, y = y, x
	}
	copy(block[:rr], x)
}

// Scrypt derives a key from a password using scrypt (RFC 7914).
func Scrypt(password, salt []byte, n, r, p, keyLen int) ([]byte, error) {
	if n <= 1 || (n&(n-1)) != 0 {
		return nil, errors.New("scrypt: N must be > 1 and a power of two")
	}
	if r <= 0 || p <= 0 {
		return nil, errors.New("scrypt: r and p must be > 0")
	}
	if keyLen <= 0 {
		return nil, errors.New("scrypt: keyLen must be > 0")
	}
	b := pbkdf2SHA256(password, salt, 1, p*128*r)
	v := make([]byte, 128*r*n)
	xy := make([]byte, 256*r)
	for i := 0; i < p; i++ {
		smix(b[i*128*r:(i+1)*128*r], r, n, v, xy)
	}
	return pbkdf2SHA256(password, b, 1, keyLen), nil
}
