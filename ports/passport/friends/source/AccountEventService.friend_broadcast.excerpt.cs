    private async Task BroadcastPresenceActivitiesUpdated(Guid accountId)
    {
        var activities = await GetActiveActivities(accountId);
        var evt = new AccountPresenceActivitiesUpdatedEvent
        {
            AccountId = accountId,
            Activities = activities,
        };

        await eventBus.PublishAsync(evt);

        var friendIds = await relationships.ListAccountFriends(accountId);
        if (friendIds.Count == 0)
            return;

        await ws.PushWebSocketPacketToUsers(
            friendIds.Select(id => id.ToString()).ToList(),
            WebSocketPacketType.AccountPresenceActivitiesUpdated,
            InfraObjectCoder
                .ConvertObjectToByteString(
                    new Dictionary<string, object>
                    {
                        ["account_id"] = accountId,
                        ["activities"] = activities,
                        ["timestamp"] = evt.Timestamp,
                    }
                )
                .ToByteArray()
        );

        logger.LogDebug(
            "Broadcast account presence activities update for {AccountId} to {FriendCount} friends",
            accountId,
            friendIds.Count
        );
    }
