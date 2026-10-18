// SPDX-License-Identifier: AGPL-3.0-only
// 内容互动组装：问卷、贴纸与自动补全共享圈子权限和媒体，并提供聊天的规范引用组合。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：contentEngagementFor(db, verifier, guard, social, chat, wallet, media)
package application

import (
	"database/sql"
	"net/http"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/autocompletepg"
	"solsynth.local/go-migration/internal/chatpg"
	"solsynth.local/go-migration/internal/mediapg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/stickerpg"
	"solsynth.local/go-migration/internal/surveypg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/autocomplete"
	"solsynth.local/go-migration/ports/sphere/stickers"
	"solsynth.local/go-migration/ports/sphere/surveys"
)

type contentEngagementModules struct {
	surveysRepo      *surveypg.Repository
	autocompleteRepo *autocompletepg.Repository
	autocompleteHTTP http.Handler
	surveysHTTP      http.Handler
	stickersHTTP     http.Handler
}

func contentEngagementFor(
	db *sql.DB,
	v *authority.Verifier,
	guard *authoritypg.TransactionGuard,
	socialRepo *socialpg.Repository,
	chatRepo *chatpg.Repository,
	wallet *walletpg.Repository,
	mediaRepo *mediapg.Repository,
) (contentEngagementModules, error) {
	identity := identity{v}
	var err error
	// 聊天嵌入只能通过真实钱包和问卷仓库校验；媒体仅在配置时提供。
	surveysRepo := surveypg.NewWithAuthority(db, guard).WithVisibility(socialRepo)
	stickersRepo := stickerpg.NewWithAuthority(db, guard).WithVisibility(socialRepo)
	chatRepo.WithMessageComposer(NewChatMessageComposer(wallet, surveysRepo))
	if mediaRepo != nil {
		surveysRepo.WithMedia(mediaRepo)
		stickersRepo.WithMedia(mediaRepo)
	}
	// 自动补全使用同一房间与贴纸仓库，并保留事务凭证校验。
	var autocompleteRepo *autocompletepg.Repository
	var autocompleteHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "AUTOCOMPLETE_AUTHORITY_UNAVAILABLE") })
	if guard != nil {
		autocompleteRepo, err = autocompletepg.New(db, guard, autocompletepg.Config{Stickers: stickersRepo, Rooms: chatRepo})
		if err != nil {
			return contentEngagementModules{}, err
		}
		autocompleteHTTP = bindIdentity(v, bindTransactionAuthority(guard, autocomplete.NewHandler(autocompleteRepo, postIdentity{identity})))
	}
	surveysHTTP := surveys.NewHandler(surveysRepo, postIdentity{identity})
	stickersHTTP := stickers.NewHandler(stickersRepo, postIdentity{identity})
	return contentEngagementModules{
		surveysRepo:      surveysRepo,
		autocompleteRepo: autocompleteRepo,
		autocompleteHTTP: autocompleteHTTP,
		surveysHTTP:      surveysHTTP,
		stickersHTTP:     stickersHTTP,
	}, nil
}
