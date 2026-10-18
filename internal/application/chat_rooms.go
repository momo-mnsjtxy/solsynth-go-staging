// SPDX-License-Identifier: AGPL-3.0-only
// 聊天房间组装：消息投影、房间写入、媒体关联和订阅权益在此归一。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：chatRoomsFor(db, config, verifier, guard, commerce, bots)
package application

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/botpg"
	"solsynth.local/go-migration/internal/chatmediapg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/internal/roommediapg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/messager/rooms"
)

type chatRoomsModules struct {
	messageReader    *messagerpg.Repository
	chatRepo         *chatpg.Repository
	chatMedia        *chatmediapg.Repository
	roomMedia        *roommediapg.Repository
	chatOrganization *chatpg.Organization
	chatHTTP         http.Handler
}

func chatRoomsFor(
	db *sql.DB,
	c Config,
	v *authority.Verifier,
	guard *authoritypg.TransactionGuard,
	commerceRepo *commercepg.Repository,
	botRepo *botpg.Repository,
) (chatRoomsModules, error) {
	identity := identity{v}
	// 明确绑定枚举政策，再连接消息读写与机器人回复。
	messageReader, err := messagerpg.NewWithIndependentPolicy(db, *c.MessagerPolicy)
	if err != nil {
		return chatRoomsModules{}, err
	}
	chatRepo, err := chatpg.NewWithAuthority(db, rooms.Policy{NoneEncryptionMode: c.MessagerPolicy.NoneEncryptionMode, NotifyAll: c.MessagerPolicy.NotifyAll, NotifyMentions: c.MessagerPolicy.NotifyMentions, NotifyNone: c.MessagerPolicy.NotifyNone}, guard)
	if err != nil {
		return chatRoomsModules{}, err
	}
	if botRepo != nil {
		chatRepo.WithBotIdentity(botRepo)
		botRepo.WithReplySender(chatRepo)
	}
	// 明文映射与 MLS 模式不能冲突；语音读取保护随显式保留期启用。
	if c.EnableMLSDelivery {
		if c.MessagerPolicy.NoneEncryptionMode == rooms.MLSEncryptionMode {
			return chatRoomsModules{}, errors.New("plaintext mapping must differ from authored MLS mode 3")
		}
		chatRepo.WithMLSDelivery()
	}
	chatMedia := chatmediapg.New(db, chatRepo, c.MessagerPolicy.NoneEncryptionMode).WithVoiceReadProtection(c.ChatVoiceRetention != 0)
	chatRepo.WithAssociations(chatMedia)
	roomMedia := roommediapg.New(db, chatRepo, c.MessagerPolicy.NoneEncryptionMode)
	chatRepo.WithRoomMedia(roomMedia)
	messageReader.WithMessageDecorator(chatMedia)
	// 搜索和昵称权益都由交易目录校验，不引入默认授权。
	var chatSearch rooms.SearchEntitlement
	if c.ChatSearchPerks != nil {
		chatSearch, err = commerceRepo.ChatSearchPerks(c.ChatSearchPerks)
		if err != nil {
			return chatRoomsModules{}, err
		}
	}
	if c.ChatProfilePerks != nil {
		perks, e := commerceRepo.ChatProfilePerks(c.ChatProfilePerks)
		if e != nil {
			return chatRoomsModules{}, e
		}
		chatRepo.WithProfilePerks(perks)
	}
	if c.ChatVoiceRetention != 0 && (c.MediaStore == nil || c.ChatVoiceRetention < time.Hour || c.ChatVoiceRetention > 365*24*time.Hour) {
		return chatRoomsModules{}, errors.New("voice requires canonical media and explicit retention from one hour to one year")
	}
	chatOrganization := chatpg.NewOrganization(chatRepo, messageReader, chatSearch)
	chatHTTP := (&rooms.Service{Repository: chatRepo, Organization: chatOrganization, Authority: proofIdentity{identity}, Reader: messageReader}).Handler()
	return chatRoomsModules{
		messageReader:    messageReader,
		chatRepo:         chatRepo,
		chatMedia:        chatMedia,
		roomMedia:        roomMedia,
		chatOrganization: chatOrganization,
		chatHTTP:         chatHTTP,
	}, nil
}
