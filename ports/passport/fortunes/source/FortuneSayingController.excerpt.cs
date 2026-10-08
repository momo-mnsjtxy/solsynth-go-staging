// SPDX-License-Identifier: AGPL-3.0-only
// Exact non-corpus excerpts: source lines 1-17 and 123-143. Corpus omitted.
using DysonNetwork.Shared.Capabilities;
using DysonNetwork.Shared.Networking;
using Microsoft.AspNetCore.Mvc;

namespace DysonNetwork.Passport.Account;

public record FortuneSaying(
    string Content,
    string Source,
    string Language
);

[ApiController]
[Route("/api/fortune")]
[ApiFeature("fortune", Revision = 1)]
public class FortuneSayingController : ControllerBase
{

    [HttpGet]
    public ActionResult<List<FortuneSaying>> ListFortunes()
    {
        return Ok(Sayings);
    }

    [HttpGet("random")]
    public ActionResult<List<FortuneSaying>> GetRandomFortunes([FromQuery] string? language)
    {
        var filteredSayings = string.IsNullOrEmpty(language)
            ? Sayings
            : Sayings.Where(s => s.Language.Equals(language, StringComparison.OrdinalIgnoreCase)).ToArray();

        if (filteredSayings.Length == 0)
            return NotFound(new ApiError { Code = "PASSPORT_FORTUNE_NOT_FOUND", Message = "No fortunes found for the specified language.", Status = 404, TraceId = HttpContext.TraceIdentifier });

        var random = new Random();
        var randomSaying = filteredSayings[random.Next(filteredSayings.Length)];

        return Ok(new List<FortuneSaying> { randomSaying });
    }
