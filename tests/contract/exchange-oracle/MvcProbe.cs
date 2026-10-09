// Framework-only in-memory MVC request pipeline. No network listener or Shared types.
using System.Text.Json;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Mvc;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;

static class MvcProbe
{
    public static async Task Run()
    {
        var builder=WebApplication.CreateBuilder(new WebApplicationOptions { Args=[] });
        builder.Logging.ClearProviders();
        builder.Services.AddControllers().AddApplicationPart(typeof(ProbeController).Assembly);
        await using var app=builder.Build();
        app.UseRouting();
        app.UseEndpoints(endpoints=>endpoints.MapControllers());
        var pipeline=((IApplicationBuilder)app).Build();
        var results=new List<object>();
        foreach(var (method,path) in new[]{("GET","/api/wallets/exchange"),("HEAD","/api/wallets/exchange"),("POST","/api/wallets/exchange"),("GET","/API/WALLETS/EXCHANGE"),("GET","/api/wallets/exchange/"),("GET","/api/wallets/exchange//")})
        {
            using var scope=app.Services.CreateScope();
            var context=new DefaultHttpContext { RequestServices=scope.ServiceProvider };
            context.Request.Method=method;context.Request.Path=path;
            context.Response.Body=new MemoryStream();
            await pipeline(context);
            context.Response.Body.Position=0;
            results.Add(new {method,path,status=context.Response.StatusCode,allow=context.Response.Headers.Allow.ToString(),body=await new StreamReader(context.Response.Body).ReadToEndAsync()});
        }
        Console.WriteLine(JsonSerializer.Serialize(results));
    }
}

[ApiController]
public sealed class ProbeController:ControllerBase
{
    [HttpGet("/api/wallets/exchange")]
    public IActionResult Get()=>Ok(new[]{new {source_currency="USD",source_amount=1m,target_currency="EUR",target_amount=2m}});
}
