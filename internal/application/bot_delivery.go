// SPDX-License-Identifier: AGPL-3.0-only
// 机器人投递组装：连接媒体、受限执行器和聊天观察者，默认保持显式不可用。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：botDeliveryFor(config, verifier, guard, bots, chat, media, developerMedia)
package application

import (
	"net/http"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/botpg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/developmediapg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/bots"
	"solsynth.local/go-migration/ports/messager/rooms"
)

type botDeliveryModules struct {
	botHTTP http.Handler
}

func botDeliveryFor(
	c Config,
	v *authority.Verifier,
	guard *authoritypg.TransactionGuard,
	botRepo *botpg.Repository,
	chatRepo *chatpg.Repository,
	mediaRepo *mediapg.Repository,
	developerMedia *developmediapg.Repository,
) (botDeliveryModules, error) {
	var err error
	var botHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "BOT_AUTHORITY_UNAVAILABLE") })
	// 有执行配置也必须具备事务权威，并核对加密模式后才能观察消息。
	if c.BotExecution != nil && guard == nil {
		return botDeliveryModules{}, bots.ErrUnavailable
	}
	if guard != nil {
		if mediaRepo != nil {
			botRepo.WithMedia(mediaRepo).WithProfileMedia(developerMedia)
		}
		if c.BotExecution != nil {
			if chatRepo == nil || c.MessagerPolicy == nil || c.BotExecution.Runtime.NoneEncryptionMode != int(c.MessagerPolicy.NoneEncryptionMode) {
				return botDeliveryModules{}, bots.ErrInvalid
			}
			c.BotExecution.Runtime.EnableMLSOpaque = c.EnableMLSDelivery
			c.BotExecution.Runtime.MLSEncryptionMode = int(rooms.MLSEncryptionMode)
			if err = botRepo.WithExecution(c.BotExecution.Runtime, c.BotExecution.Adapters...); err != nil {
				return botDeliveryModules{}, err
			}
			chatRepo.WithTextMessageObserver(botRepo)
		}
		botHTTP = (&bots.Service{Repository: botRepo, Authority: v, Bind: guard.Bind}).Handler()
	}
	return botDeliveryModules{
		botHTTP: botHTTP,
	}, nil
}
