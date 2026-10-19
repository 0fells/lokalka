package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

const (
	KeySize   = 32
	nonceSize = 12
	tagSize   = 16
)

func Random(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

func GenerateKeyPair() (priv, pub []byte, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return key.Bytes(), key.PublicKey().Bytes(), nil
}

func DeriveKey(privBytes, peerPub, salt, info []byte) ([]byte, error) {
	if len(privBytes) != KeySize || len(peerPub) != KeySize {
		return nil, errors.New("неверная длина ключа")
	}
	priv, err := ecdh.X25519().NewPrivateKey(privBytes)
	if err != nil {
		return nil, err
	}
	pub, err := ecdh.X25519().NewPublicKey(peerPub)
	if err != nil {
		return nil, err
	}
	shared, err := priv.ECDH(pub)
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, shared, salt, string(info), KeySize)
}

func Seal(key, plaintext []byte) ([]byte, error) {
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

func Open(key, blob []byte) ([]byte, error) {
	if len(blob) < nonceSize+tagSize {
		return nil, errors.New("короткий шифротекст")
	}
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, blob[:nonceSize], blob[nonceSize:], nil)
}

func NewAEAD(key []byte) (cipher.AEAD, error) { return gcm(key) }

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func MAC(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write(data)
	return m.Sum(nil)[:16]
}

func VerifyMAC(key, data, mac []byte) bool {
	if len(mac) != 16 {
		return false
	}
	return hmac.Equal(MAC(key, data), mac)
}
