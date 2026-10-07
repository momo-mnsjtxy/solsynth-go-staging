package boardpg

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"solsynth.local/go-migration/ports/passport/board"
)

func TestPolicyDefaultFailsClosed(t *testing.T) {
	r := New(nil)
	ctx := context.Background()
	id := "00000000-0000-0000-0000-000000000001"
	if _, err := r.List(ctx, id); !errors.Is(err, ErrPolicyUnverified) {
		t.Fatal(err)
	}
	if err := r.Replace(ctx, id, nil); !errors.Is(err, ErrPolicyUnverified) {
		t.Fatal(err)
	}
	if err := r.Save(ctx, board.Item{AccountID: id}); !errors.Is(err, ErrPolicyUnverified) {
		t.Fatal(err)
	}
	called := false
	if err := r.WithinAccount(ctx, id, func(board.Repository) error { called = true; return nil }); !errors.Is(err, ErrPolicyUnverified) || called {
		t.Fatal(err)
	}
}
func TestDecodeJSONFidelity(t *testing.T) {
	raw := []byte(`{"integer":9007199254740993,"null":null,"array":[null,{"value":123456789012345678901234567890}],"empty":{},"empty_array":[]}`)
	got, err := decodePayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got["integer"] != json.Number("9007199254740993") {
		t.Fatalf("%#v", got)
	}
	if got["array"].([]any)[1].(map[string]any)["value"] != json.Number("123456789012345678901234567890") {
		t.Fatal(got)
	}
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	again, err := decodePayload(out)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatal(string(out), err)
	}
	n, err := decodePayload([]byte("null"))
	if err != nil || n != nil {
		t.Fatal(n, err)
	}
	e, err := decodePayload([]byte("{}"))
	if err != nil || e == nil || len(e) != 0 {
		t.Fatal(e, err)
	}
	for _, invalid := range []string{"", "[]", "1", `"x"`, "{} {}"} {
		if _, err := decodePayload([]byte(invalid)); !errors.Is(err, ErrInvalidPayload) {
			t.Fatal(invalid, err)
		}
	}
}
func TestCanonicalUUIDAndNullPointers(t *testing.T) {
	for _, id := range []string{"", "1", "00000000-0000-0000-0000-00000000000G", "00000000_0000-0000-0000-000000000001"} {
		if validID(id) {
			t.Fatal(id)
		}
	}
	if !validID("00000000-0000-0000-0000-000000000001") {
		t.Fatal("canonical ID")
	}
	empty := ""
	if nullable(nil) != nil || nullable(&empty) != "" {
		t.Fatal("null/empty collapsed")
	}
}
func TestScopedTransactionRejectsOtherAccount(t *testing.T) {
	repo := &transactionRepository{account: "a"}
	ctx := context.Background()
	if _, err := repo.List(ctx, "b"); !errors.Is(err, ErrAccountMismatch) {
		t.Fatal(err)
	}
	if _, err := repo.FindByID(ctx, "b", "x"); !errors.Is(err, ErrAccountMismatch) {
		t.Fatal(err)
	}
	if _, err := repo.FindFirstWidget(ctx, "b", "x", "y"); !errors.Is(err, ErrAccountMismatch) {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, board.Item{AccountID: "b"}); !errors.Is(err, ErrAccountMismatch) {
		t.Fatal(err)
	}
	if err := repo.Replace(ctx, "b", nil); !errors.Is(err, ErrAccountMismatch) {
		t.Fatal(err)
	}
	if _, ok := any(repo).(board.AccountTransactionRepository); ok {
		t.Fatal("recursive transaction interface")
	}
}
