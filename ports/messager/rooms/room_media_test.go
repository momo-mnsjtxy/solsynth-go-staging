// SPDX-License-Identifier: AGPL-3.0-only
package rooms

import (
	"context"
	"errors"
	"testing"
)

type roomMediaFake struct {
	Repository
	patch  RoomPatch
	writes int
}

func (f *roomMediaFake) Create(_ context.Context, _ string, p RoomPatch) (Room, error) {
	f.patch = p
	f.writes++
	return Room{}, ValidateRoom(p, true, Policy{77, 10, 20, 30})
}
func (f *roomMediaFake) Update(_ context.Context, _, _ string, p RoomPatch) (Room, error) {
	f.patch = p
	f.writes++
	return Room{}, ValidateRoom(p, false, Policy{77, 10, 20, 30})
}
func TestRoomMediaHTTPPreservesNullAndExplicitRemoval(t *testing.T) {
	a := &fakeAuthority{}
	repo := &roomMediaFake{}
	h := (&Service{Repository: repo, Authority: a}).Handler()
	target := "/api/chat/" + testRoom
	for _, body := range []string{`{}`, `{"picture_id":null,"background_id":null}`} {
		w := call(h, "PATCH", target, body, "Bearer fixture")
		if w.Code != 200 || repo.patch.PictureID != nil || repo.patch.BackgroundID != nil || repo.patch.RemovePicture || repo.patch.RemoveBackground {
			t.Fatal(w, repo.patch)
		}
	}
	for _, slot := range []string{"picture", "background"} {
		w := call(h, "DELETE", target+"/media/"+slot, `{}`, "Bearer fixture")
		if w.Code != 204 || a.permission != "chat.update" || (slot == "picture") != repo.patch.RemovePicture || (slot == "background") != repo.patch.RemoveBackground {
			t.Fatal(w, repo.patch, a)
		}
	}
	w := call(h, "POST", "/api/chat", `{"name":"room","picture_id":"`+testActor+`","background_id":"`+testRoom+`"}`, "Bearer fixture")
	if w.Code != 200 || repo.patch.PictureID == nil || *repo.patch.PictureID != testActor || a.permission != "chat.create" {
		t.Fatal(w, repo.patch)
	}
	before := repo.writes
	for _, body := range []string{`{"picture_id":null,"picture_id":null}`, `{"PictureId":null}`, `{"picture_id":12}`, `{"remove_picture":true}`} {
		if w := call(h, "PATCH", target, body, "Bearer fixture"); w.Code != 400 || repo.writes != before {
			t.Fatal(body, w, repo.writes)
		}
	}
	for _, id := range []string{"", "https://example.invalid/image.png", "00000000-0000-0000-0000-000000000000"} {
		if !errors.Is(ValidateRoom(RoomPatch{PictureID: &id}, false, Policy{}), ErrInvalid) {
			t.Fatal(id)
		}
	}
	if w := call(h, "DELETE", target+"/media/picture", `{"picture_id":null}`, "Bearer fixture"); w.Code != 501 {
		t.Fatal(w)
	}
	a.denied = ErrForbidden
	if w := call(h, "DELETE", target+"/media/picture", ``, "Bearer fixture"); w.Code != 403 {
		t.Fatal(w)
	}
}
