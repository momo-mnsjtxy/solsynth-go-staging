# Disposable PostgreSQL CI validation

The `PostgreSQL contract validation` workflow performs local build/vet/race checks, then initializes a fresh PostgreSQL 16 cluster under the runner's private temporary directory. Only a private Unix-domain socket is enabled; `listen_addresses` is empty. No existing database, service secret, deployment, or external account data is used. PostgreSQL is stopped on exit and explicitly restarted between write and persistence-read phases.

Six named real-database cases must pass before restart; the persistence case must pass again after restart. `PG_TEST_REQUIRED=1` makes absent configuration fatal. The JSON result guard also rejects skips, failures, missing test cases and empty output. Ordinary `verify.sh` without the disposable runner still clearly reports that database tests skipped.

Official action references were resolved through GitHub's public tag refs on 2026-10-07 and pinned immutably:

- [actions/checkout v4](https://api.github.com/repos/actions/checkout/git/ref/tags/v4): `11d5960a326750d5838078e36cf38b85af677262`
- [actions/setup-go v5](https://api.github.com/repos/actions/setup-go/git/ref/tags/v5): `40f1582b2485089dde7abd97c1529aa768e1baff`

These are official-origin checks, not a claim that every line of each action has undergone a security audit. Checkout does not persist credentials. Permissions are limited to `contents: read`; no user secrets are referenced. Go 1.26.4 is pinned, module downloads are checked against `go.sum`, and test execution disables new module retrieval. PostgreSQL 16 is supplied by the [official Ubuntu 24.04 runner image](https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2404-Readme.md), which documents the service as disabled by default. The workflow checks the major version and executable before using the binaries; it does not start the default service.

Local preflight passed shell/YAML parsing, Go build/vet/ordinary/race checks, and synthetic guard checks proving that missing/skipped/failed results are rejected. The local runtime still cannot create Unix sockets; only a completed remote workflow run can establish actual database results. This workflow does not establish production authentication, migration rollout, C# differential compatibility or whole-platform parity.
