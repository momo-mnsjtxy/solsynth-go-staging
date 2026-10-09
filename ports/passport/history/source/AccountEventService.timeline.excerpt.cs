    public async Task<(List<AccountTimelineItem>, int)> GetTimeline(
        Guid userId,
        int offset = 0,
        int take = 20
    )
    {
        var statusQuery = db.AccountStatuses.Where(e =>
            e.AccountId == userId && e.DeletedAt == null
        );
        var activityQuery = db.PresenceActivities.Where(e =>
            e.AccountId == userId && e.DeletedAt == null
        );

        var statusCount = await statusQuery.CountAsync();
        var activityCount = await activityQuery.CountAsync();
        var totalCount = statusCount + activityCount;

        var statuses = await statusQuery.OrderByDescending(e => e.CreatedAt).ToListAsync();
        var activities = await activityQuery.OrderByDescending(e => e.CreatedAt).ToListAsync();

        var timelineItems = new List<AccountTimelineItem>();

        foreach (var status in statuses)
        {
            timelineItems.Add(
                new AccountTimelineItem
                {
                    Id = status.Id,
                    CreatedAt = status.CreatedAt,
                    EventType = TimelineEventType.StatusChange,
                    Status = status,
                }
            );
        }

        foreach (var activity in activities)
        {
            timelineItems.Add(
                new AccountTimelineItem
                {
                    Id = activity.Id,
                    CreatedAt = activity.CreatedAt,
                    EventType = TimelineEventType.Activity,
                    Activity = activity,
                }
            );
        }

        var sortedTimeline = timelineItems
            .OrderByDescending(e => e.CreatedAt)
            .Skip(offset)
            .Take(take)
            .ToList();

        return (sortedTimeline, totalCount);
    }
