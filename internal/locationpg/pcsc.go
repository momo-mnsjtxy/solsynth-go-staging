// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/locations"
	"strings"
	"sync"
	"time"
)

// Explicit bindings prevent selecting a different card/reader/key after a retry.
// ApplicationKey is the independently enrolled NTAG application change key,
// not a guessed/default key or the SDM read key used by anonymous SUN readers.
type PCSCBinding struct {
	RecordID, UID, Reader string
	NDEFURITemplate       string
	ApplicationKey        []byte
	KeyNumber             byte
}
type PCSCLocker struct {
	helper, limiter string
	bindings        map[string]PCSCBinding
	slots           chan struct{}
	mu              sync.RWMutex
}

func NewPCSCLocker(helper, limiter string, bindings []PCSCBinding) (*PCSCLocker, error) {
	if runtime.GOOS != "linux" || len(bindings) < 1 || len(bindings) > 100 {
		return nil, locations.ErrInvalid
	}
	for _, path := range []string{helper, limiter} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, locations.ErrInvalid
		}
		real, e := filepath.EvalSymlinks(path)
		if e != nil || real != path {
			return nil, locations.ErrInvalid
		}
		stat, e := os.Stat(path)
		if e != nil || !stat.Mode().IsRegular() || stat.Mode().Perm()&0111 == 0 || stat.Mode().Perm()&0022 != 0 {
			return nil, locations.ErrInvalid
		}
	}
	out := &PCSCLocker{helper: helper, limiter: limiter, bindings: map[string]PCSCBinding{}, slots: make(chan struct{}, 1)}
	success := false
	defer func() {
		if !success {
			out.Close()
		}
	}()
	seenUID := map[string]bool{}
	for _, b := range bindings {
		uid, e := locations.NormalizeUID(b.UID)
		if e != nil || len(uid) != 14 || !authority.ValidID(b.RecordID) || b.RecordID != strings.ToLower(b.RecordID) || len(b.ApplicationKey) != 16 || bytes.Equal(b.ApplicationKey, make([]byte, 16)) || b.KeyNumber > 4 || len(b.Reader) < 1 || len(b.Reader) > 127 || strings.IndexFunc(b.Reader, func(c rune) bool { return c < 32 || c == 127 }) >= 0 {
			return nil, locations.ErrInvalid
		}
		if _, e = canonicalNDEFTemplate(b.NDEFURITemplate, b.RecordID); e != nil {
			return nil, e
		}
		if _, ok := out.bindings[b.RecordID]; ok {
			return nil, locations.ErrInvalid
		}
		if seenUID[uid] {
			out.Close()
			return nil, locations.ErrInvalid
		}
		seenUID[uid] = true
		b.UID = uid
		b.ApplicationKey = bytes.Clone(b.ApplicationKey)
		out.bindings[b.RecordID] = b
	}
	success = true
	return out, nil
}
func (p *PCSCLocker) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, b := range p.bindings {
		clear(b.ApplicationKey)
		delete(p.bindings, id)
	}
}
func (p *PCSCLocker) command(ctx context.Context, mode, reader string) *exec.Cmd {
	c := exec.CommandContext(ctx, p.limiter, "--as=67108864", "--cpu=2", "--", p.helper, mode, reader)
	c.Env = []string{"LC_ALL=C"}
	c.Stderr = io.Discard
	c.WaitDelay = time.Second
	return c
}
func (p *PCSCLocker) Ready(ctx context.Context) error {
	if p == nil {
		return locations.ErrUnavailable
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return locations.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	readers := map[string]bool{}
	p.mu.RLock()
	for _, b := range p.bindings {
		readers[b.Reader] = true
	}
	p.mu.RUnlock()
	if len(readers) == 0 {
		return locations.ErrUnavailable
	}
	for reader := range readers {
		cmd := p.command(ctx, "ready", reader)
		var output smallPCSCOutput
		cmd.Stdout = &output
		if cmd.Run() != nil || output.String() != "ready\n" {
			return locations.ErrUnavailable
		}
	}
	return nil
}

type smallPCSCOutput struct{ b bytes.Buffer }

func (o *smallPCSCOutput) Write(p []byte) (int, error) {
	if o.b.Len()+len(p) > 1024 {
		return 0, locations.ErrUnavailable
	}
	return o.b.Write(p)
}
func (o *smallPCSCOutput) String() string { return o.b.String() }

func (p *PCSCLocker) Lock(ctx context.Context, req locations.NFCLockRequest) (locations.NFCLockProof, error) {
	var proof locations.NFCLockProof
	if p == nil {
		return proof, locations.ErrUnavailable
	}
	p.mu.RLock()
	binding, ok := p.bindings[req.RecordID]
	binding.ApplicationKey = bytes.Clone(binding.ApplicationKey)
	p.mu.RUnlock()
	defer clear(binding.ApplicationKey)
	if !ok || binding.UID != req.UID {
		return proof, locations.ErrUnavailable
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		return proof, locations.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := p.command(ctx, "exchange", binding.Reader)
	input, e := cmd.StdinPipe()
	if e != nil {
		return proof, e
	}
	output, e := cmd.StdoutPipe()
	if e != nil {
		input.Close()
		return proof, e
	}
	if e = cmd.Start(); e != nil {
		input.Close()
		return proof, e
	}
	waited := false
	defer func() {
		input.Close()
		cancel()
		if !waited {
			_ = cmd.Wait()
		}
	}()
	scanner := bufio.NewScanner(io.LimitReader(output, 16*518))
	scanner.Buffer(make([]byte, 518), 518)
	transcript := sha256.New()
	exchanges := 0
	exchange := func(apdu []byte) ([]byte, error) {
		if len(apdu) < 5 || len(apdu) > 261 || exchanges >= 16 {
			return nil, locations.ErrInvalid
		}
		exchanges++
		transcript.Write(apdu)
		if _, e := io.WriteString(input, hex.EncodeToString(apdu)+"\n"); e != nil {
			return nil, locations.ErrUnavailable
		}
		if !scanner.Scan() {
			return nil, locations.ErrUnavailable
		}
		value, e := hex.DecodeString(scanner.Text())
		if e != nil || len(value) < 2 || len(value) > 258 {
			return nil, locations.ErrUnavailable
		}
		transcript.Write(value)
		return value, nil
	}
	proof, e = lockNTAG424(req, binding, exchange, rand.Reader)
	if e != nil {
		return locations.NFCLockProof{}, e
	}
	proof.TranscriptSHA256 = hex.EncodeToString(transcript.Sum(nil))
	input.Close()
	e = cmd.Wait()
	waited = true
	if e != nil {
		return locations.NFCLockProof{}, locations.ErrUnavailable
	}
	return proof, nil
}

type apduExchange func([]byte) ([]byte, error)

func plainAPDU(x apduExchange, apdu []byte, status byte) ([]byte, error) {
	v, e := x(apdu)
	if e != nil {
		return nil, e
	}
	if len(v) < 2 || v[len(v)-1] != status || v[len(v)-2] != 0x91 {
		return nil, locations.ErrUnavailable
	}
	return v[:len(v)-2], nil
}
func isoAPDU(x apduExchange, apdu []byte) ([]byte, error) {
	v, e := x(apdu)
	if e != nil {
		return nil, e
	}
	if len(v) < 2 || v[len(v)-2] != 0x90 || v[len(v)-1] != 0 {
		return nil, locations.ErrUnavailable
	}
	return v[:len(v)-2], nil
}
func wrappedAPDU(cmd byte, data []byte) []byte {
	if len(data) == 0 {
		return []byte{0x90, cmd, 0, 0, 0}
	}
	out := []byte{0x90, cmd, 0, 0, byte(len(data))}
	out = append(out, data...)
	return append(out, 0)
}
func cbc(key, iv, input []byte, decrypt bool) ([]byte, error) {
	if len(iv) != 16 || len(input) == 0 || len(input)%16 != 0 {
		return nil, locations.ErrInvalid
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	out := make([]byte, len(input))
	if decrypt {
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, input)
	} else {
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, input)
	}
	return out, nil
}
func rotate(v []byte) []byte { return append(append([]byte{}, v[1:]...), v[0]) }
func ev2Keys(key, a, b []byte) (enc, mac []byte, e error) {
	if len(key) != 16 || len(a) != 16 || len(b) != 16 {
		return nil, nil, locations.ErrInvalid
	}
	sv := []byte{0xa5, 0x5a, 0, 1, 0, 0x80}
	sv = append(sv, a[:2]...)
	for i := 0; i < 6; i++ {
		sv = append(sv, a[i+2]^b[i])
	}
	sv = append(sv, b[6:]...)
	sv = append(sv, a[8:]...)
	enc, e = aesCMAC(key, sv)
	if e != nil {
		return
	}
	sv[0], sv[1] = 0x5a, 0xa5
	mac, e = aesCMAC(key, sv)
	return
}

type ev2Session struct {
	enc, mac, ti []byte
	counter      uint16
	exchange     apduExchange
}

func authenticateEV2(x apduExchange, b PCSCBinding, random io.Reader) (*ev2Session, error) {
	first, e := plainAPDU(x, wrappedAPDU(0x71, []byte{b.KeyNumber, 0}), 0xaf)
	if e != nil || len(first) != 16 {
		return nil, locations.ErrUnavailable
	}
	rb, e := cbc(b.ApplicationKey, make([]byte, 16), first, true)
	if e != nil {
		return nil, e
	}
	defer clear(rb)
	ra := make([]byte, 16)
	if _, e = io.ReadFull(random, ra); e != nil {
		return nil, e
	}
	defer clear(ra)
	data, e := cbc(b.ApplicationKey, make([]byte, 16), append(bytes.Clone(ra), rotate(rb)...), false)
	if e != nil {
		return nil, e
	}
	response, e := plainAPDU(x, wrappedAPDU(0xaf, data), 0)
	if e != nil || len(response) != 32 {
		return nil, locations.ErrUnavailable
	}
	value, e := cbc(b.ApplicationKey, make([]byte, 16), response, true)
	if e != nil {
		return nil, e
	}
	defer clear(value)
	if subtle.ConstantTimeCompare(value[4:20], rotate(ra)) != 1 || !bytes.Equal(value[26:32], make([]byte, 6)) {
		return nil, locations.ErrUnavailable
	}
	enc, mac, e := ev2Keys(b.ApplicationKey, ra, rb)
	if e != nil {
		return nil, e
	}
	return &ev2Session{enc: enc, mac: mac, ti: bytes.Clone(value[:4]), exchange: x}, nil
}
func truncatedCMAC(key, data []byte) []byte {
	v, e := aesCMAC(key, data)
	if e != nil {
		return nil
	}
	out := make([]byte, 8)
	for i := range out {
		out[i] = v[2*i+1]
	}
	return out
}
func (s *ev2Session) iv(response bool) []byte {
	v := make([]byte, 16)
	v[0], v[1] = 0xa5, 0x5a
	if response {
		v[0], v[1] = 0x5a, 0xa5
	}
	copy(v[2:6], s.ti)
	v[6], v[7] = byte(s.counter), byte(s.counter>>8)
	b, _ := aes.NewCipher(s.enc)
	b.Encrypt(v, v)
	return v
}

// NT4H2421Gx 9.1: CmdCtr is LSB-first, independent command/response IVs,
// mandatory 80/00 padding, response MAC covers ciphertext before decryption.
func (s *ev2Session) command(cmd byte, header, data []byte, full bool) ([]byte, error) {
	if s.counter == 65535 {
		return nil, locations.ErrUnavailable
	}
	if full && len(data) > 0 {
		p := append(bytes.Clone(data), 0x80)
		for len(p)%16 != 0 {
			p = append(p, 0)
		}
		var e error
		data, e = cbc(s.enc, s.iv(false), p, false)
		if e != nil {
			return nil, e
		}
	}
	signed := []byte{cmd, byte(s.counter), byte(s.counter >> 8)}
	signed = append(signed, s.ti...)
	signed = append(signed, header...)
	signed = append(signed, data...)
	payload := append(bytes.Clone(header), data...)
	payload = append(payload, truncatedCMAC(s.mac, signed)...)
	response, e := plainAPDU(s.exchange, wrappedAPDU(cmd, payload), 0)
	if e != nil || len(response) < 8 {
		return nil, locations.ErrUnavailable
	}
	s.counter++
	body := response[:len(response)-8]
	signed = []byte{0, byte(s.counter), byte(s.counter >> 8)}
	signed = append(signed, s.ti...)
	signed = append(signed, body...)
	if subtle.ConstantTimeCompare(response[len(response)-8:], truncatedCMAC(s.mac, signed)) != 1 {
		return nil, locations.ErrUnavailable
	}
	if full && len(body) > 0 {
		value, e := cbc(s.enc, s.iv(true), body, true)
		if e != nil {
			return nil, e
		}
		n := len(value) - 1
		for n >= 0 && value[n] == 0 {
			n--
		}
		if n < 0 || value[n] != 0x80 {
			return nil, locations.ErrUnavailable
		}
		body = value[:n]
	}
	return body, nil
}
func ntagSettings(v []byte, profile string) (picc, input, enc, mac int, e error) {
	e = locations.ErrUnavailable
	if len(v) < 19 || v[0] != 0 || v[1] != 0x40 || v[3]>>4 != 0xe || !bytes.Equal(v[4:7], []byte{0, 1, 0}) || v[8]>>4 != 0xf || v[9]>>4 > 4 || v[9]&15 > 4 {
		return
	}
	opt := byte(0xc1)
	if profile == "encrypted-file" {
		opt = 0xd1
	} else if profile != "empty" {
		return
	}
	if v[7] != opt {
		return
	}
	at := 10
	offset := func() (int, bool) {
		if at+3 > len(v) {
			return 0, false
		}
		n := int(v[at]) | int(v[at+1])<<8 | int(v[at+2])<<16
		at += 3
		return n, n <= 256
	}
	var ok bool
	picc, ok = offset()
	if !ok || picc+32 > 256 {
		return
	}
	input, ok = offset()
	if !ok {
		return
	}
	if profile == "encrypted-file" {
		enc, ok = offset()
		if !ok || enc+32 > 256 {
			return
		}
		n, yes := offset()
		if !yes || n != 32 {
			return
		}
	}
	mac, ok = offset()
	if !ok || mac+16 > 256 || at != len(v) {
		return
	}
	if profile == "empty" && input != mac || profile == "encrypted-file" && (input != enc || mac != enc+38) {
		return
	}
	if !(picc+32 <= input || mac+16 <= picc) {
		return
	}
	e = nil
	return
}

// The owned physical provisioning contract uses one short, well-known URI
// record (TNF=1, type U, https:// prefix code 04). Dynamic values are zero-filled
// in configuration, so the exact static site/path/record and mirror positions
// can be compared before any irreversible command is sent to the card.
func canonicalNDEFTemplate(template, recordID string) (string, error) {
	u, e := url.Parse(template)
	if e != nil || len(template) > 257 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawPath != "" || strings.IndexFunc(template, func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
		return "", locations.ErrInvalid
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil || len(q["record_id"]) != 1 || q.Get("record_id") != recordID || !authority.ValidID(recordID) || recordID != strings.ToLower(recordID) {
		return "", locations.ErrInvalid
	}
	profile := "empty"
	if _, ok := q["enc"]; ok {
		profile = "encrypted-file"
	}
	u.RawQuery = ""
	base := u.String()
	want := base + "?record_id=" + recordID + "&picc=" + strings.Repeat("0", 32)
	if profile == "encrypted-file" {
		want += "&enc=" + strings.Repeat("0", 32)
	}
	want += "&cmac=" + strings.Repeat("0", 16)
	// The 256-byte Type 4 file reserves two bytes for NLEN and five for the
	// short URI record header/type/prefix; URI compression removes eight bytes.
	if template != want || len(template)-8+5 > 254 {
		return "", locations.ErrInvalid
	}
	return profile, nil
}

func boundNDEFSUN(file []byte, b PCSCBinding, profile string, picc, input, enc, mac int) (locations.SUNRequest, error) {
	var out locations.SUNRequest
	expectedProfile, e := canonicalNDEFTemplate(b.NDEFURITemplate, b.RecordID)
	if e != nil || expectedProfile != profile || len(file) != 256 {
		return out, locations.ErrInvalid
	}
	n := int(file[0])<<8 | int(file[1])
	if n < 5 || n > 254 || file[2] != 0xd1 || file[3] != 1 || int(file[4])+4 != n || file[5] != 'U' || file[6] != 4 {
		return out, locations.ErrUnavailable
	}
	uri := "https://" + string(file[7:2+n])
	if len(uri) != len(b.NDEFURITemplate) {
		return out, locations.ErrUnavailable
	}
	masked := []byte(uri)
	values := map[string]string{}
	for _, mirror := range []struct {
		name         string
		offset, size int
	}{{"picc", picc, 32}, {"cmac", mac, 16}, {"enc", enc, 32}} {
		if mirror.name == "enc" && profile == "empty" {
			continue
		}
		at := strings.Index(b.NDEFURITemplate, "&"+mirror.name+"=")
		if at < 0 {
			return out, locations.ErrUnavailable
		}
		at += len(mirror.name) + 2
		if mirror.offset != at-8+7 || at+mirror.size > len(masked) {
			return out, locations.ErrUnavailable
		}
		value := uri[at : at+mirror.size]
		if decoded, err := hex.DecodeString(value); err != nil || len(decoded)*2 != mirror.size {
			return out, locations.ErrUnavailable
		}
		values[mirror.name] = value
		copy(masked[at:at+mirror.size], strings.Repeat("0", mirror.size))
	}
	if string(masked) != b.NDEFURITemplate || profile == "empty" && input != mac || profile == "encrypted-file" && input != enc {
		return out, locations.ErrUnavailable
	}
	parsed, e := url.Parse(uri)
	if e != nil {
		return out, locations.ErrUnavailable
	}
	// RecordID comes from the authenticated card's actual static URI, never
	// from the requested database record pasted onto an unrelated SUN proof.
	out.RecordID = parsed.Query().Get("record_id")
	if out.RecordID != b.RecordID {
		return locations.SUNRequest{}, locations.ErrUnavailable
	}
	out.PICCData, out.CMAC, out.Encrypted = values["picc"], values["cmac"], values["enc"]
	return out, nil
}

func lockNTAG424(req locations.NFCLockRequest, b PCSCBinding, x apduExchange, random io.Reader) (locations.NFCLockProof, error) {
	var proof locations.NFCLockProof
	if req.RecordID != b.RecordID || req.UID != b.UID || len(req.MetaKey) != 16 || len(req.FileKey) != 16 {
		return proof, locations.ErrInvalid
	}
	if _, e := isoAPDU(x, []byte{0, 0xa4, 4, 0x0c, 7, 0xd2, 0x76, 0, 0, 0x85, 1, 1, 0}); e != nil {
		return proof, e
	}
	before, e := plainAPDU(x, wrappedAPDU(0xf5, []byte{2}), 0)
	if e != nil {
		return proof, e
	}
	picc, input, enc, mac, e := ntagSettings(before, req.MACProfile)
	if e != nil {
		return proof, e
	}
	if _, e = isoAPDU(x, []byte{0, 0xa4, 0, 0x0c, 2, 0xe1, 4, 0}); e != nil {
		return proof, e
	}
	file, e := isoAPDU(x, []byte{0, 0xb0, 0, 0, 0})
	if e != nil || len(file) != 256 {
		return proof, locations.ErrUnavailable
	}
	sun, e := boundNDEFSUN(file, b, req.MACProfile, picc, input, enc, mac)
	if e != nil {
		return proof, e
	}
	counter, e := verifySUN(req.MetaKey, req.FileKey, req.UID, sun.PICCData, sun.Encrypted, sun.CMAC, req.MACProfile)
	if e != nil || counter <= req.MinimumCounter {
		return proof, locations.ErrConflict
	}
	session, e := authenticateEV2(x, b, random)
	if e != nil {
		return proof, e
	}
	defer clear(session.enc)
	defer clear(session.mac)
	uid, e := session.command(0x51, nil, nil, true)
	if e != nil || !strings.EqualFold(hex.EncodeToString(uid), req.UID) {
		return proof, locations.ErrUnavailable
	}
	settings, e := session.command(0xf5, []byte{2}, nil, false)
	if e != nil || !bytes.Equal(settings, before) {
		return proof, locations.ErrUnavailable
	}
	// Preserve all SDM keys, offsets, communication and read settings. F disables
	// both write paths and future changes; an authenticated readback is the proof.
	expected := bytes.Clone(settings)
	expected[2] = 0xff
	expected[3] = 0xef
	if !bytes.Equal(settings, expected) {
		payload := append(bytes.Clone(expected[1:4]), expected[7:]...)
		if _, e = session.command(0x5f, []byte{2}, payload, true); e != nil {
			return proof, e
		}
	}
	final, e := session.command(0xf5, []byte{2}, nil, false)
	if e != nil || !bytes.Equal(final, expected) {
		return proof, locations.ErrUnavailable
	}
	proof.SUN = sun
	return proof, nil
}

var _ locations.NFCLocker = (*PCSCLocker)(nil)
