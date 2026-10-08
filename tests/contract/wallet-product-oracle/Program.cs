// SPDX-License-Identifier: AGPL-3.0-only
// Bounded binder/projection/serializer reference. No database, payment, HTTP
// listener, remote provider, Shared assembly, protocol binding, or auth code.
using System.Globalization;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
using Microsoft.Extensions.Configuration;

// Freeze the framework's UTF-16 char classification/casing facts for the
// source-backed naming-policy state machine. No implementation is copied here.
if(args.FirstOrDefault()=="--naming-bmp")
{
    var table=new List<object>();
    for(var code=0;code<=0xffff;code++)
    {
        var character=(char)code;
        string? category=char.GetUnicodeCategory(character) switch
        {
            UnicodeCategory.UppercaseLetter=>"Lu",
            UnicodeCategory.LowercaseLetter=>"Ll",
            UnicodeCategory.DecimalDigitNumber=>"Nd",
            UnicodeCategory.SpaceSeparator=>"Zs",
            _=>null
        };
        if(category is not null)
            table.Add(new {code,category,lower=(int)char.ToLowerInvariant(character)});
    }
    Console.WriteLine(JsonSerializer.Serialize(table));
    return;
}

CultureInfo.CurrentCulture=CultureInfo.InvariantCulture;
CultureInfo.CurrentUICulture=CultureInfo.InvariantCulture;
var input=JsonDocument.Parse(Console.In.ReadToEnd());
var serializer=new JsonSerializerOptions {
    PropertyNamingPolicy=JsonNamingPolicy.SnakeCaseLower,
    DictionaryKeyPolicy=JsonNamingPolicy.SnakeCaseLower,
    NumberHandling=JsonNumberHandling.AllowNamedFloatingPointLiterals
};
var results=new List<object>();
foreach(var test in input.RootElement.EnumerateArray())
{
    var phase="configuration";
    try
    {
        var builder=new ConfigurationBuilder();
        if(test.TryGetProperty("configs",out var configs))
            foreach(var config in configs.EnumerateArray())
                builder.AddJsonStream(new MemoryStream(Encoding.UTF8.GetBytes(config.GetString()!)));
        if(test.TryGetProperty("providers",out var providers))
            foreach(var provider in providers.EnumerateArray())
                builder.AddInMemoryCollection(provider.EnumerateArray().Select(pair=>
                    new KeyValuePair<string,string?>(pair.GetProperty("key").GetString()!,
                        pair.GetProperty("value").ValueKind==JsonValueKind.Null?null:pair.GetProperty("value").GetString())));
        var configuration=builder.Build();
        using var lifetime=(IDisposable)configuration;
        phase="binding";
        // Exact source service expression; FixtureDefaults affects only the
        // known excluded initializer, as explained in OwnedProjection.cs.
        var options=configuration.GetSection("Payment:Product").Get<ProductOptions>();
        var definition=options?.GoldCurrency??new WalletProductDefinitionOptions();
        if(test.TryGetProperty("direct_definition",out var direct))
            definition=JsonSerializer.Deserialize<WalletProductDefinitionOptions>(direct.GetRawText())!;
        phase="projection";
        var item=OwnedProjection.Catalog(definition);
        phase="serialization";
        var body=JsonSerializer.Serialize(item,serializer);
        results.Add(new {name=test.GetProperty("name").GetString(),body,error=(string?)null,error_phase=(string?)null});
    }
    catch(Exception error)
    {
        results.Add(new {name=test.GetProperty("name").GetString(),body=(string?)null,error=error.GetType().FullName,error_phase=phase});
    }
}
Console.WriteLine(JsonSerializer.Serialize(results));
