// SPDX-License-Identifier: AGPL-3.0-only
// 开发者应用与交易组装：应用目录、机器人额度、商户商品及板块共享同一依赖。
// 调用：由 NewPostgres 在依赖仓库就绪后组装；不访问数据库、不启动后台任务。
// 示例：developerCommerceFor(db, config, verifier, guard, authorityStore, paymentPIN)
package application

import (
	"database/sql"
	"errors"
	"net/http"

	"solsynth.local/go-migration/internal/apppg"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/boardpg"
	"solsynth.local/go-migration/internal/botpg"
	"solsynth.local/go-migration/internal/commercepg"
	"solsynth.local/go-migration/internal/merchantpg"
	"solsynth.local/go-migration/internal/walletpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/develop/apps"
	"solsynth.local/go-migration/ports/passport/board"
	walletaccounts "solsynth.local/go-migration/ports/wallet/accounts"
	"solsynth.local/go-migration/ports/wallet/commerce"
	"solsynth.local/go-migration/ports/wallet/merchants"
	"solsynth.local/go-migration/ports/wallet/productcatalog"
)

type developerCommerceModules struct {
	appRepo      *apppg.Repository
	botRepo      *botpg.Repository
	merchantHTTP http.Handler
	commerceRepo *commercepg.Repository
	commerceHTTP http.Handler
	appHTTP      http.HandlerFunc
	boardHTTP    http.Handler
}

func developerCommerceFor(
	db *sql.DB,
	c Config,
	v *authority.Verifier,
	guard *authoritypg.TransactionGuard,
	aStore *authoritypg.Repository,
	paymentPIN *walletpg.PINVerifier,
) (developerCommerceModules, error) {
	identity := identity{v}
	var err error
	// 应用与板块共用缓存，机器人额度直接挂到应用服务。
	cache := board.NewGroupCache()
	appRepo := apppg.NewWithAuthority(db, guard).WithBoardCache(cache)
	appService := &apps.Service{Repository: appRepo, Authority: proofIdentity{identity}}
	var botRepo *botpg.Repository
	if guard != nil {
		botRepo = botpg.NewWithAuthority(db, guard)
		appService.Quota = botRepo
		if c.MiniAppUploadTimeout != 0 {
			if err = botRepo.ConfigurePackageUploadTimeout(c.MiniAppUploadTimeout); err != nil {
				return developerCommerceModules{}, err
			}
		}
	}
	// 扣款、优惠与产品订单绑定同一支付校验和事务守卫。
	merchantRepo := merchantpg.New(db, guard)
	merchantHTTP := merchants.NewHandler(merchantRepo, postIdentity{identity})
	if !c.CommerceCouponPolicy.Empty() && !c.CommerceCouponPolicy.Valid() {
		return developerCommerceModules{}, errors.New("invalid explicit coupon policy")
	}
	commerceRepo := commercepg.New(db, guard, commercepg.AppAdapters(appRepo)).WithPaymentAuthorization(paymentPIN).WithCouponPolicy(c.CommerceCouponPolicy)
	appRepo.WithProductCatalog(commerceRepo)
	appService.CustomOrders = commercepg.NewManagedOrders(commerceRepo, appRepo)
	if c.Authority.Now != nil {
		commerceRepo.WithClock(c.Authority.Now)
		merchantRepo.WithClock(c.Authority.Now)
	}
	merchantRepo.WithMaintenance(commerceRepo)
	if catalog, catalogErr := productcatalog.New(c.CatalogProviders...); catalogErr == nil {
		if raw, jsonErr := catalog.JSON(); jsonErr == nil {
			merchantRepo.WithWalletProductCatalog(raw)
			if walletaccounts.ValidID(c.WalletProductTreasuryWalletID) {
				merchantRepo.WithWalletProductTreasury(c.WalletProductTreasuryWalletID)
			}
		}
	}
	commerceHTTP := (&commerce.Service{Store: commerceRepo, Identity: commerceIdentity{identity}, Payments: paymentPIN.Atomic(), Providers: c.CommerceProviders}).Handler()
	// 应用凭证路由保留独立验证入口；普通路由继续使用账号事务凭证。
	appNormal := bindIdentity(v, bindTransactionAuthority(guard, appService.Handler()))
	appHTTP := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apps.IsAppCredentialRoute(r) {
			appService.AppHandler().ServeHTTP(w, r)
			return
		}
		appNormal.ServeHTTP(w, r)
	})
	bRepo := boardpg.NewWithAuthority(db, guard, nil)
	boardHTTP := board.NewHTTPHandler(board.HTTPOptions{Service: board.Service{Repository: bRepo, Catalog: appRepo, Normalizer: board.DocumentedEnvelope{}, Cache: cache}, Authority: boardIdentity{identity}, Accounts: accountResolver{aStore}, AccountCache: cache, Delete: bRepo})
	return developerCommerceModules{
		appRepo:      appRepo,
		botRepo:      botRepo,
		merchantHTTP: merchantHTTP,
		commerceRepo: commerceRepo,
		commerceHTTP: commerceHTTP,
		appHTTP:      appHTTP,
		boardHTTP:    boardHTTP,
	}, nil
}
