package msfp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// The contexts the engine seals under (host.md section 5).
const (
	sealCurrent = "msfp current key"
	sealNext    = "msfp next key"
	sealOld     = "msfp old key"
	sealRevoked = "msfp revoked key"
	sealIDs     = "msfp ids secret"
	sealCode    = "msfp code secret"
)

// PlainSealer seals nothing: what it "seals" is kept as it is, its
// context in front. For tests, and for the memory store, whose records
// are never written anywhere. Never for a store on disk.
type PlainSealer struct{}

// Seal gives the context, a line feed, and plain.
func (PlainSealer) Seal(plain []byte, context string) ([]byte, error) {
	return append([]byte(context+"\n"), plain...), nil
}

// Open gives back what Seal was given, if it was sealed under context.
func (PlainSealer) Open(sealed []byte, context string) ([]byte, error) {
	prefix := context + "\n"
	if len(sealed) < len(prefix) || string(sealed[:len(prefix)]) != prefix {
		return nil, ErrSealed
	}
	return append([]byte(nil), sealed[len(prefix):]...), nil
}

// KeySealer seals with AES-256-GCM under a 32-byte key, the context as
// the additional data, so that a box opens only under its own context. A
// box is the 12-byte nonce, then the sealed bytes.
func KeySealer(key []byte) (Sealer, error) {
	if len(key) != 32 {
		return nil, errors.New("msfp: a sealing key is 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcmSealer{gcm}, nil
}

type gcmSealer struct{ aead cipher.AEAD }

func (s gcmSealer) Seal(plain []byte, context string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, []byte(context)), nil
}

func (s gcmSealer) Open(sealed []byte, context string) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrSealed
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], []byte(context))
	if err != nil {
		return nil, ErrSealed
	}
	return plain, nil
}

// KeyFileSealer is a KeySealer whose key is in its own file at path: 32
// random bytes, made on first use with mode 0600. Keep that file apart
// from the store's data where you can (another disk, another backup):
// whoever holds both holds the keys.
func KeyFileSealer(path string) (Sealer, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		// O_EXCL: two processes starting at once never write two keys.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(key); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("msfp: %s is not a sealing key (32 bytes)", path)
	}
	return KeySealer(key)
}
