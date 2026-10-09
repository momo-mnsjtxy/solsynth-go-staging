    private static async Task HandleWebSocketStatusChanged(Guid accountId, string deviceId, Instant timestamp, EventContext ctx)
    {
        var accountEvents = ctx.ServiceProvider.GetRequiredService<AccountEventService>();
        var relationships = ctx.ServiceProvider.GetRequiredService<RelationshipService>();
        var ws = ctx.ServiceProvider.GetRequiredService<RemoteWebSocketService>();
        var logger = ctx.ServiceProvider.GetRequiredService<ILogger<EventBus>>();

        var previousStatus = await accountEvents.GetPreviousStatus(accountId);
        accountEvents.PurgeStatusCache(accountId);
        // The broadcast audience is the account's own friends, who are allowed
        // to see device presence, so skip the viewer relationship check.
        var currentStatus = await accountEvents.GetStatus(accountId, includeDevicePresence: true);

        if (previousStatus is not null && BroadcastEventHandler.StatusesEqual(previousStatus, currentStatus))
            return;

        var friendIds = await relationships.ListAccountFriends(accountId);
        if (friendIds.Count == 0) return;

        await ws.PushWebSocketPacketToUsers(
            friendIds.Select(id => id.ToString()).ToList(),
            WebSocketPacketType.AccountStatusUpdated,
            InfraObjectCoder.ConvertObjectToByteString(new Dictionary<string, object>
            {
                ["account_id"] = accountId,
                ["status"] = currentStatus,
                ["device_id"] = deviceId,
                ["timestamp"] = timestamp
            }).ToByteArray()
        );

        logger.LogDebug("Broadcast account status update for {AccountId} to {FriendCount} friends", accountId, friendIds.Count);
    }
