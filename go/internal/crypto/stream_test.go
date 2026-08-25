package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	cs := DefaultChunkSize
	sizes := []int{0, 1, 100, cs - 1, cs, cs + 1, 2 * cs, 2*cs + 123}
	pass := []byte("correct horse battery staple")
	for _, sz := range sizes {
		data := make([]byte, sz)
		rand.Read(data)
		var enc bytes.Buffer
		if err := Encrypt(&enc, bytes.NewReader(data), pass); err != nil {
			t.Fatalf("size %d encrypt: %v", sz, err)
		}
		var dec bytes.Buffer
		if err := Decrypt(&dec, bytes.NewReader(enc.Bytes()), pass); err != nil {
			t.Fatalf("size %d decrypt: %v", sz, err)
		}
		if !bytes.Equal(dec.Bytes(), data) {
			t.Fatalf("size %d round-trip mismatch", sz)
		}
	}
}

func TestDecryptWrongPassphrase(t *testing.T) {
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader([]byte("secret")), []byte("right")); err != nil {
		t.Fatal(err)
	}
	var dec bytes.Buffer
	if err := Decrypt(&dec, bytes.NewReader(enc.Bytes()), []byte("wrong")); err == nil {
		t.Fatal("expected decryption failure with wrong passphrase")
	}
}

func TestDecryptTruncationDetected(t *testing.T) {
	cs := DefaultChunkSize
	data := make([]byte, 2*cs+50)
	rand.Read(data)
	var enc bytes.Buffer
	if err := Encrypt(&enc, bytes.NewReader(data), []byte("pw")); err != nil {
		t.Fatal(err)
	}
	// Drop the final chunk (the last tagLen+50 bytes region) to simulate truncation.
	truncated := enc.Bytes()[:cs+tagLen]
	var dec bytes.Buffer
	if err := Decrypt(&dec, bytes.NewReader(truncated), []byte("pw")); err == nil {
		t.Fatal("expected truncation to be detected")
	}
}
