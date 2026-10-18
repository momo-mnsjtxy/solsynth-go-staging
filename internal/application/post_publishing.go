// SPDX-License-Identifier: AGPL-3.0-only
// 发帖组装：互动、时间线、媒体嵌入、定时发布与博客使用相同可见性和审核政策。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：postPublishingFor(db, db, config, verifier, guard, social, reader)
package application

import (
	"database/sql"
	"errors"
	"net/http"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/blogpg"
	"solsynth.local/go-migration/internal/interactionpg"
	"solsynth.local/go-migration/internal/postembedpg"
	"solsynth.local/go-migration/internal/postmediapg"
	"solsynth.local/go-migration/internal/postpg"
	"solsynth.local/go-migration/internal/readerpg"
	"solsynth.local/go-migration/internal/socialpg"
	"solsynth.local/go-migration/internal/timelinepg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/sphere/blogshttp"
	"solsynth.local/go-migration/ports/sphere/interactions"
	"solsynth.local/go-migration/ports/sphere/posts"
	"solsynth.local/go-migration/ports/sphere/timeline"
)

type postPublishingModules struct {
	interactionRepo *interactionpg.Repository
	interactionHTTP http.Handler
	postRepository  *postpg.Repository
	postMedia       *postmediapg.Repository
	postComposition *postembedpg.Repository
	timelineRepo    *timelinepg.Repository
	timelineHTTP    http.Handler
	scheduledPosts  *postpg.Repository
	blogRepo        *blogpg.Repository
	blogHTTP        http.Handler
	postsHTTP       http.Handler
}

func postPublishingFor(
	db, authorityDB *sql.DB,
	c Config,
	v *authority.Verifier,
	guard *authoritypg.TransactionGuard,
	socialRepo *socialpg.Repository,
	readerRepo *readerpg.Repository,
) (postPublishingModules, error) {
	identity := identity{v}
	var err error
	// 所有帖子投影和嵌入引用共享圈子可见性，审核开关同步到写入与时间线。
	interactionRepo := interactionpg.NewWithAuthority(db, guard).WithVisibility(socialRepo)
	interactionHTTP := interactions.NewHandler(interactionRepo, postIdentity{identity})
	postRepository := postpg.NewWithAuthority(db, guard)
	if guard == nil {
		postRepository = postpg.New(db)
	}
	postMedia := postmediapg.New(db, interactionRepo).WithVisibility(socialRepo)
	postComposition := postembedpg.New(db, postMedia, socialRepo)
	timelineRepo := timelinepg.NewWithAuthority(db, guard).WithVisibility(socialRepo).WithAssociations(postComposition)
	interactionRepo.WithPostProjection(timelineRepo)
	timelineHTTP := timeline.NewHandler(timelineRepo, postIdentity{identity})
	postRepository.WithAssociations(timelineRepo).WithVisibility(socialRepo).WithAutomod(c.PostAutomodReview)
	timelineRepo.WithAutomod(c.PostAutomodReview)
	// 定时发布要求事务守卫；博客仅在同库权威可用时连接预览服务。
	var scheduledPosts *postpg.Repository
	if guard != nil {
		scheduledPosts = postRepository.WithScheduling()
	}
	var blogRepo *blogpg.Repository
	var blogHTTP http.Handler = blogshttp.NewHandler(nil, nil)
	if c.Blogs != nil {
		if guard == nil || authorityDB != db {
			return postPublishingModules{}, errors.New("blogs require canonical same-pool authority")
		}
		cfg := *c.Blogs
		if cfg.Now == nil {
			cfg.Now = c.Authority.Now
		}
		if cfg.Previewer == nil && readerRepo != nil {
			cfg.Previewer = readerRepo
		}
		blogRepo, err = blogpg.New(db, guard, cfg)
		if err != nil {
			return postPublishingModules{}, err
		}
		postRepository.WithBlogs(blogRepo)
		blogHTTP = bindIdentity(v, bindTransactionAuthority(guard, blogshttp.NewHandler(blogRepo, postIdentity{identity})))
	}
	postsHTTP := posts.NewHandler(postRepository, postIdentity{identity})
	return postPublishingModules{
		interactionRepo: interactionRepo,
		interactionHTTP: interactionHTTP,
		postRepository:  postRepository,
		postMedia:       postMedia,
		postComposition: postComposition,
		timelineRepo:    timelineRepo,
		timelineHTTP:    timelineHTTP,
		scheduledPosts:  scheduledPosts,
		blogRepo:        blogRepo,
		blogHTTP:        blogHTTP,
		postsHTTP:       postsHTTP,
	}, nil
}
