// SPDX-License-Identifier: AGPL-3.0-only
// Independently authored synthetic fixture from licensed BOT_CHAT_API.md's
// command/parameter tables. NOT the excluded shared model or an EF/Npgsql oracle.
using System.Text.Json;
using System.Text.Json.Serialization;

var options = new JsonSerializerOptions {
    PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower,
    PropertyNameCaseInsensitive = false,
    UnmappedMemberHandling = JsonUnmappedMemberHandling.Disallow,
    RespectNullableAnnotations = true
};
var inputs = new (string Name, string Input)[] {
    ("empty", "[]"),
    ("null-list-fallback", "null"),
    ("optional-omitted", "[{\"name\":\"help\",\"description\":\"Help\"}]"),
    ("explicit-nulls", "[{\"name\":\"help\",\"description\":\"Help\",\"usage\":null,\"parameters\":null}]"),
    ("empty-parameters", "[{\"name\":\"help\",\"description\":\"Help\",\"usage\":\"/help\",\"parameters\":[]}]"),
    ("parameters", "[{\"name\":\"help\",\"description\":\"Help\",\"usage\":\"/help [topic]\",\"parameters\":[{\"name\":\"topic\",\"required\":false},{\"name\":\"count\",\"description\":\"Count\",\"required\":true,\"type\":\"int\"}]}]"),
    ("unknown-command-field", "[{\"name\":\"help\",\"description\":\"Help\",\"secret\":\"fixture\"}]"),
    ("unknown-parameter-field", "[{\"name\":\"help\",\"description\":\"Help\",\"parameters\":[{\"name\":\"topic\",\"required\":false,\"extra\":1}]}]"),
    ("pascal-case", "[{\"Name\":\"help\",\"Description\":\"Help\"}]"),
    ("missing-name", "[{\"description\":\"Help\"}]"),
    ("null-name", "[{\"name\":null,\"description\":\"Help\"}]"),
    ("missing-description", "[{\"name\":\"help\"}]"),
    ("wrong-usage", "[{\"name\":\"help\",\"description\":\"Help\",\"usage\":3}]"),
    ("missing-required", "[{\"name\":\"help\",\"description\":\"Help\",\"parameters\":[{\"name\":\"topic\"}]}]"),
    ("null-required", "[{\"name\":\"help\",\"description\":\"Help\",\"parameters\":[{\"name\":\"topic\",\"required\":null}]}]"),
    ("wrong-required", "[{\"name\":\"help\",\"description\":\"Help\",\"parameters\":[{\"name\":\"topic\",\"required\":\"false\"}]}]"),
    ("null-command", "[null]"),
    ("null-parameter", "[{\"name\":\"help\",\"description\":\"Help\",\"parameters\":[null]}]"),
    ("non-array", "{}")
};
var results = new List<object>();
foreach (var (name, input) in inputs) {
    try {
        var commands = JsonSerializer.Deserialize<List<DocumentedCommand>>(input, options) ?? [];
        if (commands.Any(c => c is null || c.Parameters?.Any(p => p is null) == true))
            throw new JsonException("Null members are outside this selected fixture policy.");
        results.Add(new { name, input, accepted = true, output = JsonSerializer.Serialize(commands, options) });
    } catch (JsonException) {
        results.Add(new { name, input, accepted = false, output = (string?)null });
    }
}
Console.WriteLine(JsonSerializer.Serialize(results, new JsonSerializerOptions { WriteIndented = true }));

public sealed class DocumentedCommand {
    public required string Name { get; init; }
    public required string Description { get; init; }
    public string? Usage { get; init; }
    public List<DocumentedParameter>? Parameters { get; init; }
}
public sealed class DocumentedParameter {
    public required string Name { get; init; }
    public string? Description { get; init; }
    public required bool Required { get; init; }
    public string? Type { get; init; }
}
