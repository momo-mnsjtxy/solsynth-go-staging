// SPDX-License-Identifier: AGPL-3.0-only
package commerce

import "errors"

// ErrRetry denotes a transaction that rolled back entirely on a nonblocking
// canonical lock boundary. The caller may retry the same idempotent request.
var ErrRetry = errors.New("commerce operation must be retried after concurrent change")
