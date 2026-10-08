// SPDX-License-Identifier: AGPL-3.0-only
// Licensed authored algorithm excerpts only. Prompt, corpus and fallback prose omitted.
// Original source lines 46-48
    private const int FortuneReportVersion = 2;
    private const int FortuneTipTitleMaxLength = 48;
    private const int FortuneTipContentMaxLength = 180;
// Original source lines 835-902
    public SnCheckInResult PrepareCheckInResultForResponse(
        SnAccount account,
        SnCheckInResult result,
        int version = FortuneReportVersion
    )
    {
        if (version < FortuneReportVersion || result.FortuneReport is not null)
            result.FortuneReport = CompleteFortuneReport(account, result);
        return result;
    }

    public async Task GenerateCheckInFortuneAsync(Guid checkInResultId, SnAccount account)
    {
        var result = await db.AccountCheckInResults.FirstOrDefaultAsync(x => x.Id == checkInResultId);
        if (result is null || result.FortuneReport is not null)
            return;

        var accountProfile = await accountGrpc.GetAccountAsync(
            new DyGetAccountRequest { Id = account.Id.ToString() }
        );
        var profile = accountProfile.Profile;
        var userTimeZone = !string.IsNullOrEmpty(profile?.TimeZone)
            ? DateTimeZoneProviders.Tzdb.GetZoneOrNull(profile.TimeZone) ?? DateTimeZone.Utc
            : DateTimeZone.Utc;
        var checkInDate = result.CreatedAt.InZone(userTimeZone).Date;
        var accountBirthday = profile?.Birthday?.ToInstant();
        var birthdayDate = accountBirthday?.InZone(userTimeZone).Date;
        var isBirthday = birthdayDate.HasValue
            && birthdayDate.Value.Month == checkInDate.Month
            && birthdayDate.Value.Day == checkInDate.Day;
        var publicEvents = await GetPublicEventsForDate(account.Id, checkInDate, userTimeZone);
        var notableDays = await GetNotableDaysForDate(account.Region, checkInDate);
        var recentFortunes = await db
            .AccountCheckInResults.AsNoTracking()
            .Where(x => x.AccountId == account.Id && x.Id != result.Id && x.FortuneReport != null)
            .OrderByDescending(x => x.CreatedAt)
            .Take(8)
            .ToListAsync();
        var generation = await GenerateCheckInFortune(
            account,
            checkInDate,
            isBirthday,
            result.BackdatedFrom.HasValue,
            result.Level,
            CloneFortuneTips(result.Tips),
            publicEvents,
            notableDays,
            recentFortunes
        );
        if (generation is null)
            return;

        result.Tips = generation.Tips;
        result.FortuneReport = generation.Report;
        await db.SaveChangesAsync();
    }

    private static List<CheckInFortuneTip> CloneFortuneTips(List<CheckInFortuneTip> tips)
    {
        return tips
            .Select(t => new CheckInFortuneTip
            {
                IsPositive = t.IsPositive,
                Title = t.Title,
                Content = t.Content,
            })
            .ToList();
    }
// Original source lines 904-1021
    private async Task<CheckInFortuneGeneration?> GenerateCheckInFortune(
        SnAccount account,
        LocalDate checkInDate,
        bool isBirthday,
        bool isBackdated,
        CheckInResultLevel level,
        List<CheckInFortuneTip> tips,
        List<SnUserCalendarEvent> publicEvents,
        List<NotableDay> notableDays,
        List<SnCheckInResult> recentFortunes
    )
    {
        var model = configuration.GetValue<string>("CheckIn:Fortune:Model");
        var timeoutSeconds = Math.Clamp(
            configuration.GetValue<int?>("CheckIn:Fortune:TimeoutSeconds") ?? 90,
            5,
            900
        );
        var temperature = Math.Clamp(
            configuration.GetValue<double?>("CheckIn:Fortune:Temperature") ?? 0.95,
            0,
            2
        );
        var topP = Math.Clamp(
            configuration.GetValue<double?>("CheckIn:Fortune:TopP") ?? 0.98,
            0,
            1
        );

        logger.LogInformation(
            "Starting MiChan check-in fortune generation for {AccountId}: model={Model}, timeout={TimeoutSeconds}s, temperature={Temperature}, top_p={TopP}, level={Level}, date={CheckInDate}",
            account.Id,
            string.IsNullOrWhiteSpace(model) ? "default" : model,
            timeoutSeconds,
            temperature,
            topP,
            level,
            checkInDate
        );

        try
        {
            var request = new DyCompletePersonalityRequest
            {
                AgentId = "michan",
                AccountId = account.Id.ToString(),
                Message = BuildCheckInFortunePrompt(
                    account,
                    checkInDate,
                    isBirthday,
                    isBackdated,
                    level,
                    tips,
                    publicEvents,
                    notableDays,
                    recentFortunes
                ),
                Temperature = temperature,
                TopP = topP,
            };
            if (!string.IsNullOrWhiteSpace(model))
                request.Model = model;

            var response = await personality.CompleteAsync(
                request,
                deadline: DateTime.UtcNow.AddSeconds(timeoutSeconds)
            );

            var generation = TryParseFortuneGeneration(response.Content, tips);
            if (generation is not null)
            {
                logger.LogInformation(
                    "MiChan check-in fortune generation succeeded for {AccountId}: response_chars={ResponseLength}, tips={TipCount}",
                    account.Id,
                    response.Content.Length,
                    generation.Tips.Count
                );
                return generation;
            }

            logger.LogWarning(
                "MiChan returned an invalid check-in fortune generation for {AccountId}: {Content}",
                account.Id,
                TruncateLogText(response.Content, 512)
            );
        }
        catch (OperationCanceledException ex)
        {
            logger.LogWarning(
                "MiChan check-in fortune generation was canceled or timed out for {AccountId}: {Message}",
                account.Id,
                ex.Message
            );
        }
        catch (RpcException ex) when (ex.StatusCode is StatusCode.Cancelled or StatusCode.DeadlineExceeded)
        {
            logger.LogWarning(
                "MiChan check-in fortune generation was canceled or timed out for {AccountId}: {Message}",
                account.Id,
                ex.Message
            );
        }
        catch (RpcException ex)
        {
            logger.LogWarning(
                ex,
                "MiChan check-in fortune generation failed through gRPC for {AccountId}",
                account.Id
            );
        }
        catch (Exception ex)
        {
            logger.LogWarning(ex, "Failed to generate MiChan check-in fortune report for {AccountId}", account.Id);
        }

        return null;
    }

// Original source lines 1320-1434
    private static CheckInFortuneReport TrimFortuneReport(CheckInFortuneReport report)
    {
        report.Version = FortuneReportVersion;
        report.Poem = TrimFortuneText(report.Poem, 120);
        report.Summary = TrimFortuneText(report.Summary, 160);
        report.SummaryDetail = TrimFortuneText(report.SummaryDetail, 360);
        report.Wish = TrimFortuneText(report.Wish, 120);
        report.Love = TrimFortuneText(report.Love, 120);
        report.Study = TrimFortuneText(report.Study, 120);
        report.Career = TrimFortuneText(report.Career, 120);
        report.Health = TrimFortuneText(report.Health, 120);
        report.LostItem = TrimFortuneText(report.LostItem, 120);
        report.LuckyColor = TrimFortuneText(report.LuckyColor, 40);
        report.LuckyDirection = TrimFortuneText(report.LuckyDirection, 40);
        report.LuckyTime = TrimFortuneText(report.LuckyTime, 40);
        report.LuckyItem = TrimFortuneText(report.LuckyItem, 40);
        report.LuckyAction = TrimFortuneText(report.LuckyAction, 120);
        report.AvoidAction = TrimFortuneText(report.AvoidAction, 120);
        report.Ritual = TrimFortuneText(report.Ritual, 160);
        return report;
    }

    private static string? ExtractJsonObject(string content)
    {
        var trimmed = StripCodeFence(content);

        var start = trimmed.IndexOf('{');
        var end = trimmed.LastIndexOf('}');
        return start >= 0 && end > start ? trimmed[start..(end + 1)] : null;
    }

    private static bool IsValidFortuneReport(CheckInFortuneReport report)
    {
        return report.Version == FortuneReportVersion
            && !string.IsNullOrWhiteSpace(report.Poem)
            && !string.IsNullOrWhiteSpace(report.Summary)
            && !string.IsNullOrWhiteSpace(report.SummaryDetail)
            && !string.IsNullOrWhiteSpace(report.Wish)
            && !string.IsNullOrWhiteSpace(report.Love)
            && !string.IsNullOrWhiteSpace(report.Study)
            && !string.IsNullOrWhiteSpace(report.Career)
            && !string.IsNullOrWhiteSpace(report.Health)
            && !string.IsNullOrWhiteSpace(report.LostItem)
            && !string.IsNullOrWhiteSpace(report.LuckyColor)
            && !string.IsNullOrWhiteSpace(report.LuckyDirection)
            && !string.IsNullOrWhiteSpace(report.LuckyTime)
            && !string.IsNullOrWhiteSpace(report.LuckyItem)
            && !string.IsNullOrWhiteSpace(report.LuckyAction)
            && !string.IsNullOrWhiteSpace(report.AvoidAction)
            && !string.IsNullOrWhiteSpace(report.Ritual);
    }

    private static bool HasValidFortuneTipBalance(List<CheckInFortuneTip> tips)
    {
        var positiveCount = tips.Count(t => t.IsPositive);
        var negativeCount = tips.Count - positiveCount;
        return positiveCount is 2 or 3 && negativeCount is 1 or 2;
    }

    private static List<CheckInFortuneTip> NormalizeGeneratedFortuneTips(
        List<CheckInFortuneTip>? generatedTips,
        List<CheckInFortuneTip> seedTips
    )
    {
        if (generatedTips is null || generatedTips.Count == 0)
            return [];

        var result = generatedTips
            .Where(t => !string.IsNullOrWhiteSpace(t.Title) && !string.IsNullOrWhiteSpace(t.Content))
            .Where(t => !ReusesSeedFortuneTips([t], seedTips))
            .GroupBy(t => NormalizeFortuneText($"{t.Title}{t.Content}"))
            .Select(g => g.First())
            .Take(4)
            .Select(t => new CheckInFortuneTip
            {
                IsPositive = t.IsPositive,
                Title = TrimFortuneText(t.Title, FortuneTipTitleMaxLength),
                Content = TrimFortuneText(t.Content, FortuneTipContentMaxLength),
            })
            .ToList();

        return result;
    }

    private static bool HasDuplicateFortuneTips(List<CheckInFortuneTip> tips)
    {
        return tips.Select(t => NormalizeFortuneText(t.Title)).Distinct().Count() != tips.Count
            || tips.Select(t => NormalizeFortuneText(t.Content)).Distinct().Count() != tips.Count;
    }

    private static bool ReusesSeedFortuneTips(
        List<CheckInFortuneTip> generatedTips,
        List<CheckInFortuneTip> seedTips
    )
    {
        var seedTexts = seedTips
            .SelectMany(t => new[] { t.Title, t.Content })
            .Select(NormalizeFortuneText)
            .Where(t => t.Length > 0)
            .ToHashSet(StringComparer.Ordinal);

        return generatedTips
            .SelectMany(t => new[] { t.Title, t.Content })
            .Select(NormalizeFortuneText)
            .Any(seedTexts.Contains);
    }

    private static string NormalizeFortuneText(string? value)
    {
        if (string.IsNullOrWhiteSpace(value))
            return string.Empty;

        return new string(value.Where(c => !char.IsWhiteSpace(c) && !char.IsPunctuation(c)).ToArray())
            .ToLowerInvariant();
    }
