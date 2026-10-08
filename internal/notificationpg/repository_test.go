// SPDX-License-Identifier: AGPL-3.0-only
package notificationpg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"solsynth.local/go-migration/ports/notifications"
)

func TestCommandValidationAndDeliveryDefaults(t *testing.T) {
	valid := notifications.Command{Key: "event:1", AccountID: "10000000-0000-0000-0000-000000000001", AppID: notifications.DefaultApp, Topic: "owned.example", Title: "Synthetic", Content: "消息"}
	if e := validate(&valid); e != nil {
		t.Fatal(e)
	}
	if string(valid.Meta) != "{}" {
		t.Fatal("object default")
	}
	for _, mutate := range []func(*notifications.Command){func(c *notifications.Command) { c.Meta = json.RawMessage(`[]`) }, func(c *notifications.Command) { c.Key = "" }, func(c *notifications.Command) { c.Title = strings.Repeat("x", 1025) }, func(c *notifications.Command) { c.AccountID = "forged" }, func(c *notifications.Command) { c.Content = "bad\x00value" }, func(c *notifications.Command) { c.AppID = "cross/app" }} {
		c := valid
		mutate(&c)
		if e := validate(&c); !errors.Is(e, notifications.ErrInvalid) {
			t.Fatal("accepted invalid input", e)
		}
	}
	if e := (notifications.UnconfiguredSender{}).Send(context.Background(), notifications.Delivery{}); !errors.Is(e, notifications.ErrDeliveryUnconfigured) {
		t.Fatal(e)
	}
	for n := 0; n < 50; n++ {
		delay := RetryDelay(n)
		if delay < time.Second || delay > 5*time.Minute {
			t.Fatal("unbounded retry", n, delay)
		}
	}
	if RetryDelay(2) != 2*time.Second || RetryDelay(10) != 5*time.Minute {
		t.Fatal("backoff")
	}
	r := New(nil, nil, notifications.DefaultApp)
	if _, e := r.Enqueue(context.Background(), valid); !errors.Is(e, notifications.ErrUnavailable) {
		t.Fatal(e)
	}
}
