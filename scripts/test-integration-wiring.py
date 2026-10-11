#!/usr/bin/env python3
"""Offline fixture wiring checks only; never starts PostgreSQL or an HTTP host."""
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
REQUIRED_SCHEMAS = {
 "internal/petpg/schema.sql",
 "internal/authoritypg/stream_ticket_schema.sql",
 "internal/affiliationpg/paid_schema.sql",
 "internal/chatpg/transient_schema.sql", "internal/mediapg/voice_schema.sql",
 "internal/developmediapg/schema.sql",
 "internal/interactionpg/collection_publication_schema.sql",
 "internal/accounthistorypg/schema.sql",
 "internal/accounthistorypg/request_context_schema.sql",
 "internal/connectionpg/schema.sql",
 "internal/notificationpg/devices_schema.sql",
 "internal/notificationpg/wellbeing_schema.sql",
 "internal/activitypg/schema.sql", "internal/friendeventpg/schema.sql", "internal/notificationpg/friend_schema.sql",
 "internal/federationpg/delivery_schema.sql",
 "internal/federationpg/broader_schema.sql",
 "internal/timelinepg/subscription_state_schema.sql",
 "internal/chatpg/organization_schema.sql",
 "internal/postembedpg/schema.sql",
 "internal/federationpg/public_admission_schema.sql",
 "internal/realmmemberpg/schema.sql",
 "internal/calendarmediapg/schema.sql",
 "internal/blogpg/schema.sql",
 "internal/botpg/execution_schema.sql",
 "internal/realmboostpg/schema.sql",
 "internal/postpg/scheduling_schema.sql",
 "internal/roommediapg/schema.sql",
 "internal/socialpg/expiry_schema.sql",
 "internal/socialpg/permissions_schema.sql",
 "internal/passkitpg/schema.sql",
 "internal/ticketmediapg/schema.sql",
 "internal/chatpg/mls_schema.sql",
 "internal/mediapg/artwork_schema.sql",
 "internal/chatmediapg/schema.sql",
 "internal/profilemediapg/schema.sql",
 "internal/walletpg/funds_schema.sql",
 "internal/walletpg/exchange_schema.sql",
 "internal/postawardpg/schema.sql",
 "internal/sponsorshippg/schema.sql",
 "internal/publisherratingpg/schema.sql",
 "internal/authoritypg/continuity_schema.sql",
 "internal/livestreampg/chat_schema.sql",
 "internal/livestreampg/outputs_schema.sql",
 "internal/livestreampg/awards_schema.sql",
 "internal/domaintrustpg/schema.sql",
 "internal/accountpg/name_schema.sql",
 "internal/federationpg/identity_schema.sql",
 "internal/federationpg/quote_schema.sql",
 "internal/federationpg/discovery_schema.sql",
 "internal/adminpg/schema.sql",
 "internal/livestreampg/schema.sql",
 "internal/publishernotificationpg/schema.sql",
 "internal/moderationpg/schema.sql",
 "internal/namecardpg/schema.sql",
 "internal/rewindpg/schema.sql",
 "internal/accountsettingspg/schema.sql",
 "internal/translationpg/schema.sql",
 "internal/chatpg/relations_schema.sql",
 "internal/readerpg/schema.sql",
 "internal/postpg/realm_schema.sql",
 "internal/notabledaypg/schema.sql",
 "internal/oauthpg/schema.sql",
 "internal/fortunepg/schema.sql",
 "internal/botpg/schema.sql",
 "internal/ticketpg/schema.sql",
 "internal/affiliationpg/schema.sql",
 "internal/authoritypg/activation_schema.sql",
 "internal/merchantpg/schema.sql",
 "internal/presencepg/schema.sql",
 "internal/progressionpg/schema.sql",
 "internal/locationpg/schema.sql",
    "internal/callpg/schema.sql",
    "internal/federationpg/schema.sql",
 "internal/mediapg/usage_schema.sql",
 "internal/surveypg/schema.sql",
 "internal/stickerpg/schema.sql",
 "internal/timelinepg/schema.sql",
 "internal/postmediapg/schema.sql",
    "internal/notificationpg/schema.sql",
    "internal/apppg/schema.sql",
    "internal/signinpg/schema.sql",
 "internal/signinpg/address_schema.sql",
    "internal/commercepg/schema.sql",
    "internal/commercepg/custom_order_identifier.sql",
    "internal/commercepg/coupon_schema.sql",
 "internal/commercepg/shipping_schema.sql",
    "internal/commercepg/order_index_scope.sql",
    "internal/commercepg/maintenance_schema.sql",
    "internal/mediapg/resumable_schema.sql",
    "internal/mediapg/schema.sql",
    "internal/socialpg/schema.sql",
    "internal/interactionpg/schema.sql",
    "internal/chatpg/schema.sql",
    "internal/authoritypg/schema.sql",
    "internal/accountpg/schema.sql",
    "internal/walletpg/schema.sql",
    "internal/application/schema.sql",
    "internal/postpg/schema.sql",
    "tests/integration/schema.sql",
    "internal/watchpg/testdata/watchers-projection.sql",
    "internal/boardpg/testdata/schema.sql",
    "internal/developpg/testdata/schema.sql",
    "internal/developpg/testdata/packages.sql",
    "internal/developpg/testdata/bot_commands.sql",
    "internal/nodeinfopg/testdata/schema.sql",
    "internal/messagerpg/testdata/schema.sql",
    "internal/taxonomypg/testdata/schema.sql",
 "internal/authoritypg/assurance_schema.sql",
 "internal/signinpg/payment_pin_schema.sql",
 "internal/signinpg/factor_setup_schema.sql",
 "internal/walletpg/pin_schema.sql",
 "internal/walletpg/transfer_requests_schema.sql",
}
REQUIRED_BINARIES = {
    "BACKEND_BINARY": ("backend", "backend"),
    "PASSPORT_BOARD_BINARY": ("passport-board", "passport-board"),
    "MESSAGER_SYNC_BINARY": ("messager-sync", "messager-sync"),
    "TAXONOMY_BINARY": ("taxonomy", "taxonomy"),
}
PSQL = '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 -f '
PHASE_TIMEOUTS = {"before": "40m", "after": "10m"}


def check(runner, read_schema=None):
    if read_schema is None:
        read_schema = lambda name: (ROOT / name).read_text()
    # Inspect only the explicit disposable schema paths, not deployment files.
    installs = list(re.finditer(
        r'^"\$PG_BIN/psql" "\$PG_TEST_DSN" -v ON_ERROR_STOP=1 (?:--single-transaction )?-f ([a-zA-Z0-9_./-]+)$',
        runner, re.M,
    ))
    schemas = [match.group(1) for match in installs]
    if set(schemas) != REQUIRED_SCHEMAS or len(schemas) != len(REQUIRED_SCHEMAS):
        raise ValueError("missing, unexpected or duplicate disposable schema install")
    for schema, dependencies in {
        "internal/authoritypg/stream_ticket_schema.sql": ("internal/authoritypg/schema.sql",),
        "internal/affiliationpg/paid_schema.sql": ("internal/affiliationpg/schema.sql", "internal/walletpg/schema.sql"),
        "internal/botpg/schema.sql": ("internal/apppg/schema.sql", "internal/mediapg/schema.sql"),
        "internal/federationpg/broader_schema.sql": ("internal/federationpg/delivery_schema.sql", "internal/socialpg/schema.sql", "internal/postpg/schema.sql"),
        "internal/accounthistorypg/request_context_schema.sql": ("internal/accounthistorypg/schema.sql", "internal/authoritypg/schema.sql"),
        "internal/connectionpg/schema.sql": ("internal/accountpg/schema.sql", "internal/accounthistorypg/schema.sql"),
        "internal/notificationpg/devices_schema.sql": ("internal/notificationpg/schema.sql", "internal/authoritypg/schema.sql"),
        "internal/notificationpg/wellbeing_schema.sql": ("internal/notificationpg/schema.sql",),
    }.items():
        if schemas.index(schema) <= max(schemas.index(dependency) for dependency in dependencies):
            raise ValueError("canonical dependencies must precede schema: " + schema)
    if schemas.index("internal/chatpg/transient_schema.sql") <= schemas.index("internal/chatpg/schema.sql"):
        raise ValueError("placeholder lifecycle requires canonical chat first")
    if schemas.index("internal/mediapg/voice_schema.sql") <= max(schemas.index(x) for x in ("internal/chatpg/schema.sql","internal/mediapg/schema.sql")):
        raise ValueError("voice staging requires canonical chat and quota/media first")
    before = runner.index("PG_TEST_PHASE=before ")
    after = runner.index("PG_TEST_PHASE=after ")
    if before >= after or any(match.start() > before for match in installs):
        raise ValueError("all disposable schemas must precede the before phase")
    if schemas.index("internal/accounthistorypg/schema.sql") <= max(schemas.index(x) for x in ("internal/accountpg/schema.sql", "internal/socialpg/schema.sql", "internal/progressionpg/schema.sql", "internal/signinpg/payment_pin_schema.sql")):
        raise ValueError("account history requires its canonical audit sources first")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/accounthistorypg/schema.sql' not in runner:
        raise ValueError("account history migration must be atomic")
    if not schemas.index("internal/authoritypg/schema.sql") < schemas.index("internal/accountpg/schema.sql"):
        raise ValueError("accounts require the authority schema first")
    if schemas.index("internal/interactionpg/collection_publication_schema.sql") <= schemas.index("internal/interactionpg/schema.sql"):
        raise ValueError("collection publication intent requires canonical collections first")
    if schemas.index("internal/authoritypg/continuity_schema.sql") <= schemas.index("internal/authoritypg/schema.sql"):
        raise ValueError("session continuity requires canonical authority first")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/authoritypg/continuity_schema.sql' not in runner:
        raise ValueError("session continuity migration must be atomic")
    if schemas.index("internal/authoritypg/assurance_schema.sql") <= schemas.index("internal/authoritypg/schema.sql"):
        raise ValueError("credential assurance requires canonical authority first")
    if schemas.index("internal/signinpg/payment_pin_schema.sql") <= schemas.index("internal/signinpg/schema.sql"):
        raise ValueError("PIN factor migration requires canonical sign-in first")
    if schemas.index("internal/walletpg/pin_schema.sql") <= max(schemas.index("internal/walletpg/schema.sql"), schemas.index("internal/signinpg/payment_pin_schema.sql")):
        raise ValueError("PIN policy references require wallet and factors first")
    if schemas.index("internal/signinpg/factor_setup_schema.sql") <= max(schemas.index("internal/signinpg/payment_pin_schema.sql"),schemas.index("internal/authoritypg/continuity_schema.sql")):
        raise ValueError("factor setup requires factors and session continuity")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/signinpg/factor_setup_schema.sql' not in runner:
        raise ValueError("factor setup policy history migration must be atomic")
    if schemas.index("internal/accountpg/name_schema.sql") <= schemas.index("internal/accountpg/schema.sql"):
        raise ValueError("publisher reservations require canonical publishers first")
    if schemas.index("internal/federationpg/identity_schema.sql") <= max(schemas.index("internal/federationpg/schema.sql"), schemas.index("internal/accountpg/name_schema.sql")):
        raise ValueError("federation anchors require registrations and name reservations first")
    if schemas.index("internal/federationpg/quote_schema.sql") <= max(schemas.index("internal/federationpg/identity_schema.sql"), schemas.index("internal/postpg/schema.sql")):
        raise ValueError("quote authorizations require canonical posts and anchored federation first")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/federationpg/quote_schema.sql' not in runner:
        raise ValueError("quote authorization migration must be atomic")
    if schemas.index("internal/federationpg/delivery_schema.sql") <= schemas.index("internal/federationpg/quote_schema.sql"):
        raise ValueError("delivery observation requires federation quotes and identity first")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/federationpg/delivery_schema.sql' not in runner:
        raise ValueError("delivery intent migration must be atomic")
    if schemas.index("internal/federationpg/public_admission_schema.sql") <= schemas.index("internal/federationpg/discovery_schema.sql"):
        raise ValueError("public admission requires durable discovery authority fencing")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/federationpg/public_admission_schema.sql' not in runner:
        raise ValueError("public admission migration must be atomic")
    if schemas.index("internal/federationpg/discovery_schema.sql") <= max(schemas.index("internal/federationpg/identity_schema.sql"), schemas.index("internal/accountpg/name_schema.sql")):
        raise ValueError("discovery requires anchored federation and permanent publisher names")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/federationpg/discovery_schema.sql' not in runner:
        raise ValueError("discovery migration must be atomic")
    if schemas.index("internal/commercepg/order_index_scope.sql") <= schemas.index("internal/commercepg/schema.sql"):
        raise ValueError("commerce index migration requires the base commerce schema first")
    if schemas.index("internal/commercepg/custom_order_identifier.sql") <= schemas.index("internal/commercepg/schema.sql"):
        raise ValueError("custom order identifier migration requires the base commerce schema first")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/commercepg/custom_order_identifier.sql' not in runner:
        raise ValueError("custom order identifier migration must be atomic")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/commercepg/order_index_scope.sql' not in runner:
        raise ValueError("commerce index migration must be atomic")
    if schemas.index("internal/commercepg/maintenance_schema.sql") <= schemas.index("internal/commercepg/schema.sql"):
        raise ValueError("maintenance quarantine requires canonical subscriptions")
    for name in ("walletpg", "application", "postpg"):
        if schemas.index("internal/accountpg/schema.sql") >= schemas.index(f"internal/{name}/schema.sql"):
            raise ValueError("owned dependent schema must follow accounts: " + name)
    if schemas.index("internal/blogpg/schema.sql") <= max(schemas.index("internal/authoritypg/continuity_schema.sql"), schemas.index("internal/accountpg/schema.sql"), schemas.index("internal/socialpg/schema.sql"), schemas.index("internal/postpg/schema.sql")):
        raise ValueError("blog policy fences require canonical dependencies")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/blogpg/schema.sql' not in runner:
        raise ValueError("blog policy fence migration must be atomic")
    if schemas.index("internal/socialpg/expiry_schema.sql") <= schemas.index("internal/socialpg/schema.sql"):
        raise ValueError("relationship expiry requires canonical social tables first")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/socialpg/expiry_schema.sql' not in runner:
        raise ValueError("relationship expiry migration must be atomic")
    if schemas.index("internal/socialpg/permissions_schema.sql") <= schemas.index("internal/socialpg/schema.sql"):
        raise ValueError("realm permissions require canonical realm membership first")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/socialpg/permissions_schema.sql' not in runner:
        raise ValueError("realm permission migration must be atomic")
    if schemas.index("internal/postpg/realm_schema.sql") <= max(schemas.index("internal/postpg/schema.sql"), schemas.index("internal/socialpg/schema.sql")):
        raise ValueError("post realm foreign key requires canonical posts and realms first")
    if schemas.index("internal/chatpg/schema.sql") <= schemas.index("internal/messagerpg/testdata/schema.sql"):
        raise ValueError("chat write extension requires canonical read schema first")
    if schemas.index("internal/postpg/schema.sql") >= schemas.index("internal/application/schema.sql"):
        raise ValueError("canonical taxonomy view requires posts first")
    if schemas.index("internal/interactionpg/schema.sql") <= schemas.index("internal/application/schema.sql"):
        raise ValueError("interactions require taxonomy first")
    if '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/mediapg/usage_schema.sql' not in runner:
        raise ValueError("media usage constraint migration must be atomic")
    if schemas.index("internal/profilemediapg/schema.sql") <= max(schemas.index("internal/accountpg/schema.sql"), schemas.index("internal/mediapg/schema.sql"), schemas.index("internal/socialpg/schema.sql")):
        raise ValueError("profile media requires canonical accounts, media and social policy")
    if schemas.index("internal/chatpg/mls_schema.sql") <= max(schemas.index("internal/chatpg/schema.sql"), schemas.index("internal/chatpg/relations_schema.sql"), schemas.index("internal/authoritypg/schema.sql")):
        raise ValueError("opaque MLS requires canonical authority, chat and relations")
    if schemas.index("internal/ticketmediapg/schema.sql") <= max(schemas.index("internal/ticketpg/schema.sql"), schemas.index("internal/mediapg/schema.sql")):
        raise ValueError("ticket media requires canonical tickets and media")
    if schemas.index("internal/mediapg/resumable_schema.sql") <= schemas.index("internal/mediapg/schema.sql"):
        raise ValueError("resumable media requires base schema")
    if any(schemas.index("internal/commercepg/schema.sql") <= schemas.index(name) for name in ("internal/apppg/schema.sql","internal/walletpg/schema.sql")):
        raise ValueError("commerce requires canonical apps and wallet first")
    if any(schemas.index("internal/callpg/schema.sql") <= schemas.index(name) for name in ("internal/authoritypg/schema.sql", "internal/chatpg/schema.sql", "internal/notificationpg/schema.sql")):
        raise ValueError("calls require canonical authority/chat/notification schemas")
    if any(schemas.index("internal/federationpg/schema.sql") <= schemas.index(name) for name in ("internal/authoritypg/schema.sql", "internal/accountpg/schema.sql", "internal/postpg/schema.sql")):
        raise ValueError("federation requires canonical authority/accounts/posts schemas")
    tables = {}
    for name in schemas:
        source = read_schema(name)
        for table in re.findall(
            r"^CREATE TABLE\s+(?:IF NOT EXISTS\s+)?([a-zA-Z_][a-zA-Z0-9_.]*)\s*\(",
            source, re.M | re.I,
        ):
            qualified = table.lower() if "." in table else "public." + table.lower()
            if qualified in tables:
                shared_chat = (name == "internal/chatpg/schema.sql" and tables[qualified] == "internal/messagerpg/testdata/schema.sql"
                               and qualified in {"public.chat_rooms", "public.chat_members", "public.chat_messages", "public.chat_reactions"}
                               and re.search(r"^CREATE TABLE IF NOT EXISTS " + re.escape(table) + r"\s*\(", source, re.M))
                if not shared_chat:
                    raise ValueError("disposable schema table collision: " + qualified)
                continue
            tables[qualified] = name
    # Infer cross-file FK order from the actual owned SQL, not a maintained
    # list of domain names. A table is known at its CREATE token, so self-FKs
    # remain valid; REFERENCES and ALTER targets must already exist.
    known_tables = set()
    token = re.compile(r"\b(CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?|REFERENCES\s+|ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?)([a-zA-Z_][a-zA-Z0-9_.]*)", re.I)
    for name in schemas:
        sql = re.sub(r"/\*.*?\*/", "", read_schema(name), flags=re.S)
        sql = re.sub(r"--[^\n]*", "", sql)
        for match in token.finditer(sql):
            kind, table = match.groups()
            qualified = table.lower() if "." in table else "public." + table.lower()
            if kind.upper().startswith("CREATE"):
                known_tables.add(qualified)
            elif qualified not in known_tables:
                raise ValueError("schema references a table before installation: " + name + " -> " + qualified)
    for variable, (binary, package) in REQUIRED_BINARIES.items():
        build = f'"$GO_BIN" build -o "$run/{binary}" ./cmd/{package}'
        export = f'export {variable}="$run/{binary}"'
        if build not in runner[:before] or export not in runner[:before]:
            raise ValueError("fixture binary is not built/exported before required tests: " + variable)
    for phase in ("before", "after"):
        invocation = (
            f'PG_TEST_PHASE={phase} "$GO_BIN" test -timeout {PHASE_TIMEOUTS[phase]} -race -count=1 -json '
            f'-run "$(python3 scripts/check-integration-results.py --pattern {phase} --group "$group")" '
            f'./tests/integration | tee "$run/{phase}.jsonl"'
        )
        verification = f'python3 scripts/check-integration-results.py {phase} "$run/{phase}.jsonl" --group "$group"'
        if invocation not in runner or verification not in runner:
            raise ValueError("phase selector/log guard must use the required-set source: " + phase)
        if runner.index(verification) <= runner.index(invocation):
            raise ValueError("phase log must be checked after the test command")
    if '"$PG_BIN/pg_ctl" -D "$run/data" -m fast -w stop\nrunning=0\nstart\n' not in runner[before:after]:
        raise ValueError("actual PostgreSQL stop/start is required between phases")
    for required in (
        "set -euo pipefail",
        "export PG_TEST_REQUIRED=1",
        'export PG_TEST_DSN="host=$run/socket port=55432 user=synthetic dbname=postgres sslmode=disable"',
        'export SOLSYNTH_DISPOSABLE_PG="$run"',
        "-c listen_addresses='' -c unix_socket_permissions=0700",
    ):
        if required not in runner[:before]:
            raise ValueError("required disposable-cluster safety or fail-closed control is missing")
    return len(schemas), len(tables)


def main():
    source = (ROOT / "scripts/integration-postgres.sh").read_text()
    schemas, tables = check(source)
    authority = PSQL + "internal/authoritypg/schema.sql"
    accounts = PSQL + "internal/accountpg/schema.sql"
    mutations = [
        source.replace("--single-transaction -f internal/accounthistorypg/schema.sql", "-f internal/accounthistorypg/schema.sql"),
        source.replace("--single-transaction -f internal/commercepg/custom_order_identifier.sql", "-f internal/commercepg/custom_order_identifier.sql"),
        source.replace("--single-transaction -f internal/socialpg/expiry_schema.sql", "-f internal/socialpg/expiry_schema.sql"),
        source.replace("--single-transaction -f internal/socialpg/permissions_schema.sql", "-f internal/socialpg/permissions_schema.sql"),
        source.replace("--single-transaction -f internal/federationpg/quote_schema.sql", "-f internal/federationpg/quote_schema.sql"),
        source.replace("--single-transaction -f internal/federationpg/discovery_schema.sql", "-f internal/federationpg/discovery_schema.sql"),
        source.replace("--single-transaction -f internal/federationpg/delivery_schema.sql", "-f internal/federationpg/delivery_schema.sql"),
        source.replace("--single-transaction -f internal/federationpg/public_admission_schema.sql", "-f internal/federationpg/public_admission_schema.sql"),
        source.replace("--single-transaction -f internal/commercepg/order_index_scope.sql", "-f internal/commercepg/order_index_scope.sql"),
        source.replace("--single-transaction -f internal/mediapg/usage_schema.sql", "-f internal/mediapg/usage_schema.sql"),
        source.replace("test -timeout 40m -race", "test -race"),
        source.replace("test -timeout 10m -race", "test -race"),
        source.replace("--single-transaction -f internal/signinpg/factor_setup_schema.sql", "-f internal/signinpg/factor_setup_schema.sql"),
        source.replace("--single-transaction -f internal/authoritypg/continuity_schema.sql", "-f internal/authoritypg/continuity_schema.sql"),
        source.replace("-f internal/messagerpg/testdata/schema.sql", "-f tests/integration/schema.sql"),
        source.replace("export MESSAGER_SYNC_BINARY=", "export OMITTED_MESSAGER_SYNC_BINARY="),
        source.replace("export BACKEND_BINARY=", "export OMITTED_BACKEND_BINARY="),
        source.replace("./cmd/taxonomy", "./cmd/missing-taxonomy"),
        source.replace("./cmd/backend", "./cmd/missing-backend"),
        source.replace("export PG_TEST_REQUIRED=1", "export PG_TEST_REQUIRED=0"),
        source.replace("--pattern after", "--pattern before"),
        source.replace("--pattern before", "--pattern after"),
        source.replace("check-integration-results.py after", "check-integration-results.py before"),
        source.replace("-f internal/taxonomypg/testdata/schema.sql", "-f internal/watchpg/testdata/schema.sql"),
        source.replace(authority + "\n", "").replace(accounts, accounts + "\n" + authority),
        source.replace(accounts + "\n", "").replace("PG_TEST_PHASE=before ", accounts + "\nPG_TEST_PHASE=before ", 1),
        source.replace('stop\nrunning=0\nstart\n', 'stop\nrunning=0\n'),
        source.replace("set -euo pipefail", "set -eu"),
        source.replace("host=$run/socket", "host=external.example.invalid"),
        source.replace("-c listen_addresses=''", "-c listen_addresses='*'"),
    ]
    custom_identifier = '"$PG_BIN/psql" "$PG_TEST_DSN" -v ON_ERROR_STOP=1 --single-transaction -f internal/commercepg/custom_order_identifier.sql'
    commerce = PSQL + "internal/commercepg/schema.sql"
    mutations.append(source.replace(custom_identifier + "\n", "").replace(commerce, custom_identifier + "\n" + commerce))
    collections = PSQL + "internal/interactionpg/schema.sql"
    publications = PSQL + "internal/interactionpg/collection_publication_schema.sql"
    mutations.append(source.replace(publications + "\n", "").replace(collections, publications + "\n" + collections))
    oauth = PSQL + "internal/oauthpg/schema.sql"
    signin = PSQL + "internal/signinpg/schema.sql"
    mutations.append(source.replace(oauth + "\n", "").replace(signin, signin + "\n" + oauth))
    # Repeated owned schemas must be rejected even when SQL uses IF NOT EXISTS.
    mutations.append(source.replace("PG_TEST_PHASE=before ", authority + "\nPG_TEST_PHASE=before ", 1))
    for mutated in mutations:
        if mutated == source:
            raise AssertionError("regression mutation did not change the runner")
        try:
            check(mutated)
        except ValueError:
            pass
        else:
            raise AssertionError("incomplete or conflicting integration wiring was accepted")
    # Detect a genuine qualified table collision even when it is guarded by
    # IF NOT EXISTS; that SQL clause must not hide incompatible fixture ownership.
    def conflicting_schema(name):
        value = (ROOT / name).read_text()
        if name == "internal/accountpg/schema.sql":
            value += "\nCREATE TABLE IF NOT EXISTS owned_authority_accounts (id uuid);\n"
        return value

    try:
        check(source, conflicting_schema)
    except ValueError as error:
        assert "table collision" in str(error)
    else:
        raise AssertionError("IF NOT EXISTS hid a table collision")
    print(
        f"Verified {schemas} disposable schemas / {tables} distinct tables, required binary/phase "
        f"wiring and {len(mutations) + 1} regression rejections; no database or HTTP started"
    )


if __name__ == "__main__":
    main()
