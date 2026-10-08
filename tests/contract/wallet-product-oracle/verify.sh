#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
DOTNET_BIN="${DOTNET_BIN:-dotnet}"
work=$(mktemp -d /tmp/solsynth-wallet-product-reference.XXXXXXXX)
export DOTNET_CLI_HOME="$work/home" DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1
export DOTNET_GENERATE_ASPNET_CERTIFICATE=false DOTNET_EnableDiagnostics=0
"$DOTNET_BIN" --version | grep -E '^10\.'
"$DOTNET_BIN" build WalletProductOracle.csproj --disable-build-servers -m:1 \
  -p:UseSharedCompilation=false --configfile NuGet.Config \
  --artifacts-path "$work/artifacts" -o "$work/bin"
"$DOTNET_BIN" "$work/bin/WalletProductOracle.dll" < cases.json > "$work/expected.json"
"$DOTNET_BIN" "$work/bin/WalletProductOracle.dll" < review-cases.json > "$work/review-expected.json"
"$DOTNET_BIN" "$work/bin/WalletProductOracle.dll" --naming-bmp > "$work/naming-bmp.json"
python3 - "$work/expected.json" "$work/naming-bmp.json" "$work/review-expected.json" <<'PY'
import json,pathlib,sys
expected=json.loads(pathlib.Path('expected.json').read_text())
actual=json.loads(pathlib.Path(sys.argv[1]).read_text())
# Bodies remain raw STRINGS. Never decode them as generic dictionaries: doing
# so would discard duplicate snake-case names and exact decimal numeric lexemes.
assert actual == expected, 'independent wallet-product reference changed'
assert json.loads(pathlib.Path(sys.argv[2]).read_text()) == json.loads(pathlib.Path('naming-bmp.json').read_text()), 'framework naming table changed'
assert json.loads(pathlib.Path(sys.argv[3]).read_text()) == json.loads(pathlib.Path('review-expected.json').read_text()), 'independent review regressions changed'
print(f'Independent wallet-product reference reproduced all {len(expected)} cases including six review regressions, the focused regression subset and BMP naming facts, preserving raw body strings')
PY
printf 'Reference build/results retained at %s\n' "$work"
