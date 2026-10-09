// SPDX-License-Identifier: AGPL-3.0-only
// Independent benign parser and route-casing regression reference, 2026-10-08.
// Uses retained licensed controller/error-contract adaptation and .NET framework
// only. No excluded assembly, external service, or network listener is used.
using System.Text;
using System.Text.Json;
using DysonNetwork.Passport.Progression;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.Configuration;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;

var input=JsonDocument.Parse(Console.In.ReadToEnd());
var configResults=new List<object>();
foreach(var test in input.RootElement.GetProperty("configs").EnumerateArray())
{
    string? error=null;
    try
    {
        using var stream=new MemoryStream(Encoding.UTF8.GetBytes(test.GetProperty("json").GetString()!));
        using var configuration=(IDisposable)new ConfigurationBuilder().AddJsonStream(stream).Build();
    }
    catch(Exception exception){error=exception.GetType().FullName;}
    configResults.Add(new {name=test.GetProperty("name").GetString(),accepted=error is null,error});
}
var builder=WebApplication.CreateBuilder(new WebApplicationOptions {Args=[]});
builder.Logging.ClearProviders();
builder.Services.AddSingleton<IOptions<BadgesOptions>>(Options.Create(new BadgesOptions()));
builder.Services.AddControllers().AddApplicationPart(typeof(BadgesDiscoveryController).Assembly)
    .AddJsonOptions(o=>o.JsonSerializerOptions.PropertyNamingPolicy=JsonNamingPolicy.SnakeCaseLower);
await using var app=builder.Build();app.UseRouting();app.UseEndpoints(e=>e.MapControllers());
var pipeline=((IApplicationBuilder)app).Build();
var routeResults=new List<object>();
foreach(var test in input.RootElement.GetProperty("routes").EnumerateArray())
{
    using var scope=app.Services.CreateScope();
    var context=new DefaultHttpContext {RequestServices=scope.ServiceProvider,TraceIdentifier="regression-reference"};
    context.Request.Method="GET";context.Request.Path=test.GetProperty("path").GetString();
    context.Response.Body=new MemoryStream();await pipeline(context);
    routeResults.Add(new {name=test.GetProperty("name").GetString(),status=context.Response.StatusCode});
}
Console.WriteLine(JsonSerializer.Serialize(new {configs=configResults,routes=routeResults}));
