// SPDX-License-Identifier: AGPL-3.0-only
// 阅读与翻译组装：显式启用同库鉴权的内容预览及翻译服务。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：readingContentFor(db, db, config, verifier, guard)
package application

import (
	"database/sql"
	"errors"
	"net/http"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/readerpg"
	"solsynth.local/go-migration/internal/translationpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/reader"
	"solsynth.local/go-migration/ports/sphere/translation"
)

type readingModules struct {
	readerRepo      *readerpg.Repository
	readerHTTP      http.Handler
	translationRepo *translationpg.Repository
	translationHTTP http.Handler
}

func readingContentFor(db, authorityDB *sql.DB, c Config, v *authority.Verifier, guard *authoritypg.TransactionGuard) (readingModules, error) {
	identity := identity{v}
	var err error
	var readerRepo *readerpg.Repository
	var readerHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "READER_UNCONFIGURED") })
	// 预览服务只接收同库事务凭证；未配置时保留显式不可用响应。
	if c.Reader != nil {
		if authorityDB != db || guard == nil {
			return readingModules{}, errors.New("reader requires canonical same-pool authority")
		}
		cfg := *c.Reader
		if cfg.Now == nil {
			cfg.Now = c.Authority.Now
		}
		readerRepo, err = readerpg.New(db, guard, cfg)
		if err != nil {
			return readingModules{}, err
		}
		readerHTTP = bindIdentity(v, bindTransactionAuthority(guard, reader.NewHandler(readerRepo, postIdentity{identity})))
	}
	var translationRepo *translationpg.Repository
	var translationHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "TRANSLATION_UNCONFIGURED") })
	// 翻译沿用宿主时钟，调用方给出的配置保持原样。
	if c.Translation != nil {
		if authorityDB != db || guard == nil {
			return readingModules{}, errors.New("translation requires canonical same-pool authority")
		}
		cfg := *c.Translation
		if cfg.Now == nil {
			cfg.Now = c.Authority.Now
		}
		translationRepo, err = translationpg.New(db, guard, cfg)
		if err != nil {
			return readingModules{}, err
		}
		translationHTTP = bindIdentity(v, bindTransactionAuthority(guard, translation.NewHandler(translationRepo, postIdentity{identity})))
	}
	return readingModules{
		readerRepo:      readerRepo,
		readerHTTP:      readerHTTP,
		translationRepo: translationRepo,
		translationHTTP: translationHTTP,
	}, nil
}
