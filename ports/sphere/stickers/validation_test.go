// SPDX-License-Identifier: AGPL-3.0-only
package stickers

import "testing"

func TestStickerRenderingAndIdentifierLimits(t *testing.T) {
	p := Pack{Name: "A", Prefix: "Ab12"}
	if e := ValidatePack(p); e != nil {
		t.Fatal(e)
	}
	for _, prefix := range []string{"a+b", "a_b", "a b", "é", ""} {
		p.Prefix = prefix
		if e := ValidatePack(p); e == nil {
			t.Fatal(prefix)
		}
	}
	s := Sticker{Slug: "smile", Size: 3, Mode: 1}
	s.Image.ID = "10000000-0000-4000-8000-000000000001"
	if e := ValidateSticker(s); e != nil {
		t.Fatal(e)
	}
	s.Size = 4
	if e := ValidateSticker(s); e == nil {
		t.Fatal("unknown size")
	}
	id := "10000000-0000-4000-8000-000000000001"
	if e := ValidateOrder([]OrderItem{{ID: id}, {ID: id}}); e == nil {
		t.Fatal("duplicate reorder")
	}
}
