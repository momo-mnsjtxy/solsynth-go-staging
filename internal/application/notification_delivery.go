// SPDX-License-Identifier: AGPL-3.0-only
// 通知投递组装：偏好、发布事件、实时连接与设备投递使用同一个应用命名空间。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：notificationsFor(db, config, verifier, guard, activity, presence, progression, chat, organization, surveys, interactions, posts, timeline, social)
package application

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"solsynth.local/go-migration/internal/accountsettingspg"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/friendeventpg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/notificationpg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/internal/publishernotificationpg"
	"solsynth.local/go-migration/internal/realtime"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/surveypg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/notifications"
	"solsynth.local/go-migration/ports/passport/accountsettings"
)

type notificationDeliveryModules struct {
	settingsRepo            *accountsettingspg.Repository
	settingsHTTP            http.Handler
	notificationRepo        *notificationpg.Repository
	publisherNotifications  *publishernotificationpg.Repository
	notificationHTTP        http.Handler
	realtimeHTTP            http.Handler
	surveyNotifications     func(context.Context, int) (int, error)
	collectionNotifications func(context.Context, int) (int, error)
	deviceModule            notificationDeviceModule
	notificationWorker      *notificationpg.Worker
}

func notificationsFor(
	db *sql.DB,
	c Config,
	v *authority.Verifier,
	guard *authoritypg.TransactionGuard,
	activityRepo *activitypg.Repository,
	presenceRepo *presencepg.Repository,
	progressionRepo *progressionpg.Repository,
	chatRepo *chatpg.Repository,
	chatOrganization *chatpg.Organization,
	surveysRepo *surveypg.Repository,
	interactionRepo *interactionpg.Repository,
	postRepository *postpg.Repository,
	timelineRepo *timelinepg.Repository,
	socialRepo *socialpg.Repository,
) (notificationDeliveryModules, error) {
	identity := identity{v}
	var err error
	// 先建立账号偏好，再绑定聊天、好友与合集发布事件。
	var settingsRepo *accountsettingspg.Repository
	var settingsHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "SETTINGS_AUTHORITY_UNAVAILABLE") })
	if guard != nil {
		settingsRepo, err = accountsettingspg.New(db, guard, c.NotificationApp)
		if err != nil {
			return notificationDeliveryModules{}, err
		}
		settingsHTTP = (&accountsettings.Service{Repository: settingsRepo, Authority: v}).Handler()
	}
	notificationRepo := notificationpg.New(db, guard, c.NotificationApp).WithNightOwlReminders(c.NightOwlReminders).WithRoomAccess(chatRepo).WithChatEvents(chatRepo).WithFriendEvents(friendeventpg.New(db, guard, activityRepo).WithStatus(presenceRepo)).WithCollectionPublications(interactionRepo)
	// 发帖与订阅投影直接连接持久通知仓库，不绕过各自可见性。
	publisherNotifications := publishernotificationpg.New(db, socialRepo, notificationRepo, c.NotificationApp)
	notificationRepo.WithPublisherContent(publisherNotifications)
	postRepository.WithPublicationObserver(publicationObservers{interactionRepo, publisherNotifications})
	timelineRepo.WithSubscriptionNotifications(publisherSubscriptionNotifications{notificationRepo, db, c.NotificationApp})
	if settingsRepo != nil {
		notificationRepo.WithPreferencePolicy(settingsRepo)
	}
	notificationIdentity := notificationIdentity{identity}
	notificationHTTP := (notifications.HTTP{Repository: notificationRepo, Identity: notificationIdentity, App: c.NotificationApp}).Handler()
	realtimeHTTP, err := realtime.New(notificationRepo, notificationIdentity, realtime.Config{App: c.NotificationApp, Commands: placeholderCommands(chatRepo), Connections: chatOrganization})
	if err != nil {
		return notificationDeliveryModules{}, err
	}
	surveyNotifications := func(ctx context.Context, limit int) (int, error) {
		return surveysRepo.ProcessNotifications(ctx, notificationRepo, c.NotificationApp, limit)
	}
	collectionNotifications := func(ctx context.Context, limit int) (int, error) {
		return interactionRepo.ProcessPublicationNotifications(ctx, notificationRepo, c.NotificationApp, limit)
	}
	// 本地投影由宿主显式调度；设备发送器与外部发送器互斥。
	deviceModule, err := notificationDevicesFor(db, guard, v, c.NotificationDevices, progressionRepo, notificationRepo)
	if err != nil {
		return notificationDeliveryModules{}, err
	}
	publisherContentNotifications := publisherNotifications.ProcessDue
	notificationWorker := &notificationpg.Worker{LocalProjections: []func(context.Context, int) (int, error){surveyNotifications, collectionNotifications, publisherContentNotifications}, Repository: notificationRepo, Wallet: true, Chat: true, ChatNoneEncryptionMode: int(c.MessagerPolicy.NoneEncryptionMode), Sender: c.NotificationSender}
	if c.NightOwlReminders {
		notificationWorker.LocalProjections = append(notificationWorker.LocalProjections, notificationRepo.ProcessNightOwls)
	}
	if deviceModule.repo != nil {
		if notificationWorker.Sender != nil {
			return notificationDeliveryModules{}, errors.New("notification sender and device delivery cannot both be configured")
		}
		notificationWorker.Sender = deviceModule.repo
	}
	return notificationDeliveryModules{
		settingsRepo:            settingsRepo,
		settingsHTTP:            settingsHTTP,
		notificationRepo:        notificationRepo,
		publisherNotifications:  publisherNotifications,
		notificationHTTP:        notificationHTTP,
		realtimeHTTP:            realtimeHTTP,
		surveyNotifications:     surveyNotifications,
		collectionNotifications: collectionNotifications,
		deviceModule:            deviceModule,
		notificationWorker:      notificationWorker,
	}, nil
}
