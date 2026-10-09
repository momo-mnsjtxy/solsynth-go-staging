// SPDX-License-Identifier: AGPL-3.0-only
// AccountEventService activity methods only; upstream blob 1f9891d9bfc97ffbc7306213e0d7f5a10d63b4ce.
// Original source lines 1587-1805
    public async Task<List<SnPresenceActivity>> GetActiveActivities(Guid userId)
    {
        var cacheKey = $"{ActivityCacheKey}{userId}";
        var cachedActivities = await cache.GetAsync<List<SnPresenceActivity>>(cacheKey);
        if (cachedActivities != null)
        {
            return cachedActivities;
        }

        var now = SystemClock.Instance.GetCurrentInstant();
        var activities = await db
            .PresenceActivities
            .Include(e => e.ActivityTags)
            .Where(e =>
                e.AccountId == userId && e.LeaseExpiresAt > now && e.DeletedAt == null
            )
            .OrderByDescending(e => e.StartedAt ?? e.CreatedAt)
            .ToListAsync();

        await cache.SetWithGroupsAsync(
            cacheKey,
            activities,
            [$"{AccountService.AccountCachePrefix}{userId}"],
            TimeSpan.FromMinutes(1)
        );
        return activities;
    }

    public async Task<Dictionary<Guid, List<SnPresenceActivity>>> GetActiveActivitiesBatch(
        List<Guid> userIds
    )
    {
        var results = new Dictionary<Guid, List<SnPresenceActivity>>();
        var cacheMissUserIds = new List<Guid>();

        // Try to get activities from cache first
        foreach (var userId in userIds)
        {
            var cacheKey = $"{ActivityCacheKey}{userId}";
            var cachedActivities = await cache.GetAsync<List<SnPresenceActivity>>(cacheKey);
            if (cachedActivities != null)
            {
                results[userId] = cachedActivities;
            }
            else
            {
                cacheMissUserIds.Add(userId);
            }
        }

        // If all activities were found in cache, return early
        if (cacheMissUserIds.Count == 0)
            return results;

        // Fetch remaining activities from database in a single query
        var now = SystemClock.Instance.GetCurrentInstant();
        var activitiesFromDb = await db
            .PresenceActivities.Where(e =>
                cacheMissUserIds.Contains(e.AccountId)
                && e.LeaseExpiresAt > now
                && e.DeletedAt == null
            )
            .ToListAsync();

        // Group activities by user ID and update cache
        var activitiesByUser = activitiesFromDb
            .GroupBy(a => a.AccountId)
            .ToDictionary(g => g.Key, g => g.ToList());

        foreach (var userId in cacheMissUserIds)
        {
            var userActivities = activitiesByUser.GetValueOrDefault(
                userId,
                new List<SnPresenceActivity>()
            );
            results[userId] = userActivities;

            // Update cache for this user
            var cacheKey = $"{ActivityCacheKey}{userId}";
            await cache.SetWithGroupsAsync(
                cacheKey,
                userActivities,
                [$"{AccountService.AccountCachePrefix}{userId}"],
                TimeSpan.FromMinutes(1)
            );
        }

        return results;
    }

    public async Task<(List<SnPresenceActivity>, int)> GetAllActivities(
        Guid userId,
        int offset = 0,
        int take = 20
    )
    {
        return await GetFilteredActivities(userId, offset, take);
    }

    public async Task<(List<SnPresenceActivity>, int)> GetFilteredActivities(
        Guid userId,
        int offset = 0,
        int take = 20,
        string? query = null,
        string? type = null,
        string? provider = null,
        string? referenceId = null,
        string? term = null,
        bool? isActive = null,
        IEnumerable<string>? tags = null,
        PresenceVisibility? visibility = null,
        string? catalogId = null,
        Instant? from = null,
        Instant? to = null
    )
    {
        var baseQuery = db.PresenceActivities.Where(e =>
            e.AccountId == userId && e.DeletedAt == null);

        if (!string.IsNullOrWhiteSpace(query))
        {
            var normalizedQuery = query.Trim().ToLowerInvariant();
            baseQuery = baseQuery.Where(e =>
                (e.Title != null && e.Title.Contains(query))
                || (e.Subtitle != null && e.Subtitle.Contains(query))
                || (e.Caption != null && e.Caption.Contains(query))
                || (e.ManualId != null && e.ManualId.Contains(query))
                || (e.Provider != null && e.Provider.Contains(query))
                || (e.ReferenceId != null && e.ReferenceId.Contains(query))
                || e.QueryableTerms.Contains(normalizedQuery));
        }

        if (!string.IsNullOrWhiteSpace(type))
        {
            var normalizedType = type.Trim();
            baseQuery = baseQuery.Where(e => e.Type == normalizedType);
        }

        if (!string.IsNullOrWhiteSpace(provider))
        {
            var normalizedProvider = provider.Trim();
            baseQuery = baseQuery.Where(e => e.Provider == normalizedProvider);
        }

        if (!string.IsNullOrWhiteSpace(referenceId))
        {
            var normalizedReferenceId = referenceId.Trim();
            baseQuery = baseQuery.Where(e => e.ReferenceId == normalizedReferenceId);
        }

        if (!string.IsNullOrWhiteSpace(term))
        {
            var normalizedTerm = term.Trim().ToLowerInvariant();
            baseQuery = baseQuery.Where(e => e.QueryableTerms.Contains(normalizedTerm));
        }

        var now = SystemClock.Instance.GetCurrentInstant();
        if (isActive == true)
        {
            baseQuery = baseQuery.Where(e => e.LeaseExpiresAt > now);
        }
        else if (isActive == false)
        {
            baseQuery = baseQuery.Where(e => e.LeaseExpiresAt <= now);
        }

        if (tags is not null)
        {
            var normalizedTags = tags
                .Where(t => !string.IsNullOrWhiteSpace(t))
                .Select(t => t.Trim())
                .Distinct(StringComparer.Ordinal)
                .ToList();
            if (normalizedTags.Count > 0)
            {
                foreach (var tagSlug in normalizedTags)
                {
                    baseQuery = baseQuery.Where(e => e.ActivityTags.Any(t => t.Slug == tagSlug));
                }
            }
        }

        if (visibility.HasValue)
        {
            baseQuery = baseQuery.Where(e => e.Visibility == visibility.Value);
        }

        if (!string.IsNullOrWhiteSpace(catalogId))
        {
            if (!Guid.TryParse(catalogId, out var parsedCatalogId))
                return ([], 0);
            baseQuery = baseQuery.Where(e => e.CatalogId == parsedCatalogId);
        }

        if (from.HasValue)
        {
            baseQuery = baseQuery.Where(e => e.StartedAt != null && e.StartedAt >= from.Value);
        }

        if (to.HasValue)
        {
            baseQuery = baseQuery.Where(e => e.StartedAt != null && e.StartedAt < to.Value);
        }

        var totalCount = await baseQuery.CountAsync();

        var activities = await baseQuery
            .Include(e => e.ActivityTags)
            .OrderByDescending(e => e.StartedAt ?? e.CreatedAt)
            .Skip(offset)
            .Take(take)
            .ToListAsync();

        foreach (var activity in activities)
            activity.Tags = activity.ActivityTags
                .Select(t => new SnPresenceTag { Slug = t.Slug, Name = t.Name })
                .ToList();

        return (activities, totalCount);
// Original source lines 1884-2473
    public async Task<SnPresenceActivity> SetActivity(SnPresenceActivity activity, int leaseMinutes)
    {
        if (leaseMinutes is < 1 or > 60)
            throw new ArgumentException("Lease minutes must be between 1 and 60");

        var now = SystemClock.Instance.GetCurrentInstant();
        activity.LeaseMinutes = leaseMinutes;
        activity.LeaseExpiresAt = now + Duration.FromMinutes(leaseMinutes);

        db.PresenceActivities.Add(activity);
        await db.SaveChangesAsync();

        PurgeActivityCache(activity.AccountId);

        await BroadcastPresenceActivitiesUpdated(activity.AccountId);

        return activity;
    }

    public async Task<SnPresenceActivity> StartActivitySession(
        Guid userId,
        string manualId,
        string? type,
        List<SnPresenceTag> tags,
        string? provider,
        string? referenceId,
        string? title,
        string? subtitle,
        string? caption,
        string? largeImage,
        string? smallImage,
        string? titleUrl,
        string? subtitleUrl,
        string[] queryableTerms,
        Dictionary<string, object?>? meta,
        string? catalogKey,
        string? catalogName,
        PresenceVisibility visibility,
        int leaseMinutes)
    {
        if (leaseMinutes is < 1 or > 60)
            throw new ArgumentException("Lease minutes must be between 1 and 60");

        var now = SystemClock.Instance.GetCurrentInstant();
        var normalizedTerms = NormalizeQueryableTerms(queryableTerms);
        var normalizedTags = tags
            .Where(t => !string.IsNullOrWhiteSpace(t.Slug))
            .GroupBy(t => t.Slug.Trim(), StringComparer.Ordinal)
            .Select(g => new SnPresenceTag
            {
                Slug = g.Key,
                Name = g.First().Name,
            })
            .ToList();

        var activeSession = await db.PresenceActivities.FirstOrDefaultAsync(e =>
            e.ManualId == manualId
            && e.AccountId == userId
            && e.LeaseExpiresAt > now
            && e.DeletedAt == null
            && e.EndedAt == null);

        SnPresenceCatalogItem? catalog = null;
        if (!string.IsNullOrWhiteSpace(catalogKey))
        {
            catalog = await db.PresenceCatalogItems.FirstOrDefaultAsync(e =>
                e.AccountId == userId
                && e.Provider == provider
                && e.CatalogKey == catalogKey
                && e.DeletedAt == null);

            if (catalog == null)
            {
                catalog = new SnPresenceCatalogItem
                {
                    AccountId = userId,
                    Provider = provider,
                    CatalogKey = catalogKey,
                    ReferenceId = referenceId,
                    Name = catalogName ?? title,
                    Subtitle = subtitle,
                    Caption = caption,
                    Tags = normalizedTags,
                    CatalogTags = normalizedTags.Select(t => new SnPresenceCatalogTag
                    {
                        Slug = t.Slug,
                        Name = t.Name,
                    }).ToList(),
                    Visibility = visibility,
                    LargeImage = largeImage,
                    SmallImage = smallImage,
                    TitleUrl = titleUrl,
                    SubtitleUrl = subtitleUrl,
                    QueryableTerms = normalizedTerms,
                    Meta = meta ?? [],
                    CreatedAt = now,
                    UpdatedAt = now,
                };
                db.PresenceCatalogItems.Add(catalog);
            }
        }

        SnPresenceActivity activity;
        if (activeSession != null)
        {
            activeSession.Type = type;
            activeSession.Tags = normalizedTags;

            // The session query does not include ActivityTags, so the tracked
            // navigation is empty; removing via it would leave the persisted
            // rows behind and re-inserting the same slugs would then violate
            // the (ActivityId, Slug) primary key. Load the real rows and
            // reconcile by slug instead. Also account for tracked tag entities
            // left over from a previously failed SaveChanges on this context
            // so a retry stays idempotent.
            var existingTags = await db.PresenceActivityTags
                .Where(t => t.ActivityId == activeSession.Id)
                .ToListAsync();
            var knownTags = existingTags
                .Concat(db.ChangeTracker.Entries<SnPresenceActivityTag>()
                    .Where(e => e.Entity.ActivityId == activeSession.Id)
                    .Select(e => e.Entity))
                .Distinct()
                .ToList();

            var newTagSlugs = normalizedTags.Select(t => t.Slug).ToHashSet(StringComparer.Ordinal);
            var tagNames = normalizedTags.ToDictionary(t => t.Slug, t => t.Name, StringComparer.Ordinal);

            // Keep one entry per slug, preferring the persisted row over an
            // unpersisted leftover; drop the rest.
            var keptTags = new List<SnPresenceActivityTag>();
            var tagsToRemove = new List<SnPresenceActivityTag>();
            var keptSlugs = new HashSet<string>(StringComparer.Ordinal);
            foreach (var tag in knownTags)
            {
                if (!keptSlugs.Add(tag.Slug))
                {
                    tagsToRemove.Add(tag);
                    continue;
                }
                keptTags.Add(tag);
                if (tagNames.TryGetValue(tag.Slug, out var tagName))
                    tag.Name = tagName;
            }

            var tagsToAdd = normalizedTags
                .Where(t => !keptSlugs.Contains(t.Slug))
                .Select(t => new SnPresenceActivityTag
                {
                    ActivityId = activeSession.Id,
                    Slug = t.Slug,
                    Name = t.Name,
                })
                .ToList();

            db.PresenceActivityTags.RemoveRange(tagsToRemove);
            activeSession.ActivityTags = keptTags.Concat(tagsToAdd).ToList();
            activeSession.Visibility = visibility;
            activeSession.Provider = provider;
            activeSession.ReferenceId = referenceId;
            activeSession.Title = title;
            activeSession.Subtitle = subtitle;
            activeSession.Caption = caption;
            activeSession.LargeImage = largeImage;
            activeSession.SmallImage = smallImage;
            activeSession.TitleUrl = titleUrl;
            activeSession.SubtitleUrl = subtitleUrl;
            activeSession.QueryableTerms = normalizedTerms;
            activeSession.Meta = meta;
            activeSession.LeaseMinutes = leaseMinutes;
            activeSession.LeaseExpiresAt = now + Duration.FromMinutes(leaseMinutes);
            activeSession.UpdatedAt = now;
            if (catalog != null)
            {
                activeSession.CatalogId = catalog.Id;
                activeSession.Catalog = catalog;
            }
            activity = activeSession;
        }
        else
        {
            activity = new SnPresenceActivity
            {
                AccountId = userId,
                ManualId = manualId,
                Type = type,
                Tags = normalizedTags,
                ActivityTags = normalizedTags.Select(t => new SnPresenceActivityTag
                {
                    Slug = t.Slug,
                    Name = t.Name,
                }).ToList(),
                Visibility = visibility,
                Provider = provider,
                ReferenceId = referenceId,
                Title = title,
                Subtitle = subtitle,
                Caption = caption,
                LargeImage = largeImage,
                SmallImage = smallImage,
                TitleUrl = titleUrl,
                SubtitleUrl = subtitleUrl,
                QueryableTerms = normalizedTerms,
                Meta = meta,
                LeaseMinutes = leaseMinutes,
                LeaseExpiresAt = now + Duration.FromMinutes(leaseMinutes),
                StartedAt = now,
                EndedAt = null,
                CatalogId = catalog?.Id,
                Catalog = catalog,
                CreatedAt = now,
                UpdatedAt = now,
            };
            db.PresenceActivities.Add(activity);
        }

        await db.SaveChangesAsync();

        PurgeActivityCache(userId);
        PurgeCatalogCache(userId);

        await BroadcastPresenceActivitiesUpdated(userId);

        return activity;
    }

    public async Task<SnPresenceActivity?> EndActivitySession(Guid userId, string manualId)
    {
        var now = SystemClock.Instance.GetCurrentInstant();
        var activeSession = await db.PresenceActivities.FirstOrDefaultAsync(e =>
            e.ManualId == manualId
            && e.AccountId == userId
            && e.LeaseExpiresAt > now
            && e.DeletedAt == null
            && e.EndedAt == null);

        if (activeSession == null)
            return null;

        activeSession.EndedAt = now;
        activeSession.LeaseExpiresAt = now;
        activeSession.UpdatedAt = now;

        if (activeSession.CatalogId.HasValue)
        {
            var catalog = await db.PresenceCatalogItems
                .Include(c => c.CatalogTags)
                .FirstOrDefaultAsync(c => c.Id == activeSession.CatalogId.Value);
            if (catalog != null && catalog.DeletedAt == null)
            {
                var durationSeconds = (long)(now - activeSession.StartedAt!.Value).TotalSeconds;
                if (durationSeconds < 0)
                    durationSeconds = 0;
                catalog.TotalSeconds += durationSeconds;
                catalog.SessionCount += 1;
                catalog.LastActiveAt = now;
                catalog.UpdatedAt = now;

                // Merge the session's tags into the catalog item (union by slug)
                var sessionTags = await db.PresenceActivityTags
                    .Where(t => t.ActivityId == activeSession.Id)
                    .ToListAsync();
                var existingSlugs = catalog.CatalogTags
                    .Select(t => t.Slug)
                    .ToHashSet(StringComparer.Ordinal);
                foreach (var sessionTag in sessionTags)
                {
                    if (!existingSlugs.Contains(sessionTag.Slug))
                    {
                        catalog.CatalogTags.Add(new SnPresenceCatalogTag
                        {
                            CatalogId = catalog.Id,
                            Slug = sessionTag.Slug,
                            Name = sessionTag.Name,
                        });
                    }
                }
            }
        }

        await db.SaveChangesAsync();

        PurgeActivityCache(userId);
        PurgeCatalogCache(userId);

        await BroadcastPresenceActivitiesUpdated(userId);

        return activeSession;
    }

    public async Task<List<SnPresenceCatalogItem>> GetCatalogItems(
        Guid userId,
        IEnumerable<string>? tags = null,
        string? provider = null,
        string? query = null,
        bool? isActive = null,
        int offset = 0,
        int take = 50)
    {
        var baseQuery = db.PresenceCatalogItems.Where(e =>
            e.AccountId == userId && e.DeletedAt == null);

        if (tags is not null)
        {
            var normalizedTags = tags
                .Where(t => !string.IsNullOrWhiteSpace(t))
                .Select(t => t.Trim())
                .Distinct(StringComparer.Ordinal)
                .ToList();
            if (normalizedTags.Count > 0)
            {
                foreach (var tagSlug in normalizedTags)
                {
                    baseQuery = baseQuery.Where(e => e.CatalogTags.Any(t => t.Slug == tagSlug));
                }
            }
        }

        if (!string.IsNullOrWhiteSpace(provider))
        {
            var normalizedProvider = provider.Trim();
            baseQuery = baseQuery.Where(e => e.Provider == normalizedProvider);
        }

        if (!string.IsNullOrWhiteSpace(query))
        {
            var normalizedQuery = query.Trim().ToLowerInvariant();
            baseQuery = baseQuery.Where(e =>
                (e.Name != null && e.Name.Contains(query))
                || e.QueryableTerms.Contains(normalizedQuery));
        }

        var now = SystemClock.Instance.GetCurrentInstant();
        if (isActive == true)
        {
            baseQuery = baseQuery.Where(e => e.LastActiveAt != null && e.LastActiveAt > now - Duration.FromHours(24));
        }
        else if (isActive == false)
        {
            baseQuery = baseQuery.Where(e => e.LastActiveAt == null || e.LastActiveAt <= now - Duration.FromHours(24));
        }

        var items = await baseQuery
            .Include(e => e.CatalogTags)
            .OrderByDescending(e => e.LastActiveAt ?? e.UpdatedAt)
            .Skip(offset)
            .Take(take)
            .ToListAsync();

        foreach (var item in items)
            item.Tags = item.CatalogTags
                .Select(t => new SnPresenceTag { Slug = t.Slug, Name = t.Name })
                .ToList();

        return items;
    }

    public async Task<Dictionary<string, long>> GetCatalogStats(
        Guid userId,
        Instant? from = null,
        Instant? to = null,
        IEnumerable<string>? tags = null)
    {
        var baseQuery = db.PresenceActivities.Where(e =>
            e.AccountId == userId
            && e.DeletedAt == null
            && e.EndedAt != null
            && e.StartedAt != null
            && e.CatalogId != null);

        if (from.HasValue)
            baseQuery = baseQuery.Where(e => e.EndedAt >= from.Value);
        if (to.HasValue)
            baseQuery = baseQuery.Where(e => e.EndedAt < to.Value);

        if (tags is not null)
        {
            var normalizedTags = tags
                .Where(t => !string.IsNullOrWhiteSpace(t))
                .Select(t => t.Trim())
                .Distinct(StringComparer.Ordinal)
                .ToList();
            if (normalizedTags.Count > 0)
            {
                foreach (var tagSlug in normalizedTags)
                {
                    baseQuery = baseQuery.Where(e =>
                        db.PresenceCatalogItems.Any(c =>
                            c.Id == e.CatalogId && c.CatalogTags.Any(t => t.Slug == tagSlug)));
                }
            }
        }

        var grouped = await baseQuery
            .GroupBy(e => e.CatalogId!.Value)
            .Select(g => new
            {
                CatalogId = g.Key,
                TotalSeconds = g.Sum(e => (long)Math.Floor((e.EndedAt!.Value - e.StartedAt!.Value).TotalSeconds)),
            })
            .ToListAsync();

        return grouped.ToDictionary(g => g.CatalogId.ToString(), g => g.TotalSeconds);
    }

    public async Task<Dictionary<string, long>> GetTagStats(
        Guid userId,
        Instant? from = null,
        Instant? to = null)
    {
        var endedSessions = db.PresenceActivities.Where(e =>
            e.AccountId == userId
            && e.DeletedAt == null
            && e.EndedAt != null
            && e.StartedAt != null);

        if (from.HasValue)
            endedSessions = endedSessions.Where(e => e.EndedAt >= from.Value);
        if (to.HasValue)
            endedSessions = endedSessions.Where(e => e.EndedAt < to.Value);

        var perTag = await endedSessions
            .SelectMany(e => e.ActivityTags)
            .GroupBy(t => t.Slug)
            .Select(g => new
            {
                Slug = g.Key,
                TotalSeconds = g.Sum(t =>
                    (long)Math.Floor((t.Activity.EndedAt!.Value - t.Activity.StartedAt!.Value).TotalSeconds)),
            })
            .ToListAsync();

        return perTag.ToDictionary(g => g.Slug, g => g.TotalSeconds);
    }

    private static string[] NormalizeQueryableTerms(IEnumerable<string>? terms)
    {
        return (terms ?? [])
            .Where(term => !string.IsNullOrWhiteSpace(term))
            .Select(term => term.Trim().ToLowerInvariant())
            .Distinct()
            .ToArray();
    }

    public async Task<SnPresenceActivity> UpdateActivity(
        Guid activityId,
        Guid userId,
        Action<SnPresenceActivity> update,
        int? leaseMinutes = null
    )
    {
        var activity = await db.PresenceActivities.FindAsync(activityId);
        if (activity == null)
            throw new KeyNotFoundException("Activity not found");

        if (activity.AccountId != userId)
            throw new UnauthorizedAccessException("Activity does not belong to user");

        var before = ClonePresenceActivity(activity);
        var wasActive = IsActivePresenceActivity(
            activity,
            SystemClock.Instance.GetCurrentInstant()
        );

        if (leaseMinutes.HasValue)
        {
            if (leaseMinutes.Value < 1 || leaseMinutes.Value > 60)
                throw new ArgumentException("Lease minutes must be between 1 and 60");

            activity.LeaseMinutes = leaseMinutes.Value;
            activity.LeaseExpiresAt =
                SystemClock.Instance.GetCurrentInstant() + Duration.FromMinutes(leaseMinutes.Value);
        }

        update(activity);
        await db.SaveChangesAsync();

        PurgeActivityCache(activity.AccountId);

        if (
            wasActive
                != IsActivePresenceActivity(activity, SystemClock.Instance.GetCurrentInstant())
            || !PresenceActivityContentEqual(before, activity)
        )
            await BroadcastPresenceActivitiesUpdated(activity.AccountId);

        return activity;
    }

    public async Task<SnPresenceActivity?> UpdateActivityByManualId(
        string manualId,
        Guid userId,
        Action<SnPresenceActivity> update,
        int? leaseMinutes = null
    )
    {
        var now = SystemClock.Instance.GetCurrentInstant();
        var activity = await db.PresenceActivities.FirstOrDefaultAsync(e =>
            e.ManualId == manualId
            && e.AccountId == userId
            && e.LeaseExpiresAt > now
            && e.DeletedAt == null
        );
        if (activity == null)
            return null;

        var before = ClonePresenceActivity(activity);
        var wasActive = IsActivePresenceActivity(activity, now);

        if (leaseMinutes.HasValue)
        {
            if (leaseMinutes.Value is < 1 or > 60)
                throw new ArgumentException("Lease minutes must be between 1 and 60");

            activity.LeaseMinutes = leaseMinutes.Value;
            activity.LeaseExpiresAt =
                SystemClock.Instance.GetCurrentInstant() + Duration.FromMinutes(leaseMinutes.Value);
        }

        update(activity);
        await db.SaveChangesAsync();

        PurgeActivityCache(activity.AccountId);

        if (
            wasActive
                != IsActivePresenceActivity(activity, SystemClock.Instance.GetCurrentInstant())
            || !PresenceActivityContentEqual(before, activity)
        )
            await BroadcastPresenceActivitiesUpdated(activity.AccountId);

        return activity;
    }

    public async Task<bool> DeleteActivityByManualId(string manualId, Guid userId)
    {
        var now = SystemClock.Instance.GetCurrentInstant();
        var activity = await db.PresenceActivities.FirstOrDefaultAsync(e =>
            e.ManualId == manualId
            && e.AccountId == userId
            && e.LeaseExpiresAt > now
            && e.DeletedAt == null
        );
        if (activity == null)
            return false;
        var wasActive = IsActivePresenceActivity(activity, now);
        if (activity.LeaseExpiresAt <= now)
        {
            activity.DeletedAt = now;
        }
        else
        {
            activity.LeaseExpiresAt = now;
        }

        db.Update(activity);
        await db.SaveChangesAsync();
        PurgeActivityCache(activity.AccountId);
        if (wasActive)
            await BroadcastPresenceActivitiesUpdated(activity.AccountId);
        return true;
    }

    public async Task<bool> DeleteActivity(Guid activityId, Guid userId)
    {
        var activity = await db.PresenceActivities.FindAsync(activityId);
        if (activity == null)
            return false;

        if (activity.AccountId != userId)
            throw new UnauthorizedAccessException("Activity does not belong to user");

        var now = SystemClock.Instance.GetCurrentInstant();
        var wasActive = IsActivePresenceActivity(activity, now);
        if (activity.LeaseExpiresAt <= now)
        {
            activity.DeletedAt = now;
        }
        else
        {
            activity.LeaseExpiresAt = now;
        }

        db.Update(activity);
        await db.SaveChangesAsync();
        PurgeActivityCache(activity.AccountId);
        if (wasActive)
            await BroadcastPresenceActivitiesUpdated(activity.AccountId);
        return true;
    }
