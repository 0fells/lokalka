package proto

import (
	"bytes"
	"crypto/ed25519"
	"net"
	"testing"

	"lokalka/internal/crypto"
)

func TestFrames(t *testing.T) {
	key, err := crypto.Random(32)
	if err != nil {
		t.Fatal(err)
	}
	other, err := crypto.Random(32)
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	sender, _ := NewChannel(&wire, key, other)
	receiver, _ := NewChannel(&wire, other, key)
	msg := Message{Type: "welcome", IP: "10.66.1.2", Mask: 24, MTU: 1280}
	for i := 0; i < 3; i++ {
		if err := sender.Send(msg); err != nil {
			t.Fatal(err)
		}
		got, err := receiver.Recv()
		if err != nil || got.IP != msg.IP || got.Type != msg.Type {
			t.Fatalf("кадр %d: %+v %v", i, got, err)
		}
	}

	var tap bytes.Buffer
	sender2, _ := NewChannel(&tap, key, other)
	if err := sender2.Send(msg); err != nil {
		t.Fatal(err)
	}
	first := append([]byte(nil), tap.Bytes()...)
	receiver2, _ := NewChannel(&tap, other, key)
	if _, err := receiver2.Recv(); err != nil {
		t.Fatal(err)
	}
	tap.Write(first)
	if _, err := receiver2.Recv(); err == nil {
		t.Fatal("повторённый кадр принят")
	}

	var back bytes.Buffer
	a, _ := NewChannel(&back, key, other)
	if err := a.Send(msg); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Recv(); err == nil {
		t.Fatal("отражённый кадр принят")
	}
}

func TestDatagram(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	payload, err := crypto.Seal(key, []byte("ip"))
	if err != nil {
		t.Fatal(err)
	}
	d := Datagram{
		Type:    TypeRelay,
		Src:     net.ParseIP("10.66.1.2"),
		Dst:     net.ParseIP("10.66.1.3"),
		Payload: payload,
	}
	body, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	d.MAC = crypto.MAC(key, body)
	raw, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseDatagram(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !crypto.VerifyMAC(key, parsed.Body, parsed.MAC) {
		t.Fatal("MAC")
	}
	plain, err := crypto.Open(key, parsed.Payload)
	if err != nil || string(plain) != "ip" {
		t.Fatalf("payload %q %v", plain, err)
	}
}

func TestBroadcast(t *testing.T) {
	netIP := net.ParseIP("10.66.1.0")
	if !SameSubnetBroadcast(net.ParseIP("10.66.1.255"), netIP, 24) {
		t.Fatal("broadcast подсети")
	}
	if SameSubnetBroadcast(net.ParseIP("10.66.1.3"), netIP, 24) {
		t.Fatal("unicast принят за broadcast")
	}
	if !SameSubnetBroadcast(net.ParseIP("255.255.255.255"), netIP, 24) {
		t.Fatal("limited broadcast")
	}
}

func TestSignedHello(t *testing.T) {
	_, id, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	clientPub := bytes.Repeat([]byte{1}, 32)
	serverPub := bytes.Repeat([]byte{2}, 32)
	salt := bytes.Repeat([]byte{3}, 16)
	hello := ServerHello(id, clientPub, serverPub, salt)
	pub, gotSalt, key, err := ParseServerHello(hello, clientPub)
	if err != nil || !bytes.Equal(pub, serverPub) || !bytes.Equal(gotSalt, salt) || !bytes.Equal(key, id.Public().(ed25519.PublicKey)) {
		t.Fatalf("подписанное приветствие: %v", err)
	}

	if _, _, _, err := ParseServerHello(hello, bytes.Repeat([]byte{9}, 32)); err == nil {
		t.Fatal("подпись принята для другого клиента")
	}
	hello[5] ^= 1
	if _, _, _, err := ParseServerHello(hello, clientPub); err == nil {
		t.Fatal("подменённое приветствие принято")
	}
	got, err := ParseClientHello(ClientHello(clientPub))
	if err != nil || !bytes.Equal(got, clientPub) {
		t.Fatalf("приветствие клиента: %v", err)
	}
}

func TestLargeFrame(t *testing.T) {
	a, _ := crypto.Random(32)
	b, _ := crypto.Random(32)
	var wire bytes.Buffer
	send, _ := NewChannel(&wire, a, b)
	recv, _ := NewChannel(&wire, b, a)
	big := Message{Type: "admin-nets", Error: string(bytes.Repeat([]byte("x"), 200_000))}
	if err := send.Send(big); err != nil {
		t.Fatal(err)
	}
	got, err := recv.Recv()
	if err != nil || len(got.Error) != 200_000 {
		t.Fatalf("большой кадр: %v", err)
	}
	huge := Message{Error: string(bytes.Repeat([]byte("x"), MaxFrame))}
	if err := send.Send(huge); err == nil {
		t.Fatal("кадр больше потолка отправлен")
	}
}
