package crypto

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Magic identifies the PACKENC encryption layer.
var Magic = []byte("PACKENC\x01")

const (
	DefaultN         = 1 << 15
	DefaultR         = 8
	DefaultP         = 1
	DefaultKeyLen    = 32
	DefaultChunkSize = 64 * 1024
	saltLen          = 16
	noncePrefixLen   = 7
	tagLen           = 16
	nonceLen         = 12
)

type scryptParams struct {
	N int `json:"N"`
	R int `json:"r"`
	P int `json:"p"`
}

// Header is the JSON header of a PACKENC stream. See SPEC.md section 4.
type Header struct {
	Version     int          `json:"version"`
	KDF         string       `json:"kdf"`
	Salt        string       `json:"salt"`
	Scrypt      scryptParams `json:"scrypt"`
	KeyLen      int          `json:"keylen"`
	Cipher      string       `json:"cipher"`
	NoncePrefix string       `json:"nonce_prefix"`
	ChunkSize   int          `json:"chunk_size"`
}

func makeNonce(prefix []byte, counter uint32, last bool) []byte {
	nonce := make([]byte, nonceLen)
	copy(nonce[:noncePrefixLen], prefix)
	binary.BigEndian.PutUint32(nonce[noncePrefixLen:noncePrefixLen+4], counter)
	if last {
		nonce[nonceLen-1] = 1
	}
	return nonce
}

// Encrypt reads plaintext from src and writes a PACKENC stream to dst.
func Encrypt(dst io.Writer, src io.Reader, passphrase []byte) error {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	noncePrefix := make([]byte, noncePrefixLen)
	if _, err := rand.Read(noncePrefix); err != nil {
		return err
	}
	key, err := Scrypt(passphrase, salt, DefaultN, DefaultR, DefaultP, DefaultKeyLen)
	if err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}

	hdr := Header{
		Version:     1,
		KDF:         "scrypt",
		Salt:        base64.StdEncoding.EncodeToString(salt),
		Scrypt:      scryptParams{N: DefaultN, R: DefaultR, P: DefaultP},
		KeyLen:      DefaultKeyLen,
		Cipher:      "aes-256-gcm",
		NoncePrefix: base64.StdEncoding.EncodeToString(noncePrefix),
		ChunkSize:   DefaultChunkSize,
	}
	hb, err := json.Marshal(hdr)
	if err != nil {
		return err
	}
	if _, err := dst.Write(Magic); err != nil {
		return err
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(hb)))
	if _, err := dst.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := dst.Write(hb); err != nil {
		return err
	}

	br := bufio.NewReaderSize(src, DefaultChunkSize)
	chunk := make([]byte, DefaultChunkSize)
	var counter uint32
	for {
		n, rerr := io.ReadFull(br, chunk)
		if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
			return rerr
		}
		last := false
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			last = true
		} else if _, perr := br.Peek(1); perr == io.EOF {
			last = true
		} else if perr != nil {
			return perr
		}
		nonce := makeNonce(noncePrefix, counter, last)
		ct := gcm.Seal(nil, nonce, chunk[:n], nil)
		if _, err := dst.Write(ct); err != nil {
			return err
		}
		counter++
		if last {
			break
		}
	}
	return nil
}

// Decrypt reads a PACKENC stream from src and writes plaintext to dst.
func Decrypt(dst io.Writer, src io.Reader, passphrase []byte) error {
	magic := make([]byte, len(Magic))
	if _, err := io.ReadFull(src, magic); err != nil {
		return fmt.Errorf("not a PACKENC stream: %w", err)
	}
	if string(magic) != string(Magic) {
		return errors.New("not a PACKENC stream: bad magic")
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(src, lenBuf[:]); err != nil {
		return err
	}
	hlen := binary.BigEndian.Uint32(lenBuf[:])
	if hlen == 0 || hlen > 1<<20 {
		return errors.New("invalid PACKENC header length")
	}
	hb := make([]byte, hlen)
	if _, err := io.ReadFull(src, hb); err != nil {
		return err
	}
	var hdr Header
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return err
	}
	if hdr.KDF != "scrypt" || hdr.Cipher != "aes-256-gcm" {
		return fmt.Errorf("unsupported kdf/cipher: %s/%s", hdr.KDF, hdr.Cipher)
	}
	salt, err := base64.StdEncoding.DecodeString(hdr.Salt)
	if err != nil {
		return err
	}
	noncePrefix, err := base64.StdEncoding.DecodeString(hdr.NoncePrefix)
	if err != nil {
		return err
	}
	if hdr.ChunkSize <= 0 || len(noncePrefix) != noncePrefixLen {
		return errors.New("invalid PACKENC header fields")
	}
	key, err := Scrypt(passphrase, salt, hdr.Scrypt.N, hdr.Scrypt.R, hdr.Scrypt.P, hdr.KeyLen)
	if err != nil {
		return err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}

	encChunk := hdr.ChunkSize + tagLen
	br := bufio.NewReaderSize(src, encChunk)
	buf := make([]byte, encChunk)
	var counter uint32
	for {
		n, rerr := io.ReadFull(br, buf)
		if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
			return rerr
		}
		if n == 0 && rerr == io.EOF {
			if counter == 0 {
				return errors.New("empty ciphertext")
			}
			// Should have already broken on the final chunk.
			return errors.New("truncated ciphertext")
		}
		last := false
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			last = true
		} else if _, perr := br.Peek(1); perr == io.EOF {
			last = true
		} else if perr != nil {
			return perr
		}
		if n < tagLen {
			return errors.New("corrupt: short chunk")
		}
		nonce := makeNonce(noncePrefix, counter, last)
		pt, err := gcm.Open(nil, nonce, buf[:n], nil)
		if err != nil {
			return fmt.Errorf("decryption failed (wrong passphrase or corrupt/truncated data): %w", err)
		}
		if _, err := dst.Write(pt); err != nil {
			return err
		}
		counter++
		if last {
			break
		}
	}
	return nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
