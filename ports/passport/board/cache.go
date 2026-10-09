// SPDX-License-Identifier: AGPL-3.0-only
package board

import (
	"context"
	"sync"
)

// GroupCache is an actual process-local cache for the explicit fixture host.
// It is not a distributed production account-hydration cache substitute.
type GroupCache struct {
	mu     sync.RWMutex
	groups map[string]map[string]Account
}

func NewGroupCache() *GroupCache { return &GroupCache{groups: map[string]map[string]Account{}} }
func (c *GroupCache) Put(group, key string, value Account) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.groups == nil {
		c.groups = map[string]map[string]Account{}
	}
	if c.groups[group] == nil {
		c.groups[group] = map[string]Account{}
	}
	c.groups[group][key] = value
}
func (c *GroupCache) Get(group, key string) (Account, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.groups[group][key]
	return v, ok
}
func (c *GroupCache) InvalidateGroup(ctx context.Context, group string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.groups, group)
	return nil
}
func (c *GroupCache) PurgeAccount(ctx context.Context, account Account) error {
	return c.InvalidateGroup(ctx, "account:"+account.ID)
}
