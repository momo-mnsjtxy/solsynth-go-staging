// Synthetic .NET standard-library observation, no network/server/shared SDK.
using System.Text;
using System.Text.Json;
var lower = new List<int[]>();
var whitespace = new List<int>();
for (int value=0; value<=0x10ffff; value++) {
    if (!Rune.IsValid(value)) continue;
    var source = new Rune(value).ToString();
    var result = source.ToLowerInvariant();
    var runes = result.EnumerateRunes().ToArray();
    if (runes.Length != 1) throw new InvalidOperationException("Unexpected case expansion.");
    if (runes[0].Value != value) lower.Add(new[]{value,runes[0].Value});
    if (source.Trim().Length == 0) whitespace.Add(value);
}
File.WriteAllText(args[0], JsonSerializer.Serialize(new{lower,whitespace})+"\n");
