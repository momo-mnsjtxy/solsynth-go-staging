// SPDX-License-Identifier: AGPL-3.0-only
// One in-process modular domain host. It neither discovers credentials nor
// initializes production schemas, users, grants, signing keys or payment policy.
package main

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"solsynth.local/go-migration/internal/application"
	"solsynth.local/go-migration/internal/authoritypg"
	"solsynth.local/go-migration/internal/mediastore"
	"solsynth.local/go-migration/internal/messagerpg"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/authority/signin"
	"solsynth.local/go-migration/ports/media"
	"solsynth.local/go-migration/ports/wallet/exchangecatalog"
)

func explicitInt(value, name string) (int32, error) {
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("explicit signed-int32 %s is required", name)
	}
	return int32(n), nil
}
func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("backend", flag.ContinueOnError)
	fs.SetOutput(out)
	listen := fs.String("listen", "127.0.0.1:8090", "loopback HTTP address behind trusted TLS ingress")
	serve := fs.Bool("serve", false, "explicitly connect and serve; otherwise validate configuration only")
	dsnEnv := fs.String("dsn-env", "SOLSYNTH_BACKEND_DSN", "injected environment variable with explicit PostgreSQL URL")
	issuer := fs.String("issuer", "", "exact issuer of owned authority tokens")
	audience := fs.String("audience", "", "exact owned token audience")
	kid := fs.String("key-id", "", "explicit public signing-key identifier")
	keyFile := fs.String("trusted-public-key-file", "", "explicit RSA public PEM, no certificates or private keys")
	maxLifetime := fs.Duration("max-access-lifetime", 10*time.Minute, "maximum access-token lifetime, matching issuer policy")
	public := fs.String("taxonomy-public-visibility", "", "explicit public visibility enum stored in database")
	none := fs.String("messager-none-encryption", "", "explicit unencrypted room mode stored in database")
	all := fs.String("messager-notify-all", "", "explicit all-notifications mode stored in database")
	mentions := fs.String("messager-notify-mentions", "", "explicit mentions-only notification mode")
	noNotify := fs.String("messager-notify-none", "", "explicit no-notifications mode")
	verificationSettings := verificationFlags(fs)
	factorIssuer := fs.String("factor-issuer-label", "SoSYS", "human-readable TOTP issuer label")
	enableSignin := fs.Bool("enable-signin", false, "explicitly enable owned first-party issuer and factor protocol")
	signingFile := fs.String("issuer-private-key-file", "", "explicit RSA issuer key PEM")
	refreshEnv := fs.String("issuer-refresh-hash-env", "", "explicit base64 refresh-hash key variable")
	replayEnv := fs.String("issuer-replay-seal-env", "", "explicit independent base64 refresh-seal key variable")
	signinEnv := fs.String("signin-seal-env", "", "explicit independent base64 factor-seal key variable")
	memberGrants := fs.String("initial-member-permissions", "", "explicit comma-separated concrete initial registration permissions")
	notificationApp := fs.String("notification-app", "dev.solsynth.solian", "explicit first-party inbox and stream namespace")
	mediaRoot := fs.String("media-root", "", "explicit preprovisioned private filesystem media directory")
	mediaMax := fs.Int64("media-max-bytes", 20<<20, "maximum streamed media object bytes")
	catalogFile := fs.String("catalog-config", "", "optional explicit catalog JSON snapshot")
	badgeIcons := fs.String("badge-icons", "", "optional directory of licensed badge icons")
	pepperEnv := fs.String("pin-pepper-env", "", "optional explicit environment variable with base64 PIN pepper")
	pepperID := fs.String("pin-pepper-id", "", "key ID for that externally provisioned PIN policy")
	settlement := fs.Duration("settlement-interval", 15*time.Minute, "bounded durable wallet lifecycle interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !loopback(*listen) || *issuer == "" || *audience == "" || *kid == "" || *settlement < time.Second || *settlement > 24*time.Hour {
		return errors.New("explicit authority settings, loopback listen and valid settlement interval required")
	}
	// A service file can override host settings before pgx finishes parsing. Never
	// discover or read it, even if an ambient developer shell happened to set one.
	if err := ambientPostgres(); err != nil {
		return err
	}
	dsn, err := explicitDSN(os.Getenv(*dsnEnv))
	if err != nil {
		return err
	}
	pubKey, err := publicKey(*keyFile)
	if err != nil {
		return err
	}
	config := authority.Config{Issuer: *issuer, Audience: *audience, TrustedKeys: map[string]*rsa.PublicKey{*kid: pubKey}, AccessTTL: *maxLifetime}
	if _, err = authority.NewVerifier(authoritypg.New(nil), config); err != nil {
		return errors.New("invalid authority verification configuration")
	}
	var signinConfig *signin.Config
	if *enableSignin {
		config.SigningKeyID = *kid
		config.SigningKey, err = issuerKey(*signingFile)
		if err != nil {
			return err
		}
		config.RefreshHashKey, err = issuerSecret(*refreshEnv)
		if err != nil {
			return err
		}
		config.ReplaySealKey, err = issuerSecret(*replayEnv)
		if err != nil {
			return err
		}
		seal, e := issuerSecret(*signinEnv)
		if e != nil {
			return e
		}
		if base64.StdEncoding.EncodeToString(seal) == base64.StdEncoding.EncodeToString(config.RefreshHashKey) || base64.StdEncoding.EncodeToString(seal) == base64.StdEncoding.EncodeToString(config.ReplaySealKey) {
			return errors.New("factor seal key must be independent")
		}
		if _, err = authority.New(authoritypg.New(nil), config); err != nil {
			return errors.New("invalid explicit issuer configuration")
		}
		signinConfig = &signin.Config{SealKey: seal, Issuer: *factorIssuer}
		if *memberGrants != "" {
			for _, permission := range strings.Split(*memberGrants, ",") {
				permission = strings.TrimSpace(permission)
				if !authority.ValidPermissionPattern(permission) || strings.Contains(permission, "*") {
					return errors.New("initial permissions must be concrete keys")
				}
				signinConfig.InitialMemberPermissions = append(signinConfig.InitialMemberPermissions, permission)
			}
		}
	} else if *signingFile != "" || *refreshEnv != "" || *replayEnv != "" || *signinEnv != "" || *memberGrants != "" {
		return errors.New("issuer configuration requires explicit enable-signin")
	}
	if err = verificationSettings.apply(signinConfig); err != nil {
		return err
	}
	publicVisibility, err := explicitInt(*public, "taxonomy-public-visibility")
	if err != nil {
		return err
	}
	encryption, err := explicitInt(*none, "messager-none-encryption")
	if err != nil {
		return err
	}
	notifyAll, err := explicitInt(*all, "messager-notify-all")
	if err != nil {
		return err
	}
	notifyMentions, err := explicitInt(*mentions, "messager-notify-mentions")
	if err != nil {
		return err
	}
	notifyNone, err := explicitInt(*noNotify, "messager-notify-none")
	if err != nil {
		return err
	}
	policy := messagerpg.IndependentPolicy{NoneEncryptionMode: encryption, NotifyAll: notifyAll, NotifyMentions: notifyMentions, NotifyNone: notifyNone}
	if _, err = messagerpg.NewWithIndependentPolicy(nil, policy); err != nil {
		return errors.New("notification enum values must be distinct")
	}
	peppers := map[string][]byte{}
	if (*pepperEnv == "") != (*pepperID == "") {
		return errors.New("PIN pepper environment and ID must be supplied together")
	}
	if *pepperEnv != "" {
		raw, err := base64.StdEncoding.DecodeString(os.Getenv(*pepperEnv))
		if err != nil || len(raw) < 32 || len(*pepperID) > 64 {
			return errors.New("invalid injected PIN pepper")
		}
		defer clear(raw)
		peppers[*pepperID] = raw
	}
	var objectStore media.ObjectStore
	if *mediaMax <= 0 || *mediaMax > 1<<30 {
		return errors.New("media maximum must be positive and at most one GiB")
	}
	if *mediaRoot != "" {
		store, e := mediastore.NewFilesystem(*mediaRoot, *mediaMax)
		if e != nil {
			return errors.New("media root unavailable or not private")
		}
		defer store.Close()
		objectStore = store
	}
	var catalogProviders []exchangecatalog.Provider
	if *catalogFile != "" {
		file, e := os.Open(*catalogFile)
		if e != nil {
			return errors.New("catalog configuration unavailable")
		}
		provider, e := exchangecatalog.LoadJSON(file)
		closeErr := file.Close()
		if e != nil || closeErr != nil {
			return errors.New("catalog configuration invalid")
		}
		catalogProviders = []exchangecatalog.Provider{provider}
	}
	if !*serve {
		_, err = fmt.Fprintln(out, "Backend configuration validated. No database connection, schema, account, key, grant, listener or transfer was created.")
		return err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return errors.New("database configuration unavailable")
	}
	defer db.Close()
	db.SetMaxOpenConns(30)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	backend, err := application.NewPostgres(db, nil, application.Config{Authority: config, PublicVisibility: &publicVisibility, MessagerPolicy: &policy, PINPeppers: peppers, CatalogProviders: catalogProviders, BadgeIconsPath: *badgeIcons, MediaStore: objectStore, MaxMediaBytes: *mediaMax, NotificationApp: *notificationApp, Signin: signinConfig})
	if err != nil {
		return errors.New("backend configuration unavailable")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = db.PingContext(check)
	if err == nil {
		err = backend.Ready(check)
	}
	cancel()
	if err != nil {
		return errors.New("required database/schema unavailable; startup performs no migrations")
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return errors.New("backend listener unavailable")
	}
	defer listener.Close()
	go func() {
		_ = backend.RunNotifications(ctx, func(code string) { fmt.Fprintln(out, "Notification worker:", code) })
	}()
	server := &http.Server{Handler: backend, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32768}
	go func() {
		ticker := time.NewTicker(*settlement)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				job, cancel := context.WithTimeout(ctx, 20*time.Second)
				_, err := backend.ProcessDue(job, 100)
				cancel()
				if err != nil {
					fmt.Fprintln(out, "Wallet lifecycle batch failed; committed state remains durable.")
				}
				cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 20*time.Second)
				_, cleanupErr := backend.CleanupMedia(cleanupCtx, 100)
				cleanupCancel()
				if cleanupErr != nil {
					fmt.Fprintln(out, "Media cleanup batch failed; durable reservations retained.")
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Fprintf(out, "Authenticated backend listening on %s\n", listener.Addr())
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("backend HTTP server stopped unexpectedly")
	}
	return nil
}
func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "backend:", err)
		os.Exit(1)
	}
}
