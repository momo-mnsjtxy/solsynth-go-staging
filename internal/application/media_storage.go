// SPDX-License-Identifier: AGPL-3.0-only
// 媒体存储组装：统一文件大小、语音保留和各业务引用的读取权限。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：mediaStorageFor(db, db, config, verifier, guard, accounts, apps, bots, chat, chatMedia, roomMedia, postMedia, tickets, locations, external)
package application

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"solsynth.local/go-migration/internal/accountpg"
	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/botpg"
	"solsynth.local/go-migration/internal/calendarmediapg"
	"solsynth.local/go-migration/internal/chatmediapg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/developmediapg"
	"solsynth.local/go-migration/internal/locationpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/postmediapg"
	"solsynth.local/go-migration/internal/profilemediapg"
	"solsynth.local/go-migration/internal/roommediapg"
	"solsynth.local/go-migration/internal/ticketmediapg"
	"solsynth.local/go-migration/internal/ticketpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/passport/artwork"
)

type mediaStorageModules struct {
	mediaRepo      *mediapg.Repository
	calendarMedia  *calendarmediapg.Repository
	developerMedia *developmediapg.Repository
	artworkHTTP    http.Handler
	mediaHTTP      http.Handler
}

func mediaStorageFor(
	db, authorityDB *sql.DB,
	c Config,
	v *authority.Verifier,
	guard *authoritypg.TransactionGuard,
	accountRepo *accountpg.Repository,
	appRepo *apppg.Repository,
	botRepo *botpg.Repository,
	chatRepo *chatpg.Repository,
	chatMedia *chatmediapg.Repository,
	roomMedia *roommediapg.Repository,
	postMedia *postmediapg.Repository,
	ticketRepo *ticketpg.Repository,
	locationRepo *locationpg.Repository,
	external externalModules,
) (mediaStorageModules, error) {
	identity := identity{v}
	var err error
	var mediaRepo *mediapg.Repository
	var calendarMedia *calendarmediapg.Repository
	var developerMedia *developmediapg.Repository
	var artworkHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "MEDIA_STORAGE_UNCONFIGURED") })
	var mediaHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "MEDIA_STORAGE_UNCONFIGURED") })
	// 存储要求同库权威和正数大小上限；所有保护在对外暴露 Handler 前装配。
	if c.MediaStore != nil {
		if authorityDB != db || c.MaxMediaBytes <= 0 {
			return mediaStorageModules{}, errors.New("media requires same-pool authority and explicit positive size limit")
		}
		mediaRepo, err = mediapg.New(db, c.MediaStore, guard, mediapg.Config{MaxFileBytes: c.MaxMediaBytes, PendingTTL: time.Hour})
		if err != nil {
			return mediaStorageModules{}, err
		}
		if err = mediaRepo.WithPools(c.MediaPools); err != nil {
			return mediaStorageModules{}, err
		}
		mediaRepo.WithRemoteFetcher(c.MediaRemoteFetcher)
		mediaRepo.WithRenderer(c.MediaRenderer)
		if c.ChatVoiceRetention != 0 {
			if err = chatRepo.WithVoiceStorage(mediaRepo, c.ChatVoiceRetention); err != nil {
				return mediaStorageModules{}, err
			}
		}
		ticketMedia := ticketmediapg.New(db, ticketRepo)
		ticketRepo.WithMedia(ticketMedia)
		calendarMedia = calendarmediapg.New(db)
		developerMedia = developmediapg.New(db)
		appRepo.WithMedia(developerMedia)
		// 汇合帖子、房间、票据与开发者资源的可见性，头像读取再叠加引用保护。
		readVisibility := mediapg.AnyReadVisibility{postMedia, chatMedia, roomMedia, ticketMedia, calendarMedia, developerMedia}
		if botRepo != nil {
			readVisibility = append(readVisibility, botRepo)
		}
		profileMedia := profilemediapg.New(db, readVisibility)
		accountRepo.WithProfileMedia(profileMedia)
		locationRepo.WithMedia().WithMediaProtection(chatMedia)
		mediaRepo.WithReadVisibility(mediapg.AnyReadVisibility{profileMedia, locationRepo}).WithFileReadProtection(chatMedia)
		if external.federation != nil {
			external.federation.WithProfileMedia(profileMedia)
		}
		artworkHTTP = (&artwork.Service{Repository: mediaRepo, Authority: proofIdentity{identity}}).Handler()
		mediaHTTP = (&media.Service{Repository: mediaRepo, Authority: proofIdentity{identity}, MaxFileBytes: c.MaxMediaBytes}).Handler()
	}
	return mediaStorageModules{
		mediaRepo:      mediaRepo,
		calendarMedia:  calendarMedia,
		developerMedia: developerMedia,
		artworkHTTP:    artworkHTTP,
		mediaHTTP:      mediaHTTP,
	}, nil
}
