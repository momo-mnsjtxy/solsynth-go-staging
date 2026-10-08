// SPDX-License-Identifier: AGPL-3.0-only
// Independently authored, deterministic calendar-fact oracle using only the
// public installed .NET API. No upstream model, copied runtime table, networking,
// database, user profile, certificate access, or external package dependency.
using System.Globalization;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Runtime.InteropServices;

CultureInfo.CurrentCulture = CultureInfo.InvariantCulture;
var calendar = new ChineseLunisolarCalendar();
var firstYear = calendar.GetYear(calendar.MinSupportedDateTime);
var lastYear = calendar.GetYear(calendar.MaxSupportedDateTime);
var years = new List<object>();
for (var year = firstYear; year <= lastYear; year++) {
    var months = Enumerable.Range(1, calendar.GetMonthsInYear(year, 1))
        .Select(month => calendar.GetDaysInMonth(year, month, 1)).ToArray();
    years.Add(new { year, new_year = calendar.ToDateTime(year, 1, 1, 0, 0, 0, 0, 1).ToString("yyyy-MM-dd"),
        leap_slot = calendar.GetLeapMonth(year, 1), months });
}
string? Resolve(int year, int month, int day) {
    foreach (var lunarYear in new[] { year, year - 1 }) {
        if (lunarYear < firstYear || lunarYear > lastYear) continue;
        var leap = calendar.GetLeapMonth(lunarYear, 1);
        var slot = leap > 0 && month >= leap ? month + 1 : month;
        try {
            var solar = calendar.ToDateTime(lunarYear, slot, day, 0, 0, 0, 0, 1);
            if (solar.Year == year) return solar.ToString("yyyy-MM-dd");
        } catch (ArgumentOutOfRangeException) {
            if (month == 12 && day == 30) {
                var solar = calendar.ToDateTime(lunarYear, slot, 29, 0, 0, 0, 0, 1);
                if (solar.Year == year) return solar.ToString("yyyy-MM-dd");
            }
        }
    }
    return null;
}
var facts = new StringBuilder();
var count = 0;
var lastResolutionYear = calendar.MaxSupportedDateTime.Year;
for (var year = firstYear; year <= lastResolutionYear; year++)
 for (var month = 1; month <= 12; month++)
  for (var day = 1; day <= 31; day++) {
    facts.Append(year).Append('|').Append(month).Append('|').Append(day).Append('|')
        .Append(Resolve(year, month, day) ?? "null").Append('\n');
    count++;
  }
var hash = Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(facts.ToString()))).ToLowerInvariant();
Console.WriteLine(JsonSerializer.Serialize(new { runtime = RuntimeInformation.FrameworkDescription,
    minimum = calendar.MinSupportedDateTime.ToString("yyyy-MM-dd"), maximum = calendar.MaxSupportedDateTime.ToString("yyyy-MM-dd"),
    first_lunar_year = firstYear, last_lunar_year = lastYear, last_resolution_year = lastResolutionYear,
    years, checked_cases = count, resolution_sha256 = hash }));
