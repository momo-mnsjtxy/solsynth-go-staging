// SPDX-License-Identifier: AGPL-3.0-only
// 账号日历组装：标准活动与运势报告叠加，媒体引用沿用统一存储。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：calendarFor(db, config, verifier, guard, fortunes, calendarMedia)
package application

import (
	"database/sql"
	"net/http"

	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/calendarmediapg"
	"solsynth.local/go-migration/internal/fortunepg"
	"solsynth.local/go-migration/internal/notabledaypg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/passport/notabledays"
)

type calendarModules struct {
	calendarRepo *notabledaypg.Repository
	calendarHTTP http.Handler
}

func calendarFor(
	db *sql.DB,
	c Config,
	v *authority.Verifier,
	guard *authoritypg.TransactionGuard,
	fortuneRepo *fortunepg.Repository,
	calendarMedia *calendarmediapg.Repository,
) (calendarModules, error) {
	identity := identity{v}
	var err error
	var calendarRepo *notabledaypg.Repository
	var calendarHTTP http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { problem(w, 503, "CALENDAR_AUTHORITY_UNAVAILABLE") })
	// 日历写入依赖事务守卫，存在运势仓库时才叠加对应活动。
	if guard != nil {
		var activity notabledaypg.ActivityOverlay = notabledaypg.CanonicalActivityOverlay{}
		if fortuneRepo != nil {
			activity = fortunepg.CalendarOverlay{Base: activity, Reports: fortuneRepo}
		}
		calendarRepo, err = notabledaypg.New(db, guard, notabledaypg.Config{Now: c.Authority.Now, Activity: activity})
		if err != nil {
			return calendarModules{}, err
		}
		if calendarMedia != nil {
			calendarRepo.WithMedia(calendarMedia)
		}
		calendarHTTP = bindIdentity(v, (&notabledays.Service{Repository: calendarRepo, Authority: proofIdentity{identity}}).Handler())
	}
	return calendarModules{
		calendarRepo: calendarRepo,
		calendarHTTP: calendarHTTP,
	}, nil
}
