// SPDX-License-Identifier: AGPL-3.0-only
package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A database (not a search_path schema) isolates restart state from explicitly
// global maintenance tests. Only the runner may prepare the empty template.
// This helper never installs schemas, creates credentials or repairs rows.
type fixtureTemplate struct {
	Version            int        `json:"version"`
	Candidate          string     `json:"candidate"`
	SchemaSHA256       string     `json:"schema_sha256"`
	Name               string     `json:"name"`
	OID                uint32     `json:"oid"`
	Cluster            string     `json:"cluster"`
	Comment            string     `json:"comment"`
	SchemaFiles        []string   `json:"schema_files"`
	Extensions         [][]string `json:"extensions"`
	ApplicationTables  int        `json:"application_tables"`
	BaselineRowsCopied int        `json:"baseline_rows_copied"`
}

type isolatedRestartDatabase struct {
	Name     string          `json:"name"`
	OID      uint32          `json:"oid"`
	Comment  string          `json:"comment"`
	Template fixtureTemplate `json:"template"`
}

type disposableDatabaseConfig struct {
	root, parent, dsn, candidate, schemaHash, mode, ci, privateCI, genericCI string
	localTransport, localPort, localPassword                                 string
	githubEnvironment                                                        bool
}

var (
	fixtureRootPath = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
	fixtureRunName  = regexp.MustCompile(`^run\.[A-Za-z0-9]{8}$`)
	fixtureHex40    = regexp.MustCompile(`^[a-f0-9]{40}$`)
	fixtureHex64    = regexp.MustCompile(`^[a-f0-9]{64}$`)
	fixtureDBName   = regexp.MustCompile(`^solsynth_fixture_[a-z][a-z0-9_]{0,15}_[a-f0-9]{24}$`)
	fixturePurpose  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,15}$`)
	fixtureCluster  = regexp.MustCompile(`^[0-9]+$`)
)

// Local runs opt in explicitly and cannot borrow CI identity. An absent mode
// retains the existing private CI contract; unknown modes fail closed.
func validateDisposableExecutionMode(c disposableDatabaseConfig) error {
	switch c.mode {
	case "":
		if c.ci != "true" || c.privateCI != "true" {
			return errors.New("isolated fixture requires authorized private CI")
		}
		if c.localTransport != "" || c.localPort != "" || c.localPassword != "" {
			return errors.New("local transport settings are forbidden in private CI")
		}
	case "local-disposable":
		if c.genericCI != "" || c.ci != "" || c.privateCI != "" || c.githubEnvironment {
			return errors.New("local disposable execution cannot claim CI identity")
		}
	default:
		return errors.New("unknown disposable execution mode")
	}
	return nil
}

func hasGithubEnvironment(environment []string) bool {
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GITHUB_") && value != "" {
			return true
		}
	}
	return false
}

func disposableDatabaseEnvironment() disposableDatabaseConfig {
	parent := os.Getenv("PG_TEST_ROOT")
	if parent == "" {
		parent = "/workspace/shared/solsynth-go-integration"
	}
	return disposableDatabaseConfig{
		root: os.Getenv("SOLSYNTH_DISPOSABLE_PG"), parent: parent,
		dsn: os.Getenv("PG_TEST_DSN"), candidate: os.Getenv("PG_TEST_CANDIDATE"),
		schemaHash: os.Getenv("PG_TEST_SCHEMA_SHA256"), mode: os.Getenv("PG_TEST_MODE"),
		ci: os.Getenv("GITHUB_ACTIONS"), privateCI: os.Getenv("PG_TEST_PRIVATE_CI"),
		genericCI: os.Getenv("CI"), githubEnvironment: hasGithubEnvironment(os.Environ()),
		localTransport: os.Getenv("PG_TEST_LOCAL_TRANSPORT"), localPort: os.Getenv("PG_TEST_LOCAL_PORT"),
		localPassword: os.Getenv("PG_TEST_LOCAL_PASSWORD"),
	}
}

// Unix sockets remain the default. A local runner may explicitly select one
// password-authenticated loopback port when the host forbids Unix sockets.
func disposableExpectedDSN(c disposableDatabaseConfig) (string, error) {
	switch c.localTransport {
	case "":
		if c.localPort != "" || c.localPassword != "" {
			return "", errors.New("unexpected local transport credentials")
		}
		return "host=" + filepath.Join(c.root, "socket") + " port=55432 user=synthetic dbname=postgres sslmode=disable", nil
	case "loopback-tcp":
		port, err := strconv.Atoi(c.localPort)
		if c.mode != "local-disposable" || err != nil || port < 1024 || port > 65535 || strconv.Itoa(port) != c.localPort || !fixtureHex64.MatchString(c.localPassword) {
			return "", errors.New("invalid explicit local loopback transport configuration")
		}
		return "host=127.0.0.1 port=" + c.localPort + " user=synthetic dbname=postgres sslmode=disable password=" + c.localPassword, nil
	default:
		return "", errors.New("unknown disposable local transport")
	}
}

func validateDisposableDatabaseConfig(c disposableDatabaseConfig) error {
	if err := validateDisposableExecutionMode(c); err != nil {
		return err
	}
	expectedDSN, err := disposableExpectedDSN(c)
	if err != nil {
		return err
	}
	if !fixtureRootPath.MatchString(c.root) || !filepath.IsAbs(c.root) || !filepath.IsAbs(c.parent) || filepath.Clean(c.root) != c.root || filepath.Clean(c.parent) != c.parent || filepath.Dir(c.root) != c.parent || !fixtureRunName.MatchString(filepath.Base(c.root)) {
		return errors.New("isolated fixture requires the marked disposable root")
	}
	resolved, err := filepath.EvalSymlinks(c.root)
	if err != nil || resolved != c.root {
		return errors.New("disposable root cannot contain symlinks")
	}
	for _, name := range []string{c.root, filepath.Join(c.root, "socket")} {
		info, err := os.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return errors.New("disposable directories must be private")
		}
	}
	marker := filepath.Join(c.root, ".solsynth-disposable")
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("private disposable marker required")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "synthetic-only\n" {
		return errors.New("disposable marker mismatch")
	}
	if c.dsn != expectedDSN || !fixtureHex40.MatchString(c.candidate) || !fixtureHex64.MatchString(c.schemaHash) {
		return errors.New("exact disposable connection and source hashes required")
	}
	return nil
}

// The child backend accepts a PostgreSQL URL. Convert only the exact connection
// already validated above, and pass the result through the child's environment.
func backendProcessDSN(t *testing.T) string {
	t.Helper()
	c := disposableDatabaseEnvironment()
	if err := validateDisposableDatabaseConfig(c); err != nil {
		t.Fatal(err)
	}
	u := &url.URL{Scheme: "postgres", User: url.User("synthetic"), Host: "localhost:55432", Path: "/postgres"}
	q := url.Values{"sslmode": {"disable"}}
	if c.localTransport == "loopback-tcp" {
		u.Host = "127.0.0.1:" + c.localPort
		u.User = url.UserPassword("synthetic", c.localPassword)
	} else {
		q.Set("host", filepath.Join(c.root, "socket"))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// A TCP address alone does not identify the marked cluster. Before any fixture
// SQL, bind the connected server to the runner's exact data directory.
func verifyDisposableServerDirectory(ctx context.Context, db *sql.DB, c disposableDatabaseConfig) error {
	if c.mode != "local-disposable" || c.localTransport != "loopback-tcp" {
		return nil
	}
	var directory string
	if err := db.QueryRowContext(ctx, `SELECT current_setting('data_directory')`).Scan(&directory); err != nil || directory != filepath.Join(c.root, "data") {
		return errors.New("local disposable PostgreSQL directory does not match the marked cluster")
	}
	return nil
}

func validateFixtureTemplate(c disposableDatabaseConfig, m fixtureTemplate) error {
	nonce := sha256.Sum256([]byte(c.root + "\x00" + c.candidate))
	suffix := hex.EncodeToString(nonce[:])[:24]
	if m.Version != 1 || m.Candidate != c.candidate || m.SchemaSHA256 != c.schemaHash || m.OID == 0 || !fixtureCluster.MatchString(m.Cluster) || m.Name != "solsynth_template_"+suffix || m.Comment != "solsynth-fixture-template/v1/"+c.candidate+"/"+c.schemaHash+"/"+suffix || m.ApplicationTables <= 0 || m.BaselineRowsCopied != 0 || len(m.SchemaFiles) == 0 || len(m.Extensions) == 0 {
		return errors.New("empty template marker does not match this candidate, schema and cluster")
	}
	seen := map[string]bool{}
	for _, path := range m.SchemaFiles {
		if !strings.HasSuffix(path, ".sql") || filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, "../") || seen[path] {
			return errors.New("invalid template schema inventory")
		}
		seen[path] = true
	}
	return nil
}

func validateIsolatedRestartDatabase(c disposableDatabaseConfig, template fixtureTemplate, saved isolatedRestartDatabase) error {
	if err := validateFixtureTemplate(c, template); err != nil {
		return err
	}
	if !fixtureDBName.MatchString(saved.Name) || len(saved.Name) > 63 || saved.OID == 0 || !reflect.DeepEqual(saved.Template, template) || saved.Comment != "solsynth-fixture/v1/"+c.candidate+"/"+c.schemaHash+"/"+saved.Name {
		return errors.New("saved database identity does not match the original fixture")
	}
	return nil
}

func isolatedDatabaseConfig(t *testing.T) (disposableDatabaseConfig, fixtureTemplate) {
	t.Helper()
	if os.Getenv("PG_TEST_DSN") == "" {
		_ = database(t) // Preserve the ordinary offline skip/required-DSN contract.
	}
	c := disposableDatabaseEnvironment()
	if err := validateDisposableDatabaseConfig(c); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.root, "fixture-template.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		t.Fatal("private bounded template marker required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read template marker")
	}
	var m fixtureTemplate
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal("invalid template marker")
	}
	if err := validateFixtureTemplate(c, m); err != nil {
		t.Fatal(err)
	}
	return c, m
}

func connectDisposableDatabase(t *testing.T, c disposableDatabaseConfig, name string) *sql.DB {
	t.Helper()
	if err := validateDisposableDatabaseConfig(c); err != nil {
		t.Fatal(err)
	}
	if name != "template1" && !fixtureDBName.MatchString(name) {
		t.Fatal("invalid isolated database name")
	}
	// The exact runner DSN was validated first. Never accept a second DSN or an
	// environment override, and never print driver errors containing credentials.
	dsn := strings.Replace(c.dsn, "dbname=postgres", "dbname="+name, 1)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("cannot open isolated disposable database")
	}
	db.SetMaxIdleConns(0)
	t.Cleanup(func() { db.Close() })
	check, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := verifyDisposableServerDirectory(check, db, c); err != nil {
		t.Fatal(err)
	}
	return db
}

func verifyTemplateCatalog(ctx context.Context, db *sql.DB, m fixtureTemplate) error {
	var oid uint32
	var allow bool
	var comment, cluster string
	var connections int
	err := db.QueryRowContext(ctx, `SELECT d.oid,d.datallowconn,COALESCE(shobj_description(d.oid,'pg_database'),''),(SELECT system_identifier::text FROM pg_control_system()),(SELECT count(*) FROM pg_stat_activity WHERE datname=d.datname) FROM pg_database d WHERE datname=$1`, m.Name).Scan(&oid, &allow, &comment, &cluster, &connections)
	if err != nil || oid != m.OID || allow || comment != m.Comment || cluster != m.Cluster || connections != 0 {
		return errors.New("original sealed template identity is unavailable or changed")
	}
	return nil
}

func createIsolatedRestartDatabase(t *testing.T, purpose string) (*sql.DB, isolatedRestartDatabase) {
	t.Helper()
	if fixtureRestartPhase(t) != "before" || !fixturePurpose.MatchString(purpose) {
		t.Fatal("only before may create a named isolated fixture")
	}
	c, template := isolatedDatabaseConfig(t)
	admin := connectDisposableDatabase(t, c, "template1")
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := verifyTemplateCatalog(ctx, admin, template); err != nil {
		t.Fatal(err)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("cannot generate disposable database name")
	}
	saved := isolatedRestartDatabase{Name: "solsynth_fixture_" + purpose + "_" + hex.EncodeToString(nonce[:]), Template: template}
	saved.Comment = "solsynth-fixture/v1/" + c.candidate + "/" + c.schemaHash + "/" + saved.Name
	// Both identifiers are validated, bounded, locally generated names. The
	// source is sealed and all connections are closed; CREATE also fails busy.
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+saved.Name+`" WITH TEMPLATE "`+template.Name+`"`); err != nil {
		t.Fatal("cannot clone sealed disposable template")
	}
	if _, err := admin.ExecContext(ctx, `COMMENT ON DATABASE "`+saved.Name+`" IS '`+saved.Comment+`'`); err != nil {
		t.Fatal("cannot mark isolated disposable database")
	}
	if err := admin.QueryRowContext(ctx, `SELECT oid FROM pg_database WHERE datname=$1`, saved.Name).Scan(&saved.OID); err != nil || saved.OID == 0 {
		t.Fatal("cannot record isolated database identity")
	}
	return verifiedIsolatedDatabase(t, c, template, saved), saved
}

func openIsolatedRestartDatabase(t *testing.T, saved isolatedRestartDatabase) *sql.DB {
	t.Helper()
	if fixtureRestartPhase(t) != "after" {
		t.Fatal("saved isolated database opens only after the real restart")
	}
	c, template := isolatedDatabaseConfig(t)
	// No create/seed/install fallback: a missing database or changed OID fails.
	return verifiedIsolatedDatabase(t, c, template, saved)
}

func verifiedIsolatedDatabase(t *testing.T, c disposableDatabaseConfig, template fixtureTemplate, saved isolatedRestartDatabase) *sql.DB {
	t.Helper()
	if err := validateIsolatedRestartDatabase(c, template, saved); err != nil {
		t.Fatal(err)
	}
	admin := connectDisposableDatabase(t, c, "template1")
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := verifyTemplateCatalog(ctx, admin, template); err != nil {
		t.Fatal(err)
	}
	if err := verifyIsolatedCatalog(ctx, admin, saved); err != nil {
		t.Fatal(err)
	}
	db := connectDisposableDatabase(t, c, saved.Name)
	if err := verifyIsolatedConnection(ctx, db, saved); err != nil {
		t.Fatal(err)
	}
	return db
}

func verifyIsolatedCatalog(ctx context.Context, db *sql.DB, saved isolatedRestartDatabase) error {
	var oid uint32
	var allow bool
	var comment string
	if err := db.QueryRowContext(ctx, `SELECT oid,datallowconn,COALESCE(shobj_description(oid,'pg_database'),'') FROM pg_database WHERE datname=$1`, saved.Name).Scan(&oid, &allow, &comment); err != nil || oid != saved.OID || !allow || comment != saved.Comment {
		return errors.New("original isolated database missing or replaced; refusing to recreate it")
	}
	return nil
}

func verifyIsolatedConnection(ctx context.Context, db *sql.DB, saved isolatedRestartDatabase) error {
	var oid uint32
	var actualName, cluster string
	if err := db.QueryRowContext(ctx, `SELECT current_database(),d.oid,(SELECT system_identifier::text FROM pg_control_system()) FROM pg_database d WHERE datname=current_database()`).Scan(&actualName, &oid, &cluster); err != nil || actualName != saved.Name || oid != saved.OID || cluster != saved.Template.Cluster {
		return errors.New("isolated connection does not match the saved database and cluster")
	}
	return nil
}
