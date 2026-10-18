// SPDX-License-Identifier: AGPL-3.0-only
// 账号生命周期组装：在线状态、运势成长、连接历史、奖励票据与邀请激活保持同一事务链。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：accountLifecycleFor(db, config, verifier, guard, commerce, signin)
package application

import (
	"context"
	"database/sql"
	"net/http"

	"solsynth.local/go-migration/internal/accounthistorypg"
	"solsynth.local/go-migration/internal/activitypg"
	"solsynth.local/go-migration/internal/affiliationpg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/fortunepg"
	"solsynth.local/go-migration/internal/presencepg"
	"solsynth.local/go-migration/internal/progressionpg"
	"solsynth.local/go-migration/internal/signinpg"
	"solsynth.local/go-migration/internal/ticketpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/activities"
	"solsynth.local/go-migration/ports/passport/affiliation"
	"solsynth.local/go-migration/ports/passport/fortunes"
	"solsynth.local/go-migration/ports/passport/friends"
	"solsynth.local/go-migration/ports/passport/history"
	"solsynth.local/go-migration/ports/passport/presence"
	"solsynth.local/go-migration/ports/passport/progression"
	"solsynth.local/go-migration/ports/passport/tickets"
)

type accountLifecycleModules struct {
	presenceRepo       *presencepg.Repository
	fortuneRepo        *fortunepg.Repository
	fortuneHTTP        http.Handler
	progressionRepo    *progressionpg.Repository
	bindProof          func(context.Context, authority.Principal) context.Context
	activityRepo       *activitypg.Repository
	accountConnections accountConnectionModules
	accountHistoryRepo *accounthistorypg.Repository
	accountHistoryHTTP http.Handler
	friendsHTTP        http.Handler
	activityHTTP       http.Handler
	presenceHTTP       http.Handler
	progressionHTTP    http.Handler
	ticketRepo         *ticketpg.Repository
	ticketHTTP         http.Handler
	affiliationRepo    *affiliationpg.Repository
	affiliationHTTP    http.Handler
}

func accountLifecycleFor(
	db *sql.DB,
	c Config,
	v *authority.Verifier,
	guard *authoritypg.TransactionGuard,
	commerceRepo *commercepg.Repository,
	signinRepo *signinpg.Repository,
) (accountLifecycleModules, error) {
	identity := identity{v}
	var err error
	// 运势报告作为签到来源；资金及订阅奖励必须来自显式政策。
	presenceRepo := presencepg.NewWithAuthority(db, guard)
	var fortuneRepo *fortunepg.Repository
	var fortuneHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "FORTUNE_UNCONFIGURED") })
	if c.Fortunes != nil {
		fortuneRepo, err = fortunepg.New(db, guard, *c.Fortunes)
		if err != nil {
			return accountLifecycleModules{}, err
		}
		fortuneHTTP = (&fortunes.Service{Repository: fortuneRepo, Authority: v, AllowAnonymousCatalogue: c.Fortunes.AllowAnonymousCatalogue}).Handler()
	}
	progressionRepo := progressionpg.NewWithAuthority(db, guard)
	if fortuneRepo != nil {
		progressionRepo = progressionpg.NewWithCheckInReports(db, guard, fortuneRepo)
	}
	if c.ProgressionSourcePoints != nil {
		p, e := progressionpg.NewFundedSourcePoints(*c.ProgressionSourcePoints)
		if e != nil {
			return accountLifecycleModules{}, e
		}
		progressionRepo.WithSourcePointPayout(p)
	}
	if c.ProgressionCheckIn != nil {
		progressionRepo.WithCheckInPerks(commerceRepo)
		if e := progressionRepo.WithCheckInPolicy(*c.ProgressionCheckIn); e != nil {
			return accountLifecycleModules{}, e
		}
	}
	// 跨服务传递原始鉴权证明，连接、活动和历史共享同一个活动仓库。
	var bindProof func(context.Context, authority.Principal) context.Context
	if guard != nil {
		bindProof = guard.Bind
	}
	activityRepo := activitypg.New(db, guard).WithSteamProvider(c.SteamPresence).WithProvider("lastfm", c.LastFMPresence)
	accountConnections, e := accountConnectionsFor(db, guard, v, c.AccountConnections)
	if e != nil {
		return accountLifecycleModules{}, e
	}
	accountHistoryRepo := accounthistorypg.New(db, guard, activityRepo)
	accountHistoryHTTP := (&history.Service{Repository: accountHistoryRepo, Authority: v, Bind: bindProof}).Handler()
	friendsHTTP := (&friends.Service{Repository: activityRepo, Authority: v, Bind: bindProof}).Handler()
	activityHTTP := (&activities.Service{Repository: activityRepo, Authority: v, Bind: bindProof}).Handler()
	presenceHTTP := (&presence.Service{Repository: presenceRepo, Authority: v, Bind: bindProof}).Handler()
	progressionHTTP := (&progression.Service{Repository: progressionRepo, Authority: v, Bind: bindProof, Captcha: c.ProgressionCaptcha}).Handler()
	// 先创建奖励票据，再将邀请准入和登录激活接回票据。
	var ticketRepo *ticketpg.Repository
	var ticketHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "TICKETS_AUTHORITY_UNAVAILABLE") })
	if guard != nil {
		ticketConfig := ticketpg.Config{}
		if c.Tickets != nil {
			ticketConfig = *c.Tickets
		}
		if ticketConfig.Rewards == nil {
			ticketConfig.Rewards = progressionRepo
		}
		ticketRepo, err = ticketpg.New(db, guard, ticketConfig)
		if err != nil {
			return accountLifecycleModules{}, err
		}
		ticketHTTP = bindIdentity(v, (&tickets.Service{Repository: ticketRepo, Authority: proofIdentity{identity}}).Handler())
	}
	var affiliationRepo *affiliationpg.Repository
	var affiliationHTTP http.Handler = (&affiliation.Service{}).Handler()
	if guard != nil {
		affiliationRepo, err = affiliationpg.New(db, guard, affiliationpg.Config{Now: c.Authority.Now, Activation: ticketRepo, Purchase: c.AffiliationPurchase})
		if err != nil {
			return accountLifecycleModules{}, err
		}
		ticketRepo.WithInviteAdmission(affiliationRepo)
		signinRepo.WithAffiliation(affiliationRepo).WithActivation(ticketRepo)
		affiliationHTTP = (&affiliation.Service{Repository: affiliationRepo, Authority: v}).Handler()
	}
	return accountLifecycleModules{
		presenceRepo:       presenceRepo,
		fortuneRepo:        fortuneRepo,
		fortuneHTTP:        fortuneHTTP,
		progressionRepo:    progressionRepo,
		bindProof:          bindProof,
		activityRepo:       activityRepo,
		accountConnections: accountConnections,
		accountHistoryRepo: accountHistoryRepo,
		accountHistoryHTTP: accountHistoryHTTP,
		friendsHTTP:        friendsHTTP,
		activityHTTP:       activityHTTP,
		presenceHTTP:       presenceHTTP,
		progressionHTTP:    progressionHTTP,
		ticketRepo:         ticketRepo,
		ticketHTTP:         ticketHTTP,
		affiliationRepo:    affiliationRepo,
		affiliationHTTP:    affiliationHTTP,
	}, nil
}
