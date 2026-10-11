// SPDX-License-Identifier: AGPL-3.0-only
package pets

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var testNow = time.Date(2026, time.October, 11, 23, 50, 15, 0, time.UTC)

func commandID(s string) string {
	return "request-00000000-" + s
}

func adopt(t *testing.T) State {
	t.Helper()
	out, err := Apply(nil, Command{
		RequestID: commandID("adopt"),
		Kind:      "adopt",
		Name:      "Mochi",
		Species:   "cloudling",
	}, testNow)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	return out
}

func TestDefinitionsAreDeterministicAndFresh(t *testing.T) {
	a, b := Definitions(), Definitions()
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("catalog changed between calls: %#v != %#v", a, b)
	}
	if len(a.Species) != 3 || len(a.Items) != 4 || len(a.Tasks) != 3 {
		t.Fatalf("unexpected fixed catalog sizes: %#v", a)
	}
	a.Species[0].Name = "changed"
	a.Items[0].Price = 999
	a.Tasks[0].Coins = 999
	c := Definitions()
	if c.Species[0].Name == "changed" || c.Items[0].Price == 999 || c.Tasks[0].Coins == 999 {
		t.Fatal("caller mutated the authoritative catalog")
	}
	for _, item := range c.Items {
		if item.Price < 0 || item.AssetKey == "" {
			t.Fatalf("invalid item definition: %#v", item)
		}
	}
}

func TestValidateRejectsCallerControlledOrAmbiguousFields(t *testing.T) {
	valid := Command{RequestID: commandID("valid"), ExpectedVersion: 1, Kind: "feed"}
	cases := []Command{
		{},
		{RequestID: "too-short", Kind: "feed", ExpectedVersion: 1},
		{RequestID: "request-00000000-!", Kind: "feed", ExpectedVersion: 1},
		{RequestID: commandID("negative"), Kind: "feed", ExpectedVersion: -1},
		{RequestID: commandID("unknown"), Kind: "unknown", ExpectedVersion: 1},
		{RequestID: commandID("injected"), Kind: "feed", ExpectedVersion: 1, ItemID: "snack"},
		{RequestID: commandID("species"), Kind: "adopt", Name: "Mochi", Species: "unknown"},
		{RequestID: commandID("version"), Kind: "adopt", ExpectedVersion: 1, Name: "Mochi", Species: "cloudling"},
		{RequestID: commandID("space"), Kind: "adopt", Name: " Mochi", Species: "cloudling"},
		{RequestID: commandID("format"), Kind: "adopt", Name: "Mo\u200bchi", Species: "cloudling"},
		{RequestID: commandID("control"), Kind: "rename", ExpectedVersion: 1, Name: "Mo\nchi"},
		{RequestID: commandID("long"), Kind: "rename", ExpectedVersion: 1, Name: "123456789012345678901234567890123"},
		{RequestID: commandID("buy-slot"), Kind: "buy", ExpectedVersion: 1, ItemID: "snack", Slot: "head"},
		{RequestID: commandID("equip-slot"), Kind: "equip", ExpectedVersion: 1, ItemID: "leaf_hat", Slot: "neck"},
		{RequestID: commandID("claim"), Kind: "claim", ExpectedVersion: 1, TaskID: "made-up"},
	}
	for i, c := range cases {
		if err := Validate(c); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: got %v, want PET_INVALID", i, err)
		}
	}
	if err := Validate(valid); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
}

func TestAdoptionIsSingleAndInitializesBoundedState(t *testing.T) {
	s := adopt(t)
	if s.Version != 1 || s.Name != "Mochi" || s.Species != "cloudling" {
		t.Fatalf("unexpected identity: %#v", s)
	}
	if s.Stats != (Stats{80, 80, 80, 80}) || s.Coins != 20 || s.Level != 1 || s.Experience != 0 {
		t.Fatalf("unexpected initial progression: %#v", s)
	}
	if s.Inventory["snack"] != 5 || s.Inventory["soap"] != 5 || len(s.Equipment) != 0 {
		t.Fatalf("unexpected starter inventory: %#v %#v", s.Inventory, s.Equipment)
	}
	if s.Daily.Day != "2026-10-11" || s.Daily.Commands != 1 || s.Daily.RewardedCare != 0 {
		t.Fatalf("unexpected daily state: %#v", s.Daily)
	}
	if !s.AdoptedAt.Equal(testNow) || !s.SettledAt.Equal(testNow) || !s.UpdatedAt.Equal(testNow) {
		t.Fatalf("server clock was not used consistently: %#v", s)
	}
	_, err := Apply(&s, Command{RequestID: commandID("again"), Kind: "adopt", Name: "Other", Species: "foxlet"}, testNow)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second adoption got %v, want PET_CONFLICT", err)
	}
}

func TestSettlePreservesResidualClampsAndDoesNotMutateInput(t *testing.T) {
	s := adopt(t)
	s.Stats = Stats{100, 3, 2, 1}
	s.Inventory["synthetic"] = 2
	before := clone(s)

	first := Settle(s, testNow.Add(29*time.Minute+59*time.Second))
	if first.Stats != s.Stats || !first.SettledAt.Equal(testNow) {
		t.Fatalf("sub-tick time decayed state: %#v", first)
	}
	second := Settle(first, testNow.Add(30*time.Minute))
	if second.Stats != (Stats{98, 2, 1, 0}) || !second.SettledAt.Equal(testNow.Add(30*time.Minute)) {
		t.Fatalf("one decay tick mismatch: %#v", second)
	}
	again := Settle(second, testNow.Add(30*time.Minute))
	if !reflect.DeepEqual(again, second) {
		t.Fatalf("same clock instant charged decay twice:\nfirst %#v\nagain %#v", second, again)
	}
	longIdle := Settle(s, testNow.Add(50*365*24*time.Hour))
	if longIdle.Stats != (Stats{}) || longIdle.Name == "" || longIdle.Experience != s.Experience {
		t.Fatalf("idle settling deleted growth or escaped bounds: %#v", longIdle)
	}
	second.Inventory["synthetic"] = 99
	if !reflect.DeepEqual(s, before) {
		t.Fatalf("settling or returned maps mutated input:\n got %#v\nwant %#v", s, before)
	}
}

func TestSettleUsesUTCDateAndNeverRollsClockBackward(t *testing.T) {
	s := adopt(t)
	s.Daily.Care["feed"] = 1
	s.Daily.Claimed["breakfast"] = true
	s.Daily.RewardedCare = 1
	s.Daily.Commands = 10
	next := Settle(s, time.Date(2026, time.October, 12, 0, 0, 0, 0, time.FixedZone("plus-two", 2*60*60)))
	if next.Daily.Day != "2026-10-11" || next.Daily.Commands != 10 {
		t.Fatalf("local midnight incorrectly reset UTC daily state: %#v", next.Daily)
	}
	next = Settle(s, time.Date(2026, time.October, 12, 0, 0, 0, 0, time.UTC))
	if next.Daily.Day != "2026-10-12" || next.Daily.Commands != 0 || next.Daily.RewardedCare != 0 || len(next.Daily.Care) != 0 || len(next.Daily.Claimed) != 0 {
		t.Fatalf("UTC midnight failed to reset daily state: %#v", next.Daily)
	}
	backward := Settle(s, testNow.Add(-time.Hour))
	if !backward.UpdatedAt.Equal(s.UpdatedAt) || backward.SettledAt.Before(s.SettledAt) {
		t.Fatalf("clock rollback moved state backward: %#v", backward)
	}
}

func TestCareCooldownResourcesProgressAndFailureAtomicity(t *testing.T) {
	s := adopt(t)
	s.Stats.Satiety = 40
	before := clone(s)
	feed := Command{RequestID: commandID("feed-one"), ExpectedVersion: s.Version, Kind: "feed"}
	next, err := Apply(&s, feed, testNow)
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if next.Stats.Satiety != 70 || next.Inventory["snack"] != 4 || next.Coins != 22 || next.Experience != 5 || next.Daily.Care["feed"] != 1 || next.Daily.RewardedCare != 1 {
		t.Fatalf("care effects mismatch: %#v", next)
	}
	if !reflect.DeepEqual(s, before) {
		t.Fatal("successful Apply mutated its input")
	}
	failedBefore := clone(next)
	feed.RequestID = commandID("feed-cooldown")
	feed.ExpectedVersion = next.Version
	if _, err = Apply(&next, feed, testNow.Add(CareCooldown-time.Nanosecond)); !errors.Is(err, ErrCooldown) {
		t.Fatalf("cooldown got %v", err)
	}
	if !reflect.DeepEqual(next, failedBefore) {
		t.Fatal("failed Apply mutated its input")
	}
	feed.RequestID = commandID("feed-two")
	if _, err = Apply(&next, feed, testNow.Add(CareCooldown)); err != nil {
		t.Fatalf("care at exact cooldown boundary: %v", err)
	}
}

func TestShopInventoryEquipAndOptimisticVersion(t *testing.T) {
	s := adopt(t)
	buy := Command{RequestID: commandID("buy-hat"), ExpectedVersion: s.Version, Kind: "buy", ItemID: "leaf_hat"}
	if _, err := Apply(&s, buy, testNow); !errors.Is(err, ErrRequirements) {
		t.Fatalf("unearned purchase got %v", err)
	}
	s.Coins = 50
	next, err := Apply(&s, buy, testNow)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if next.Coins != 0 || next.Inventory["leaf_hat"] != 1 {
		t.Fatalf("purchase did not debit fixed price once: %#v", next)
	}
	buy.RequestID = commandID("buy-hat-again")
	buy.ExpectedVersion = next.Version
	if _, err = Apply(&next, buy, testNow); !errors.Is(err, ErrRequirements) {
		t.Fatalf("duplicate unique item purchase got %v", err)
	}
	equip := Command{RequestID: commandID("equip-hat"), ExpectedVersion: next.Version, Kind: "equip", ItemID: "leaf_hat", Slot: "head"}
	equipped, err := Apply(&next, equip, testNow)
	if err != nil || equipped.Equipment["head"] != "leaf_hat" || equipped.Inventory["leaf_hat"] != 1 {
		t.Fatalf("equip mismatch: state=%#v err=%v", equipped, err)
	}
	stale := Command{RequestID: commandID("stale"), ExpectedVersion: next.Version, Kind: "rename", Name: "Stale"}
	if _, err = Apply(&equipped, stale, testNow); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS got %v", err)
	}
}

func TestDailyQuestsAreOnceOnlyAndReset(t *testing.T) {
	s := adopt(t)
	s.Stats.Satiety = 0
	feed, err := Apply(&s, Command{RequestID: commandID("quest-feed"), ExpectedVersion: s.Version, Kind: "feed"}, testNow)
	if err != nil {
		t.Fatalf("quest care: %v", err)
	}
	claim := Command{RequestID: commandID("claim-breakfast"), ExpectedVersion: feed.Version, Kind: "claim", TaskID: "breakfast"}
	rewarded, err := Apply(&feed, claim, testNow)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if rewarded.Coins != feed.Coins+5 || rewarded.Experience != feed.Experience+10 || !rewarded.Daily.Claimed["breakfast"] {
		t.Fatalf("quest reward mismatch: %#v", rewarded)
	}
	claim.RequestID = commandID("claim-again")
	claim.ExpectedVersion = rewarded.Version
	if _, err = Apply(&rewarded, claim, testNow); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate claim got %v", err)
	}
	claim.RequestID = commandID("claim-same-day")
	if _, err = Apply(&rewarded, claim, testNow.Add(5*time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("same UTC day claim got %v", err)
	}
	claim.RequestID = commandID("claim-after-reset")
	if _, err = Apply(&rewarded, claim, time.Date(2026, time.October, 12, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrRequirements) {
		t.Fatalf("old progress survived UTC reset: %v", err)
	}
}

func TestRewardAndCommandCaps(t *testing.T) {
	s := adopt(t)
	careAt := time.Date(2026, time.October, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < MaxDailyRewardedCare+1; i++ {
		s.Stats.Mood = 0
		s.Stats.Energy = 100
		next, err := Apply(&s, Command{
			RequestID:       commandID(time.Duration(i).String()),
			ExpectedVersion: s.Version,
			Kind:            "play",
		}, careAt.Add(time.Duration(i)*CareCooldown))
		if err != nil {
			t.Fatalf("care %d: %v", i, err)
		}
		s = next
	}
	if s.Daily.RewardedCare != MaxDailyRewardedCare || s.Experience != MaxDailyRewardedCare*5 || s.Coins != 20+MaxDailyRewardedCare*2 {
		t.Fatalf("reward cap mismatch: %#v", s)
	}
	s.Daily.Commands = MaxDailyCommands
	before := clone(s)
	_, err := Apply(&s, Command{RequestID: commandID("over-limit"), ExpectedVersion: s.Version, Kind: "rename", Name: "Later"}, careAt.Add(2*time.Hour))
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("command cap got %v", err)
	}
	if !reflect.DeepEqual(s, before) {
		t.Fatal("limited command mutated input")
	}
}

func TestGrowthSaturates(t *testing.T) {
	s := State{Experience: MaxBalance - 1, Coins: MaxBalance - 1}
	growth(&s, 1000, 1000)
	if s.Experience != MaxBalance || s.Coins != MaxBalance || s.Level != 100 {
		t.Fatalf("growth escaped caps: %#v", s)
	}
}

func TestSettleBeyondDurationRangeAdvancesAnchorOnce(t *testing.T) {
	for _, year := range []int{2500, 9999} {
		t.Run(time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006"), func(t *testing.T) {
			s := adopt(t)
			s.SettledAt = s.SettledAt.Add(543210 * time.Nanosecond)
			now := time.Date(year, 1, 1, 12, 22, 20, 987654, time.UTC)
			settled := Settle(s, now)
			want := time.Date(year, 1, 1, 12, 20, 15, 543210, time.UTC)
			if !settled.SettledAt.Equal(want) || settled.Stats != (Stats{}) {
				t.Fatalf("long-gap projection: anchor=%s stats=%+v, want %s and zero stats", settled.SettledAt, settled.Stats, want)
			}
			if again := Settle(settled, now); !reflect.DeepEqual(again, settled) {
				t.Fatal("settlement replay changed an already settled projection")
			}
			first, err := Apply(&s, Command{RequestID: commandID("long-first"), Kind: "feed", ExpectedVersion: s.Version}, now)
			if err != nil || first.Stats.Satiety != 30 {
				t.Fatalf("first feed: %+v %v", first.Stats, err)
			}
			second, err := Apply(&first, Command{RequestID: commandID("long-second"), Kind: "feed", ExpectedVersion: first.Version}, now.Add(5*time.Minute))
			if err != nil || second.Stats.Satiety != 60 {
				t.Fatalf("old backlog charged again: %+v %v", second.Stats, err)
			}
		})
	}
}

func TestSettleMissingAnchorDoesNotKeepAncientBacklog(t *testing.T) {
	s := adopt(t)
	s.SettledAt = time.Time{}
	settled := Settle(s, testNow)
	want := time.Date(2026, time.October, 11, 23, 30, 0, 0, time.UTC)
	if !settled.SettledAt.Equal(want) || settled.Stats != (Stats{}) {
		t.Fatalf("missing anchor did not catch up: %s %+v", settled.SettledAt, settled.Stats)
	}
	if again := Settle(settled, testNow); !reflect.DeepEqual(again, settled) {
		t.Fatal("missing-anchor settlement was not idempotent")
	}
}

func TestZeroExpectedVersionIsOnlyForAdoption(t *testing.T) {
	commands := []Command{
		{Kind: "feed"}, {Kind: "wash"}, {Kind: "play"}, {Kind: "rest"},
		{Kind: "rename", Name: "Mochi"}, {Kind: "buy", ItemID: "snack"},
		{Kind: "equip", Slot: "head"}, {Kind: "claim", TaskID: "breakfast"},
	}
	for _, c := range commands {
		c.RequestID = commandID("zero-version")
		if err := Validate(c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s zero version: got %v, want PET_INVALID", c.Kind, err)
		}
		c.ExpectedVersion = 1
		if err := Validate(c); err != nil {
			t.Errorf("%s positive version rejected: %v", c.Kind, err)
		}
	}
}
