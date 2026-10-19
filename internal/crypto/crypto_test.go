package crypto

import "testing"

func TestSealOpen(t *testing.T) {
	key, err := Random(KeySize)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := Seal(key, []byte("lokalka"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(key, blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "lokalka" {
		t.Fatalf("plaintext %q", plain)
	}
	blob[len(blob)-1] ^= 1
	if _, err := Open(key, blob); err == nil {
		t.Fatal("повреждённый пакет принят")
	}
}

func TestDeriveKeySymmetric(t *testing.T) {
	aPriv, aPub, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	bPriv, bPub, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	info := []byte("lokalka-pair|home|10.66.1.2|10.66.1.3")
	aKey, err := DeriveKey(aPriv, bPub, nil, info)
	if err != nil {
		t.Fatal(err)
	}
	bKey, err := DeriveKey(bPriv, aPub, nil, info)
	if err != nil {
		t.Fatal(err)
	}
	if string(aKey) != string(bKey) {
		t.Fatal("ключи пары не совпали")
	}
}

func TestMAC(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	mac := MAC(key, []byte("data"))
	if !VerifyMAC(key, []byte("data"), mac) {
		t.Fatal("MAC не сошёлся")
	}
	if VerifyMAC(key, []byte("data2"), mac) {
		t.Fatal("чужой MAC принят")
	}
}
