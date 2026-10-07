package board

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type transactionalFake struct {
	*fakeRepo
	commitError error
	callbacks   int
	events      *[]string
}

func (t *transactionalFake) WithinAccount(ctx context.Context, account string, fn func(Repository) error) error {
	t.callbacks++
	*t.events = append(*t.events, "begin")
	scoped := &fakeRepo{items: copyItems(t.items)}
	if err := fn(scoped); err != nil {
		*t.events = append(*t.events, "rollback")
		return err
	}
	if t.commitError != nil {
		*t.events = append(*t.events, "commit-error")
		return t.commitError
	}
	t.items = copyItems(scoped.items)
	*t.events = append(*t.events, "commit")
	return nil
}

func TestServiceTransactionCommitsBeforeCache(t *testing.T) {
	for _, method := range []string{"replace", "app", "admin"} {
		t.Run(method, func(t *testing.T) {
			s, r, c, _ := setup(custom())
			events := []string{}
			tx := &transactionalFake{fakeRepo: r, events: &events}
			s.Repository = tx
			c.calls = &events
			s.Normalizer = &fakeNormalizer{result: Payload{"new": true}}
			var err error
			switch method {
			case "replace":
				_, err = s.Replace(context.Background(), owner, []Item{custom()})
			case "app":
				_, err = s.UpdateAppPayload(context.Background(), owner, itemID, app, "weather", nil)
			case "admin":
				_, err = s.UpdateAdminPayload(context.Background(), owner, itemID, nil)
			}
			if err != nil || tx.callbacks != 1 || !reflect.DeepEqual(events, []string{"begin", "commit", "cache"}) {
				t.Fatal(err, tx.callbacks, events)
			}
		})
	}
}

func TestServiceFailedCommitNeverInvalidates(t *testing.T) {
	s, r, c, _ := setup(custom())
	events := []string{}
	tx := &transactionalFake{fakeRepo: r, events: &events, commitError: failure}
	s.Repository = tx
	_, err := s.Replace(context.Background(), owner, nil)
	if err != failure || len(c.groups) != 0 || len(r.items) != 1 || !reflect.DeepEqual(events, []string{"begin", "commit-error"}) {
		t.Fatal(err, c.groups, r.items, events)
	}
}

func TestServiceValidatorFailureRollsBackTransaction(t *testing.T) {
	s, r, c, _ := setup(custom())
	events := []string{}
	tx := &transactionalFake{fakeRepo: r, events: &events}
	s.Repository = tx
	_, err := s.UpdateAppPayload(context.Background(), owner, itemID, app, "weather", nil)
	if !errors.Is(err, ErrDependency) || len(c.groups) != 0 || !reflect.DeepEqual(events, []string{"begin", "rollback"}) {
		t.Fatal(err, c.groups, events)
	}
}
