#!/usr/bin/env bash
# Run independently authored reference programs without tool-home credentials,
# telemetry, HTTPS certificate generation, sockets, or remote package sources.
set -euo pipefail
cd "$(dirname "$0")/.."
export DOTNET_GENERATE_ASPNET_CERTIFICATE=false
export DOTNET_EnableDiagnostics=0
export DOTNET_CLI_TELEMETRY_OPTOUT=1
export DOTNET_NOLOGO=1
export DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1
export DOTNET_BIN="${DOTNET_BIN:-/workspace/shared/toolchains/dotnet-10.0.401/dotnet}"
work=$(mktemp -d /tmp/solsynth-all-oracles.XXXXXXXX)
export DOTNET_CLI_HOME="$work/home"
for directory in exchange-oracle badges-oracle recovery-regression-oracle wallet-product-oracle; do
  bash "tests/contract/$directory/verify.sh"
done
source_dir="$PWD/inventory/product-client-evidence/bot-command-oracle"
"$DOTNET_BIN" build "$source_dir/BotCommandOracle.csproj" --disable-build-servers -m:1 \
  -p:UseSharedCompilation=false --configfile "$source_dir/NuGet.Config" \
  --artifacts-path "$work/bot-artifacts" -o "$work/bot-bin"
"$DOTNET_BIN" "$work/bot-bin/BotCommandOracle.dll" > "$work/bot-results.json"
python3 - "$source_dir/results.json" "$work/bot-results.json" <<'PY'
import json,sys
assert json.load(open(sys.argv[1]))==json.load(open(sys.argv[2])), 'documented-model fixture differs'
print('Reproduced 19 selected-policy documented-model cases; not original shared model parity')
PY
printf 'All reference results reproduced; disposable artifacts: %s\n' "$work"
