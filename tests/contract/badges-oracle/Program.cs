// Framework-only in-memory MVC oracle; never starts a network listener.
using System.Text.Json;
using DysonNetwork.Passport.Progression;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Http;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;
using Microsoft.Extensions.Options;

const string svg="<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 2 2\"><path d=\"M0 0h2v2H0z\"/></svg>\n";
var root=Path.Combine(Path.GetTempPath(),"solsynth-badges-oracle-"+Guid.NewGuid().ToString("N"));
Directory.CreateDirectory(root);
try
{
    var ready=Path.Combine(root,"ready");var empty=Path.Combine(root,"empty");
    Directory.CreateDirectory(ready);Directory.CreateDirectory(empty);
    await File.WriteAllTextAsync(Path.Combine(ready,"draw.svg"),svg);
    await File.WriteAllBytesAsync(Path.Combine(ready,"empty.svg"),[]);
    var options=new BadgesOptions();
    var builder=WebApplication.CreateBuilder(new WebApplicationOptions { Args=[] });
    builder.Logging.ClearProviders();
    builder.Services.AddSingleton<IOptions<BadgesOptions>>(Options.Create(options));
    builder.Services.AddControllers().AddApplicationPart(typeof(BadgesDiscoveryController).Assembly)
        .AddJsonOptions(o=>o.JsonSerializerOptions.PropertyNamingPolicy=JsonNamingPolicy.SnakeCaseLower);
    await using var app=builder.Build();app.UseRouting();app.UseEndpoints(e=>e.MapControllers());
    var pipeline=((IApplicationBuilder)app).Build();
    var input=JsonDocument.Parse(Console.In.ReadToEnd());var results=new List<object>();
    foreach(var test in input.RootElement.EnumerateArray())
    {
        options.IconsPath=test.GetProperty("mode").GetString() switch {
            "none"=>null,"whitespace"=>"\u00a0","missing"=>Path.Combine(root,"missing"),
            "empty"=>empty,"ready"=>ready,"file"=>Path.Combine(ready,"draw.svg"),_=>throw new Exception("unknown fixture mode")};
        using var scope=app.Services.CreateScope();
        var context=new DefaultHttpContext {RequestServices=scope.ServiceProvider,TraceIdentifier=test.TryGetProperty("trace_id",out var trace)?trace.GetString()!:"badge-reference-trace"};
        context.Request.Method=test.GetProperty("method").GetString()!;context.Request.Path=test.GetProperty("path").GetString();
        foreach(var header in test.GetProperty("headers").EnumerateObject())context.Request.Headers[header.Name]=header.Value.GetString();
        context.Response.Body=new MemoryStream();
        await pipeline(context);context.Response.Body.Position=0;
        results.Add(new { name=test.GetProperty("name").GetString(),status=context.Response.StatusCode,
            headers=context.Response.Headers.ToDictionary(p=>p.Key.ToLowerInvariant(),p=>p.Value.ToString()),
            body=await new StreamReader(context.Response.Body).ReadToEndAsync() });
    }
    Console.WriteLine(JsonSerializer.Serialize(results));
}
finally{Directory.Delete(root,true);}
