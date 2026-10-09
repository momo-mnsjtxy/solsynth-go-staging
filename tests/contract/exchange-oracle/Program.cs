// SPDX-License-Identifier: AGPL-3.0-only
// Reference harness derived only from licensed DysonNetwork WalletController.cs
// lines 536-544, 563-578, 590-619, 627-654 at 4ee2d0cebb9da8284a92483fee6dce795bf6947c.
// Uses Microsoft framework configuration/decimal/JSON APIs, no Shared assemblies,
// protocol schemas, generated bindings, service access, or network listener.
using System.Globalization;
using System.Text;
using System.Text.Json;
using Microsoft.Extensions.Configuration;

if (args.FirstOrDefault() == "--mvc-methods") { await MvcProbe.Run(); return; }
if (args.FirstOrDefault() == "--unicode-map")
{
    var mappings=new List<int[]>();
    for(var code=0;code<=0x10ffff;code++)
    {
        if(code>=0xd800&&code<=0xdfff)continue;
        var rune=new Rune(code);var upper=Rune.ToUpperInvariant(rune);
        if(upper!=rune&&string.Equals(rune.ToString(),upper.ToString(),StringComparison.OrdinalIgnoreCase))mappings.Add([code,upper.Value]);
    }
    Console.WriteLine(JsonSerializer.Serialize(mappings));return;
}
var input = JsonDocument.Parse(Console.In.ReadToEnd());
var serializer = new JsonSerializerOptions { PropertyNamingPolicy = JsonNamingPolicy.SnakeCaseLower };
var outputs = new List<object>();
foreach (var test in input.RootElement.EnumerateArray())
{
    var name = test.GetProperty("name").GetString();
    var configBuilt=false;
    try
    {
        var builder = new ConfigurationBuilder();
        if (test.TryGetProperty("configs", out var configs))
            foreach (var config in configs.EnumerateArray())
                builder.AddJsonStream(new MemoryStream(Encoding.UTF8.GetBytes(config.GetString()!)));
        if (test.TryGetProperty("providers", out var providers))
            foreach (var provider in providers.EnumerateArray())
                builder.AddInMemoryCollection(provider.EnumerateArray().Select(pair =>
                    new KeyValuePair<string, string?>(pair.GetProperty("key").GetString()!,
                        pair.GetProperty("value").ValueKind == JsonValueKind.Null ? null : pair.GetProperty("value").GetString())));
        var configuration = builder.Build();
        using var configurationLifetime = (IDisposable)configuration;
        configBuilt=true;
        var options = new List<WalletExchangeOption>();
        foreach (var exchange in configuration.GetSection("Payment:CurrencyExchange").GetChildren())
        {
            if (!TryParseCurrencyAmount(exchange.Key, out var sourceAmount, out var sourceCurrency)) continue;
            if (!TryParseCurrencyAmount(exchange.Value, out var targetAmount, out var targetCurrency) || targetAmount <= 0 ||
                string.Equals(sourceCurrency, targetCurrency, StringComparison.OrdinalIgnoreCase))
                throw new InvalidOperationException($"Invalid currency exchange configuration for '{exchange.Key}'.");
            options.Add(new WalletExchangeOption { SourceCurrency=sourceCurrency, SourceAmount=sourceAmount, TargetCurrency=targetCurrency, TargetAmount=targetAmount });
        }
        outputs.Add(new { name, status=200, body=JsonSerializer.Serialize(options,serializer), config_error=(string?)null });
    }
    catch (InvalidOperationException) when (configBuilt)
    {
        // Public own-repository API error documentation specifies null omission;
        // this explicitly constructed shape does not import excluded ApiError.
        outputs.Add(new { name, status=500, body="{\"code\":\"WALLET_EXCHANGE_CONFIGURATION_INVALID\",\"message\":\"Currency exchange configuration is invalid.\",\"status\":500}", config_error=(string?)null });
    }
    catch (Exception error)
    {
        outputs.Add(new { name, status=0, body=(string?)null, config_error=error.GetType().FullName });
    }
}
Console.WriteLine(JsonSerializer.Serialize(outputs));

static bool TryParseCurrencyAmount(string? value,out decimal amount,out string currency)
{
    amount=0;currency=string.Empty;
    if(string.IsNullOrWhiteSpace(value))return false;
    var trimmed=value.Trim();var separator=0;
    while(separator<trimmed.Length&&(char.IsDigit(trimmed[separator])||trimmed[separator]=='.'))separator++;
    if(separator==0||separator==trimmed.Length)return false;
    if(!decimal.TryParse(trimmed[..separator],NumberStyles.AllowDecimalPoint,CultureInfo.InvariantCulture,out amount)||amount<=0)return false;
    currency=trimmed[separator..].Trim();return currency.Length>0;
}

sealed class WalletExchangeOption
{
    public string SourceCurrency {get;set;}=null!;
    public decimal SourceAmount {get;set;}
    public string TargetCurrency {get;set;}=null!;
    public decimal TargetAmount {get;set;}
}
