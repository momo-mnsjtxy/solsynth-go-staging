#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../../.."
export DOTNET_GENERATE_ASPNET_CERTIFICATE=false DOTNET_EnableDiagnostics=0 DOTNET_CLI_TELEMETRY_OPTOUT=1 DOTNET_NOLOGO=1 DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1
calendar_work=$(mktemp -d /tmp/solsynth-calendar-oracle.XXXXXXXX)
export DOTNET_CLI_HOME="$calendar_work/home"
dotnet_bin="${DOTNET_BIN:-/workspace/shared/toolchains/dotnet-10.0.401/dotnet}"
source_dir="$PWD/ports/passport/notabledays/oracle"
"$dotnet_bin" build "$source_dir/CalendarFacts.csproj" --disable-build-servers -m:1 -p:UseSharedCompilation=false --configfile "$source_dir/NuGet.Config" --artifacts-path "$calendar_work/artifacts" -o "$calendar_work/bin" > "$calendar_work/build.log"
"$dotnet_bin" "$calendar_work/bin/CalendarFacts.dll" > "$calendar_work/results.json"
python3 - "$source_dir/results.json" "$calendar_work/results.json" <<'PY'
import json,sys
expected,actual=map(lambda p:json.load(open(p)),sys.argv[1:])
assert expected==actual, 'calendar fact oracle changed'
print('Verified',actual['checked_cases'],'lunar resolutions using',actual['runtime'])
PY
