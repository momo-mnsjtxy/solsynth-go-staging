// SPDX-License-Identifier: AGPL-3.0-only
// Written only from own-repository docs/SHARED_API_ERRORS.md public wire contract.
using System.Text.Json.Serialization;
namespace DysonNetwork.Passport.Progression;
public sealed class ApiError
{
    public string Code {get;set;} = "";
    public string Message {get;set;} = "";
    [JsonIgnore(Condition=JsonIgnoreCondition.WhenWritingNull)] public int? Status {get;set;}
    [JsonIgnore(Condition=JsonIgnoreCondition.WhenWritingNull)] public string? Detail {get;set;}
    [JsonPropertyName("traceId"),JsonIgnore(Condition=JsonIgnoreCondition.WhenWritingNull)] public string? TraceId {get;set;}
    [JsonIgnore(Condition=JsonIgnoreCondition.WhenWritingNull)] public object? Errors {get;set;}
    [JsonIgnore(Condition=JsonIgnoreCondition.WhenWritingNull)] public object? Meta {get;set;}
}
