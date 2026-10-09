// Original lines 425-447
          }
          try {
            final activityData = {
              'type': type,
              'manual_id': appId,
              'title': title,
              'subtitle': subtitle,
              'caption': activity['state'],
              'title_url': activity['assets']?['small_text_url'],
              'subtitle_url': activity['assets']?['large_text_url'],
              'small_image': imageSmall,
              'large_image': imageLarge,
              'meta': activity,
              'tags': [
                {'slug': type, 'name': type},
              ],
              'lease_minutes': kPresenceActivityLease,
            };

            await apiClient.post('/passport/activities', data: activityData);
            setCurrentActivity(appId, activityData);
          } catch (e) {
            Logger.root.info('Failed to set remote activity status: $e');

// Original lines 477-487
        final currentId = currentActivityManualId;
        if (currentId != null && currentId == appId) {
          try {
            await apiClient.delete(
              '/passport/activities',
              queryParameters: {'manualId': currentId},
            );
            setCurrentActivity(null, null);
          } catch (e) {
            Logger.root.info('Failed to unset remote activity status: $e');
          }

// Original lines 587-636
  Future<void> _renewActivity() async {
    if (state.currentActivityData != null) {
      try {
        await apiClient.post(
          '/passport/activities',
          data: state.currentActivityData,
        );
        Logger.root.info('Activity lease renewed');
      } catch (e) {
        Logger.root.info('Failed to renew activity lease: $e');
      }
    }
  }
}

const kPresenceActivityLease = 5;

// Providers
final rpcServerStateProvider =
    NotifierProvider<ServerStateNotifier, ServerState>(ServerStateNotifier.new);

final rpcServerProvider = Provider<ActivityRpcServer>((ref) {
  final notifier = ref.watch(rpcServerStateProvider.notifier);
  return notifier.server;
});

@riverpod
Future<List<SnPresenceActivity>> presenceActivities(
  Ref ref,
  String uname,
) async {
  final apiClient = ref.watch(apiClientProvider);
  final response = await apiClient.get('/passport/activities/$uname');
  final data = response.data as List<dynamic>;
  final activities = data
      .map((json) => SnPresenceActivity.fromJson(json))
      .toList();

  if (activities.isNotEmpty) {
    final accountId = activities.first.accountId;
    final websocket = ref.watch(websocketProvider);
    final subscription = websocket.dataStream.listen((packet) {
      if (packet.type == 'account.presence.activities.updated' &&
          packet.data?['account_id'] == accountId) {
        ref.invalidateSelf();
      }
    });
    ref.onDispose(subscription.cancel);
  }
