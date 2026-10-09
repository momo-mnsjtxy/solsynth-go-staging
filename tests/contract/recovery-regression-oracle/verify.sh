#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
DOTNET_BIN="${DOTNET_BIN:-dotnet}"
"$DOTNET_BIN" --version | grep -E '^10\.'
work=$(mktemp -d /tmp/solsynth-regression-oracle.XXXXXXXX)
export DOTNET_CLI_HOME="$work/home" DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1
export DOTNET_GENERATE_ASPNET_CERTIFICATE=false DOTNET_EnableDiagnostics=0
"$DOTNET_BIN" build RegressionOracle.csproj --disable-build-servers -m:1 \
  -p:UseSharedCompilation=false --configfile NuGet.Config \
  --artifacts-path "$work/artifacts" -o "$work/bin"
"$DOTNET_BIN" "$work/bin/RegressionOracle.dll" < cases.json > "$work/expected.json"
python3 - "$work/expected.json" <<'PY'
import json,pathlib,sys
assert json.loads(pathlib.Path(sys.argv[1]).read_text()) == json.loads(pathlib.Path('expected.json').read_text()), 'regression oracle changed'
print('Independent .NET benign parser and route-casing regression fixtures reproduced')
PY
printf 'Reference build/results retained at %s\n' "$work"
