#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
DOTNET_BIN="${DOTNET_BIN:-dotnet}"
"$DOTNET_BIN" --version | grep -E '^10\.'
work=$(mktemp -d /tmp/solsynth-exchange-oracle.XXXXXXXX)
export DOTNET_CLI_HOME="$work/home"
export DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1
export DOTNET_GENERATE_ASPNET_CERTIFICATE=false DOTNET_EnableDiagnostics=0
"$DOTNET_BIN" build ExchangeOracle.csproj --disable-build-servers -m:1 \
  -p:UseSharedCompilation=false --configfile NuGet.Config \
  --artifacts-path "$work/artifacts" -o "$work/bin"
"$DOTNET_BIN" "$work/bin/ExchangeOracle.dll" < cases.json > "$work/expected.json"
"$DOTNET_BIN" "$work/bin/ExchangeOracle.dll" --mvc-methods > "$work/mvc-methods.json"
"$DOTNET_BIN" "$work/bin/ExchangeOracle.dll" --unicode-map > "$work/unicode-map.json"
python3 - "$work" <<'PY'
import json, pathlib, sys
work=pathlib.Path(sys.argv[1])
for name in ['expected.json','mvc-methods.json','unicode-map.json']:
    assert json.loads(pathlib.Path(name).read_text()) == json.loads((work/name).read_text()), name+' differs from recorded .NET reference'
print('Actual .NET reference reproduced all recorded catalog, MVC and scalar-casing fixtures')
PY
printf 'Reference build/results retained at %s\n' "$work"
