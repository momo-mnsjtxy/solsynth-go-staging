// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"solsynth.local/go-migration/ports/sphere/locations"
	"strings"
	"testing"
)

func h(s string) []byte {
	v, e := hex.DecodeString(s)
	if e != nil {
		panic(e)
	}
	return v
}
func TestEV2OfficialAuthenticationAndCommandVectors(t *testing.T) {
	// NXP AN12196 rev2 sections5.6,5.9; independent official values, not a
	// self-generated success fixture. Wrong challenge/response/MAC must fail.
	key := make([]byte, 16)
	ra := h("13C5DB8A5930439FC3DEF9A4C675360F")
	rb := h("B9E2FC789B64BF237CCCAA20EC7E6E48")
	enc, mac, e := ev2Keys(key, ra, rb)
	if e != nil || !bytes.Equal(enc, h("1309C877509E5A215007FF0ED19CA564")) || !bytes.Equal(mac, h("4C6626F5E72EA694202139295C7A7FC7")) {
		t.Fatal(enc, mac, e)
	}
	calls := 0
	x := func(v []byte) ([]byte, error) {
		calls++
		if calls == 1 {
			if !bytes.Equal(v, h("9071000002000000")) {
				t.Fatal(hex.EncodeToString(v))
			}
			return h("A04C124213C186F22399D33AC2A3021591AF"), nil
		}
		if !bytes.Equal(v, h("90AF00002035C3E05A752E0144BAC0DE51C1F22C56B34408A23D8AEA266CAB947EA8E0118D00")) {
			t.Fatal(hex.EncodeToString(v))
		}
		return h("3FA64DB5446D1F34CD6EA311167F5E4985B89690C04A05F17FA7AB2F081206639100"), nil
	}
	session, e := authenticateEV2(x, PCSCBinding{ApplicationKey: key}, bytes.NewReader(ra))
	if e != nil || calls != 2 || !bytes.Equal(session.ti, h("9D00C4DF")) {
		t.Fatal(session, calls, e)
	}
	session.counter = 1
	session.exchange = func(v []byte) ([]byte, error) {
		if !bytes.Equal(v, h("905F0000190261B6D97903566E84C3AE5274467E89EAD799B7C1A0EF7A0400")) {
			t.Fatal(hex.EncodeToString(v))
		}
		return h("57BFF87B1241E93D9100"), nil
	}
	if _, e = session.command(0x5f, []byte{2}, h("4000E0C1F121200000430000430000"), true); e != nil {
		t.Fatal(e)
	}
	s := &ev2Session{mac: h("8248134A386E86EB7FAF54A52E536CB6"), ti: h("7A21085E"), exchange: func(v []byte) ([]byte, error) {
		if !bytes.Equal(v, h("90F5000009026597A457C8CD442C00")) {
			t.Fatal(hex.EncodeToString(v))
		}
		return h("0040EEEE000100D1FE001F00004400004400002000006A00002A474282E7A479869100"), nil
	}}
	value, e := s.command(0xf5, []byte{2}, nil, false)
	if e != nil || !bytes.Equal(value, h("0040EEEE000100D1FE001F00004400004400002000006A0000")) {
		t.Fatal(hex.EncodeToString(value), e)
	}
	s.counter = 0
	s.exchange = func([]byte) ([]byte, error) {
		return h("0040EEEE000100D1FE001F00004400004400002000006A00002A474282E7A479879100"), nil
	}
	if _, e = s.command(0xf5, []byte{2}, nil, false); e == nil {
		t.Fatal("forged response MAC")
	}
	wrongKeyExchange := func(v []byte) ([]byte, error) {
		if v[1] == 0x71 {
			return h("A04C124213C186F22399D33AC2A3021591AF"), nil
		}
		return h("3FA64DB5446D1F34CD6EA311167F5E4985B89690C04A05F17FA7AB2F081206639100"), nil
	}
	if _, e = authenticateEV2(wrongKeyExchange, PCSCBinding{ApplicationKey: bytes.Repeat([]byte{1}, 16)}, bytes.NewReader(ra)); e == nil {
		t.Fatal("incorrect physical application key")
	}
}

// This independent card emulator exercises the actual APDUs rather than a fake
// locker acknowledgement. It authenticates challenges, verifies MACs, decrypts
// settings writes, and returns authenticated UID/settings; no hardware claim.
type testNTAG struct {
	binding        PCSCBinding
	settings, file []byte
	session        *ev2Session
	rb             []byte
	writes         int
	corrupt        byte
	failWrite      bool
}

func (c *testNTAG) exchange(apdu []byte) ([]byte, error) {
	if len(apdu) < 5 {
		return nil, errors.New("APDU")
	}
	if apdu[0] == 0 {
		if apdu[1] == 0xa4 {
			return []byte{0x90, 0}, nil
		}
		if apdu[1] == 0xb0 {
			return append(bytes.Clone(c.file), 0x90, 0), nil
		}
		return nil, errors.New("ISO APDU")
	}
	cmd := apdu[1]
	data := apdu[5 : len(apdu)-1]
	if cmd == 0x71 {
		c.rb = h("B9E2FC789B64BF237CCCAA20EC7E6E48")
		v, e := cbc(c.binding.ApplicationKey, make([]byte, 16), c.rb, false)
		return append(v, 0x91, 0xaf), e
	}
	if cmd == 0xaf {
		v, e := cbc(c.binding.ApplicationKey, make([]byte, 16), data, true)
		if e != nil || len(v) != 32 || !bytes.Equal(v[16:], rotate(c.rb)) {
			return nil, errors.New("challenge")
		}
		enc, mac, _ := ev2Keys(c.binding.ApplicationKey, v[:16], c.rb)
		c.session = &ev2Session{enc: enc, mac: mac, ti: h("9D00C4DF")}
		plain := append(bytes.Clone(c.session.ti), rotate(v[:16])...)
		plain = append(plain, make([]byte, 12)...)
		response, _ := cbc(c.binding.ApplicationKey, make([]byte, 16), plain, false)
		return append(response, 0x91, 0), nil
	}
	if c.session == nil {
		if cmd == 0xf5 {
			return append(bytes.Clone(c.settings), 0x91, 0), nil
		}
		return nil, errors.New("unauthenticated")
	}
	s := c.session
	signed := []byte{cmd, byte(s.counter), byte(s.counter >> 8)}
	signed = append(signed, s.ti...)
	signed = append(signed, data[:len(data)-8]...)
	if !bytes.Equal(truncatedCMAC(s.mac, signed), data[len(data)-8:]) {
		return nil, errors.New("command MAC")
	}
	var response []byte
	switch cmd {
	case 0x51:
		response = h(c.binding.UID)
		response = append(response, 0x80)
		response = append(response, make([]byte, 8)...)
	case 0xf5:
		response = bytes.Clone(c.settings)
	case 0x5f:
		v, e := cbc(s.enc, s.iv(false), data[1:len(data)-8], true)
		if e != nil {
			return nil, e
		}
		if v[1] != 0xff || v[2] != 0xef {
			return nil, errors.New("write settings")
		}
		c.settings[2], c.settings[3] = v[1], v[2]
		c.writes++
		if c.failWrite {
			return nil, io.ErrUnexpectedEOF
		} // physical write succeeded, receipt lost.
	default:
		return nil, errors.New("native APDU")
	}
	s.counter++
	if cmd == 0x51 {
		response, _ = cbc(s.enc, s.iv(true), response, false)
	}
	signed = []byte{0, byte(s.counter), byte(s.counter >> 8)}
	signed = append(signed, s.ti...)
	signed = append(signed, response...)
	mac := truncatedCMAC(s.mac, signed)
	if c.corrupt == cmd {
		mac[0] ^= 1
	}
	return append(append(response, mac...), 0x91, 0), nil
}
func testCard() (*testNTAG, locations.NFCLockRequest) {
	return testCardProfile("empty")
}
func testCardProfile(profile string) (*testNTAG, locations.NFCLockRequest) {
	id := "11111111-1111-4111-8111-111111111111"
	uri := "https://nfc.example/sun?record_id=" + id + "&picc=" + strings.Repeat("0", 32)
	if profile == "encrypted-file" {
		uri += "&enc=" + strings.Repeat("0", 32)
	}
	uri += "&cmac=" + strings.Repeat("0", 16)
	b := PCSCBinding{RecordID: id, UID: "04958CAA5C5E80", ApplicationKey: make([]byte, 16), NDEFURITemplate: uri}
	file := make([]byte, 256)
	n := len(uri) - 8 + 5
	file[0], file[1] = byte(n>>8), byte(n)
	copy(file[2:], []byte{0xd1, 1, byte(n - 4), 'U', 4})
	copy(file[7:], uri[8:])
	offset := func(name string) int { return strings.Index(uri, "&"+name+"=") + len(name) + 1 }
	appendOffset := func(settings []byte, n int) []byte { return append(settings, byte(n), byte(n>>8), byte(n>>16)) }
	settings := h("004000E0000100C1F000")
	settings = appendOffset(settings, offset("picc"))
	input := offset("cmac")
	if profile == "encrypted-file" {
		settings[7] = 0xd1
		input = offset("enc")
	}
	settings = appendOffset(settings, input)
	if profile == "encrypted-file" {
		settings = appendOffset(settings, input)
		settings = appendOffset(settings, 32)
		copy(file[input:], "CEE9A53E3E463EF1F459635736738962")
	}
	settings = appendOffset(settings, offset("cmac"))
	copy(file[offset("picc"):], "60166A0737262991266C5B272C652265")
	// Empty CMAC vector is computed here by the card only; verifier independently
	// derives its key from the encrypted UID/counter. Official crypto vectors above
	// prevent this agreement alone from becoming algorithm evidence.
	ses, _ := aesCMAC(make([]byte, 16), h("3cc30001008004958CAA5C5E80090000"))
	var message []byte
	if profile == "encrypted-file" {
		message = file[input:offset("cmac")]
	}
	copy(file[offset("cmac"):], stringsUpperHex(truncatedCMAC(ses, message)))
	return &testNTAG{binding: b, settings: settings, file: file}, locations.NFCLockRequest{RecordID: id, UID: b.UID, MetaKey: make([]byte, 16), FileKey: make([]byte, 16), MACProfile: profile, MinimumCounter: 8}
}
func stringsUpperHex(v []byte) string {
	const chars = "0123456789ABCDEF"
	out := make([]byte, 2*len(v))
	for i, b := range v {
		out[2*i] = chars[b>>4]
		out[2*i+1] = chars[b&15]
	}
	return string(out)
}
func TestNTAGPhysicalProofBindingAndUncertainRetry(t *testing.T) {
	for _, failure := range []string{"success", "lost-receipt", "wrong-uid", "bad-mac", "counter-replay", "no-mirror", "bad-key"} {
		t.Run(failure, func(t *testing.T) {
			card, req := testCard()
			switch failure {
			case "lost-receipt":
				card.failWrite = true
			case "wrong-uid":
				card.binding.UID = "04958CAA5C5E81"
			case "bad-mac":
				card.corrupt = 0xf5
			case "counter-replay":
				req.MinimumCounter = 9
			case "no-mirror":
				card.settings[1] = 0
			case "bad-key":
				req.MetaKey = bytes.Repeat([]byte{1}, 16)
			}
			binding := card.binding
			binding.UID = req.UID
			proof, e := lockNTAG424(req, binding, card.exchange, bytes.NewReader(make([]byte, 16)))
			if failure == "success" {
				if e != nil || proof.SUN.RecordID != req.RecordID || card.writes != 1 {
					t.Fatal(proof, e, card.writes)
				}
			} else if e == nil {
				t.Fatal("invalid proof accepted", proof)
			}
			if failure == "lost-receipt" {
				card.failWrite = false
				card.session = nil
				proof, e = lockNTAG424(req, binding, card.exchange, bytes.NewReader(make([]byte, 16)))
				if e != nil || card.writes != 1 || proof.SUN.RecordID != req.RecordID {
					t.Fatal("restart recovery blindly rewrote locked tag", proof, e, card.writes)
				}
			}
			if failure != "success" && failure != "lost-receipt" && card.writes != 0 {
				t.Fatal("irreversible mutation before canonical proof", card.writes)
			}
		})
	}
	var output smallPCSCOutput
	if _, e := output.Write(bytes.Repeat([]byte{1}, 1025)); e == nil {
		t.Fatal("helper output unbounded")
	}
	digest := sha256.Sum256([]byte("synthetic"))
	if len(hex.EncodeToString(digest[:])) != 64 {
		t.Fatal(digest)
	}
}

func TestNTAGStaticNDEFBindingBeforePhysicalWrite(t *testing.T) {
	for _, profile := range []string{"empty", "encrypted-file"} {
		for _, failure := range []string{"success", "wrong-record", "wrong-site", "wrong-path", "wrong-uri-type", "wrong-offset", "bad-nlen", "missing-template"} {
			t.Run(profile+"/"+failure, func(t *testing.T) {
				card, req := testCardProfile(profile)
				switch failure {
				case "wrong-record":
					at := bytes.Index(card.file, []byte(req.RecordID))
					card.file[at+len(req.RecordID)-1] = '2'
				case "wrong-site":
					at := bytes.Index(card.file, []byte("nfc.example"))
					copy(card.file[at:], "bad.example")
				case "wrong-path":
					at := bytes.Index(card.file, []byte("/sun?"))
					copy(card.file[at:], "/bad?")
				case "wrong-uri-type":
					card.file[5] = 'T'
				case "wrong-offset":
					card.settings[10]++
				case "bad-nlen":
					card.file[1]--
				case "missing-template":
					card.binding.NDEFURITemplate = ""
				}
				proof, e := lockNTAG424(req, card.binding, card.exchange, bytes.NewReader(make([]byte, 16)))
				if failure == "success" {
					if e != nil || card.writes != 1 || proof.SUN.RecordID != req.RecordID {
						t.Fatal(proof, e, card.writes)
					}
				} else if e == nil || card.writes != 0 {
					t.Fatal("unbound NDEF reached physical mutation", proof, e, card.writes)
				}
			})
		}
	}
}
