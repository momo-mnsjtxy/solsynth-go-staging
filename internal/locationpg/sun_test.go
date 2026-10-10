// SPDX-License-Identifier: AGPL-3.0-only
package locationpg

import (
	"encoding/hex"
	"errors"
	"solsynth.local/go-migration/ports/sphere/locations"
	"testing"
)

func TestAESCMACNISTVectors(t *testing.T) {
	decode := func(s string) []byte {
		b, e := hex.DecodeString(s)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	key := decode("2b7e151628aed2a6abf7158809cf4f3c")
	for _, v := range [][2]string{{"", "bb1d6929e95937287fa37d129b756746"}, {"6bc1bee22e409f96e93d7e117393172a", "070a16b46b4d4144f79bdd9dd04a287c"}, {"6bc1bee22e409f96e93d7e117393172aae2d8a571e03ac9c9eb76fac45af8e5130c81c46a35ce411", "dfa66747de9ae63030ca32611497c827"}} {
		got, e := aesCMAC(key, decode(v[0]))
		if e != nil || hex.EncodeToString(got) != v[1] {
			t.Fatal(v, hex.EncodeToString(got), e)
		}
	}
}
func TestSUNNXPAN12196Vector(t *testing.T) {
	key := make([]byte, 16)
	ctr, e := verifySUN(key, key, "04958CAA5C5E80", "FD91EC264309878BE6345CBE53BADF40", "CEE9A53E3E463EF1F459635736738962", "ECC1E7F6C6C73BF6", "encrypted-file")
	if e != nil || ctr != 8 {
		t.Fatal(ctr, e)
	}
	for _, v := range [][3]string{{"04958CAA5C5E81", "ECC1E7F6C6C73BF6", "CEE9A53E3E463EF1F459635736738962"}, {"04958CAA5C5E80", "ECC1E7F6C6C73BF7", "CEE9A53E3E463EF1F459635736738962"}, {"04958CAA5C5E80", "ECC1E7F6C6C73BF6", "CEE9A53E3E463EF1F459635736738963"}} {
		_, e = verifySUN(key, key, v[0], "FD91EC264309878BE6345CBE53BADF40", v[2], v[1], "encrypted-file")
		if !errors.Is(e, locations.ErrNotFound) {
			t.Fatal("accepted altered authenticated field", v, e)
		}
	}
	if _, e = verifySUN(key, key, "04958CAA5C5E80", "FD91EC264309878BE6345CBE53BADF40", "CEE9A53E3E463EF1F459635736738962", "", "encrypted-file"); !errors.Is(e, locations.ErrInvalid) {
		t.Fatal("optional MAC", e)
	}
}
