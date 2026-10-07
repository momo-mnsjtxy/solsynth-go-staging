package board

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

const owner = "00000000-0000-0000-0000-000000000001"
const app = "00000000-0000-0000-0000-000000000002"
const itemID = "00000000-0000-0000-0000-000000000003"

var failure = errors.New("synthetic failure")

type fakeRepo struct {
	items                        []Item
	replaceErr, saveErr, listErr error
	replacements, saves          int
	calls                        *[]string
}

func (r *fakeRepo) List(ctx context.Context, a string) ([]Item, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return copyItems(r.items), ctx.Err()
}
func (r *fakeRepo) Replace(ctx context.Context, a string, items []Item) error {
	r.replacements++
	if r.calls != nil {
		*r.calls = append(*r.calls, "replace")
	}
	if r.replaceErr != nil {
		return r.replaceErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.items = copyItems(items)
	return nil
}
func (r *fakeRepo) FindByID(ctx context.Context, a, id string) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	for _, i := range r.items {
		if i.AccountID == a && i.ID == id {
			return copyItems([]Item{i})[0], nil
		}
	}
	return Item{}, ErrNotFound
}
func (r *fakeRepo) FindFirstWidget(ctx context.Context, a, app, key string) (Item, error) {
	if err := ctx.Err(); err != nil {
		return Item{}, err
	}
	for _, i := range r.items {
		if i.AccountID == a && i.Kind == CustomApp && i.CustomAppID != nil && *i.CustomAppID == app && i.CustomAppWidgetKey != nil && strings.EqualFold(*i.CustomAppWidgetKey, key) {
			return copyItems([]Item{i})[0], nil
		}
	}
	return Item{}, ErrNotFound
}
func (r *fakeRepo) Save(ctx context.Context, item Item) error {
	r.saves++
	if r.calls != nil {
		*r.calls = append(*r.calls, "save")
	}
	if r.saveErr != nil {
		return r.saveErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for i := range r.items {
		if r.items[i].ID == item.ID {
			r.items[i] = copyItems([]Item{item})[0]
			return nil
		}
	}
	return ErrNotFound
}

type fakeCache struct {
	groups []string
	err    error
	calls  *[]string
}

func (c *fakeCache) InvalidateGroup(ctx context.Context, g string) error {
	c.groups = append(c.groups, g)
	if c.calls != nil {
		*c.calls = append(*c.calls, "cache")
	}
	return c.err
}

type fakeCatalog struct {
	widget               *Widget
	err                  error
	result               Payload
	lookups, validations int
	cancel               context.CancelFunc
}

func (c *fakeCatalog) Lookup(ctx context.Context, a, k string) (*Widget, error) {
	c.lookups++
	if c.cancel != nil {
		c.cancel()
	}
	return c.widget, c.err
}
func (c *fakeCatalog) Validate(ctx context.Context, a, k string, p Payload) (Payload, error) {
	c.validations++
	return c.result, c.err
}

type fakeNormalizer struct {
	result Payload
	err    error
	calls  int
}

func (n *fakeNormalizer) Normalize(context.Context, Payload) (Payload, error) {
	n.calls++
	return n.result, n.err
}
func custom() Item {
	return Item{ID: itemID, AccountID: owner, Kind: CustomApp, CustomAppID: ptr(app), CustomAppWidgetKey: ptr("weather"), IsEnabled: true, Payload: Payload{"value": "old"}}
}
func setup(items ...Item) (Service, *fakeRepo, *fakeCache, *fakeCatalog) {
	r := &fakeRepo{items: items}
	c := &fakeCache{}
	catalog := &fakeCatalog{widget: &Widget{Key: "weather", Enabled: true, AllowMultiple: true}, result: Payload{"normalized": true}}
	return Service{Repository: r, Cache: c, Catalog: catalog, NewID: func() (string, error) { return itemID, nil }}, r, c, catalog
}

func TestJSONClientContract(t *testing.T) {
	i := custom()
	i.Order = 2
	raw, err := json.Marshal(i)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"id": itemID, "order": float64(2), "kind": float64(1), "custom_app_id": app, "custom_app_widget_key": "weather", "is_enabled": true, "payload": map[string]any{"value": "old"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s", raw)
	}
	if Prebuilt != 0 || CustomApp != 1 {
		t.Fatal("enum wire values")
	}
}

func TestReplacePreservesPayloadAndCanonicalizes(t *testing.T) {
	old := custom()
	old.Payload = Payload{"nested": map[string]any{"value": "server"}}
	s, r, c, cat := setup(old)
	calls := []string{}
	r.calls = &calls
	c.calls = &calls
	in := custom()
	in.CustomAppWidgetKey = ptr("WEATHER")
	in.Payload = Payload{"overwrite": true}
	in.WidgetKey = ptr("discard")
	in.AccountID = "wrong"
	got, err := s.Replace(context.Background(), owner, []Item{in})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got[0].Payload, old.Payload) || got[0].WidgetKey != nil || *got[0].CustomAppWidgetKey != "weather" || got[0].AccountID != owner {
		t.Fatalf("%+v", got)
	}
	if !reflect.DeepEqual(calls, []string{"replace", "cache"}) || !reflect.DeepEqual(c.groups, []string{"account:" + owner}) || cat.lookups != 1 {
		t.Fatal(calls, c.groups)
	}
	if !reflect.DeepEqual(in.Payload, Payload{"overwrite": true}) || *in.CustomAppWidgetKey != "WEATHER" {
		t.Fatal("mutated caller input")
	}
	got[0].Payload["nested"].(map[string]any)["value"] = "changed"
	if r.items[0].Payload["nested"].(map[string]any)["value"] != "server" {
		t.Fatal("aliased persistence")
	}
}

func TestReplaceMatchesWidgetThenConsumesPayload(t *testing.T) {
	old := custom()
	s, _, _, _ := setup(old)
	a, b := custom(), custom()
	a.ID = "new1"
	b.ID = "new2"
	b.Order = 1
	got, err := s.Replace(context.Background(), owner, []Item{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Payload["value"] != "old" || len(got[1].Payload) != 0 {
		t.Fatal(got)
	}
}

func TestReplaceIDMatchRemovesFallbackCandidate(t *testing.T) {
	s, _, _, _ := setup(custom())
	a, b := custom(), custom()
	b.ID = "new"
	b.Order = 1
	got, err := s.Replace(context.Background(), owner, []Item{a, b})
	if err != nil || got[0].Payload["value"] != "old" || len(got[1].Payload) != 0 {
		t.Fatal(got, err)
	}
}

func TestReplacePrebuiltClearsCustomFieldsAndSorts(t *testing.T) {
	s, r, _, _ := setup()
	a := custom()
	a.Kind = Prebuilt
	a.ID = ""
	a.Order = 2
	a.WidgetKey = ptr("activity")
	got, err := s.Replace(context.Background(), owner, []Item{a, {ID: "earlier", Order: -1, Kind: Prebuilt}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Order != -1 || got[1].ID != itemID || got[1].CustomAppID != nil || got[1].CustomAppWidgetKey != nil || *got[1].WidgetKey != "activity" {
		t.Fatal(got)
	}
	if r.replacements != 1 {
		t.Fatal(r.replacements)
	}
}

func TestReplaceRejectsBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		input  []Item
		change func(*Service, *fakeCatalog)
	}{
		{"duplicate orders", []Item{{Kind: Prebuilt}, {Kind: Prebuilt}}, nil},
		{"unknown kind", []Item{{Kind: 9}}, nil},
		{"missing app", []Item{{Kind: CustomApp, CustomAppWidgetKey: ptr("weather")}}, nil},
		{"missing key", []Item{{Kind: CustomApp, CustomAppID: ptr(app)}}, nil},
		{"blank key", []Item{{Kind: CustomApp, CustomAppID: ptr(app), CustomAppWidgetKey: ptr(" ")}}, nil},
		{"missing catalog", []Item{custom()}, func(s *Service, c *fakeCatalog) { s.Catalog = nil }},
		{"missing widget", []Item{custom()}, func(s *Service, c *fakeCatalog) { c.widget = nil }},
		{"disabled widget", []Item{custom()}, func(s *Service, c *fakeCatalog) { c.widget.Enabled = false }},
		{"catalog failure", []Item{custom()}, func(s *Service, c *fakeCatalog) { c.err = failure }},
		{"singleton", func() []Item { a, b := custom(), custom(); b.Order = 1; return []Item{a, b} }(), func(s *Service, c *fakeCatalog) { c.widget.AllowMultiple = false }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, r, c, cat := setup(custom())
			if tc.change != nil {
				tc.change(&s, cat)
			}
			_, err := s.Replace(context.Background(), owner, tc.input)
			if err == nil || r.replacements != 0 || len(c.groups) != 0 {
				t.Fatal(err, r.replacements, c.groups)
			}
		})
	}
}

func TestAtomicReplaceErrorAndPostCommitCacheError(t *testing.T) {
	s, r, c, _ := setup(custom())
	r.replaceErr = failure
	_, err := s.Replace(context.Background(), owner, nil)
	if err != failure || len(r.items) != 1 || len(c.groups) != 0 {
		t.Fatal(err, r.items, c.groups)
	}
	r.replaceErr = nil
	c.err = failure
	_, err = s.Replace(context.Background(), owner, nil)
	if err != failure || len(r.items) != 0 || len(c.groups) != 1 {
		t.Fatal("cache failure incorrectly treated as rollback")
	}
}

func TestMissingAdaptersFailClosed(t *testing.T) {
	s, r, _, _ := setup(custom())
	s.Cache = nil
	if _, err := s.Replace(context.Background(), owner, nil); !errors.Is(err, ErrDependency) || r.replacements != 0 {
		t.Fatal(err)
	}
	s, _, _, _ = setup(custom())
	if _, err := s.UpdateAppPayload(context.Background(), owner, itemID, app, "weather", nil); !errors.Is(err, ErrDependency) {
		t.Fatal(err)
	}
	if _, err := s.UpdateAdminPayload(context.Background(), owner, itemID, Payload{}); err != nil {
		t.Fatal(err)
	} // custom delegates catalog
	s.Catalog = nil
	if _, err := s.UpdateAdminPayload(context.Background(), owner, itemID, Payload{}); !errors.Is(err, ErrDependency) {
		t.Fatal(err)
	}
	if _, err := (Service{}).List(context.Background(), owner); !errors.Is(err, ErrDependency) {
		t.Fatal(err)
	}
}

func TestCancellationPreventsMutation(t *testing.T) {
	s, r, c, cat := setup(custom())
	ctx, cancel := context.WithCancel(context.Background())
	cat.cancel = cancel
	_, err := s.Replace(ctx, owner, []Item{custom()})
	if !errors.Is(err, context.Canceled) || r.replacements != 0 || len(c.groups) != 0 {
		t.Fatal(err)
	}
	if _, err = s.List(ctx, owner); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = s.UpdateAppPayload(ctx, owner, itemID, app, "weather", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = s.UpdateAdminPayload(ctx, owner, itemID, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAppPayloadOwnershipAndNormalization(t *testing.T) {
	for _, id := range []string{itemID, "", zeroID} {
		s, r, c, _ := setup(custom())
		n := &fakeNormalizer{result: Payload{"normalized": "yes"}}
		s.Normalizer = n
		got, err := s.UpdateAppPayload(context.Background(), owner, id, app, "WEATHER", Payload{"raw": true})
		if err != nil || got.Payload["normalized"] != "yes" || n.calls != 1 || r.saves != 1 || len(c.groups) != 1 {
			t.Fatal(got, err)
		}
	}
	for _, mutate := range []func(*Item){func(i *Item) { i.Kind = Prebuilt }, func(i *Item) { i.CustomAppID = ptr("other") }, func(i *Item) { i.CustomAppWidgetKey = ptr("other") }} {
		item := custom()
		mutate(&item)
		s, r, c, _ := setup(item)
		n := &fakeNormalizer{}
		s.Normalizer = n
		if _, err := s.UpdateAppPayload(context.Background(), owner, itemID, app, "weather", nil); err == nil || n.calls != 0 || r.saves != 0 || len(c.groups) != 0 {
			t.Fatal(err)
		}
	}
}

func TestPayloadErrorsNeverSave(t *testing.T) {
	s, r, c, _ := setup(custom())
	s.Normalizer = &fakeNormalizer{err: failure}
	if _, err := s.UpdateAppPayload(context.Background(), owner, itemID, app, "weather", nil); err != failure || r.saves != 0 || len(c.groups) != 0 {
		t.Fatal(err)
	}
	s.Normalizer = &fakeNormalizer{}
	r.saveErr = failure
	if _, err := s.UpdateAppPayload(context.Background(), owner, itemID, app, "weather", nil); err != failure || len(c.groups) != 0 {
		t.Fatal(err)
	}
	if _, err := s.UpdateAppPayload(context.Background(), "other", itemID, app, "weather", nil); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestAdminPayloadRoutesToCorrectValidator(t *testing.T) {
	for _, kind := range []Kind{Prebuilt, CustomApp} {
		item := custom()
		item.Kind = kind
		s, r, c, cat := setup(item)
		normalizer := &fakeNormalizer{result: Payload{"universal": true}}
		s.Normalizer = normalizer
		got, err := s.UpdateAdminPayload(context.Background(), owner, itemID, Payload{"raw": true})
		if err != nil || r.saves != 1 || len(c.groups) != 1 {
			t.Fatal(err)
		}
		if kind == Prebuilt && (normalizer.calls != 1 || cat.validations != 0 || got.Payload["universal"] != true) {
			t.Fatal(got)
		}
		if kind == CustomApp && (normalizer.calls != 0 || cat.validations != 1 || got.Payload["normalized"] != true) {
			t.Fatal(got)
		}
	}
}

func TestListAndIDGeneration(t *testing.T) {
	s, _, _, _ := setup(Item{Order: 5}, Item{Order: -1})
	got, err := s.List(context.Background(), owner)
	if err != nil || got[0].Order != -1 {
		t.Fatal(got, err)
	}
	s.NewID = nil
	got, err = s.Replace(context.Background(), owner, []Item{{Kind: Prebuilt}})
	if err != nil || len(got[0].ID) != 36 || got[0].ID[14] != '4' {
		t.Fatal(got, err)
	}
	s.NewID = func() (string, error) { return "", failure }
	if _, err = s.Replace(context.Background(), owner, []Item{{Kind: Prebuilt}}); err != failure {
		t.Fatal(err)
	}
}

func TestHistoricalSchemaContract(t *testing.T) {
	raw, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		HistoricalOnly bool
		Table          string
		Columns        map[string]struct {
			Type      string
			Nullable  bool
			MaxLength int
		}
		PrimaryKey []string
		Indexes    []struct {
			Columns []string
			Unique  bool
		}
		ClientKind map[string]int
	}
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if !schema.HistoricalOnly || schema.Table != "account_board_items" || len(schema.Columns) != 12 {
		t.Fatal(schema)
	}
	for _, name := range []string{"id", "account_id", "order", "kind", "is_enabled", "payload", "created_at", "updated_at"} {
		column, ok := schema.Columns[name]
		if !ok || column.Nullable {
			t.Fatalf("required column %q", name)
		}
	}
	for _, name := range []string{"widget_key", "custom_app_widget_key"} {
		column := schema.Columns[name]
		if !column.Nullable || column.MaxLength != 256 {
			t.Fatal(name, column)
		}
	}
	if !reflect.DeepEqual(schema.PrimaryKey, []string{"id"}) || !schema.Indexes[0].Unique || !reflect.DeepEqual(schema.Indexes[0].Columns, []string{"account_id", "order"}) {
		t.Fatal(schema)
	}
	if schema.ClientKind["prebuilt"] != int(Prebuilt) || schema.ClientKind["customApp"] != int(CustomApp) {
		t.Fatal(schema.ClientKind)
	}
}

func TestMissingMutationAdapters(t *testing.T) {
	ctx := context.Background()
	s, r, _, _ := setup(custom())
	for _, call := range []func(Service) error{
		func(s Service) error { _, err := s.Replace(ctx, owner, nil); return err },
		func(s Service) error {
			_, err := s.UpdateAppPayload(ctx, owner, itemID, app, "weather", nil)
			return err
		},
		func(s Service) error { _, err := s.UpdateAdminPayload(ctx, owner, itemID, nil); return err },
	} {
		missingRepo := s
		missingRepo.Repository = nil
		err := call(missingRepo)
		var dep *DependencyError
		if !errors.As(err, &dep) || dep.Adapter != "repository" || dep.Error() == "" {
			t.Fatal(err)
		}
		missingCache := s
		missingCache.Cache = nil
		if err := call(missingCache); !errors.Is(err, ErrDependency) {
			t.Fatal(err)
		}
	}
	r.items[0].Kind = Prebuilt
	if _, err := s.UpdateAdminPayload(ctx, owner, itemID, nil); !errors.Is(err, ErrDependency) || r.saves != 0 {
		t.Fatal(err)
	}
}

func TestAdminInvalidItemsAndValidatorFailure(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		kind       Kind
		missingApp bool
	}{{9, false}, {CustomApp, true}} {
		item := custom()
		item.Kind = tc.kind
		if tc.missingApp {
			item.CustomAppID = nil
		}
		s, r, c, _ := setup(item)
		if _, err := s.UpdateAdminPayload(ctx, owner, itemID, nil); err == nil || r.saves != 0 || len(c.groups) != 0 {
			t.Fatal(err)
		}
	}
	s, r, c, cat := setup(custom())
	cat.err = failure
	if _, err := s.UpdateAdminPayload(ctx, owner, itemID, nil); err != failure || r.saves != 0 || len(c.groups) != 0 {
		t.Fatal(err)
	}
	if _, err := s.UpdateAdminPayload(ctx, owner, "missing", nil); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	cat.err = nil
	c.err = failure
	if _, err := s.UpdateAdminPayload(ctx, owner, itemID, nil); err != failure || r.saves != 1 {
		t.Fatal(err)
	}
}

func TestRepositoryReadErrorsAndCancelledReplace(t *testing.T) {
	s, r, _, _ := setup()
	r.listErr = failure
	if _, err := s.List(context.Background(), owner); err != failure {
		t.Fatal(err)
	}
	if _, err := s.Replace(context.Background(), owner, nil); err != failure {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Replace(ctx, owner, nil); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestDetachedNestedJSON(t *testing.T) {
	in := Payload{"fields": []any{Payload{"value": "x"}}, "map": map[string]any{"value": "y"}}
	got := copyPayload(in)
	got["fields"].([]any)[0].(Payload)["value"] = "changed"
	got["map"].(map[string]any)["value"] = "changed"
	if in["fields"].([]any)[0].(Payload)["value"] != "x" || in["map"].(map[string]any)["value"] != "y" {
		t.Fatal("nested JSON aliased")
	}
}
