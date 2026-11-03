package config

import (
	"strings"
	"testing"
	"time"
)

// The three feature predicates below decide whether a whole surface is on, so
// each answers for every spelling an operator might deploy, and for the values
// that mean "not configured" rather than "off".
func TestFeaturePredicates(t *testing.T) {
	cases := []struct {
		store string
		want  bool
	}{
		{"", false},
		{FilesStoreOff, false},
		{FilesStoreLocal, true},
		{FilesStoreS3, true},
		{"  LOCAL  ", true},
		{"s3 ", true},
		{"rustfs", false},
	}
	for _, tc := range cases {
		c := &Config{FilesStore: tc.store}
		if got := c.FilesEnabled(); got != tc.want {
			t.Fatalf("FilesEnabled(%q) = %v, want %v", tc.store, got, tc.want)
		}
	}

	for _, mode := range []string{"", "enforce", " ENFORCE "} {
		c := &Config{RLSMode: mode}
		if !c.RLSEnforced() {
			t.Fatalf("RLSEnforced(%q) = false, want the default to enforce", mode)
		}
	}
	if (&Config{RLSMode: RLSModeOff}).RLSEnforced() {
		t.Fatal("an explicit off must be reported as not enforced")
	}

	c := &Config{DatabaseURL: "postgres://runtime", MigrateDatabaseURL: "postgres://ddl"}
	if got := c.EffectiveMigrateURL(); got != "postgres://ddl" {
		t.Fatalf("migrate url = %q, want the dedicated role", got)
	}
	c.MigrateDatabaseURL = "   "
	if got := c.EffectiveMigrateURL(); got != "postgres://runtime" {
		t.Fatalf("migrate url = %q, want the runtime DSN when the DDL one is blank", got)
	}
}

// Turning the policies off in production has to be said twice, otherwise a
// staging value copied into production silently widens row visibility.
func TestValidateRefusesQuietRowLevelRelaxation(t *testing.T) {
	c := validBaseConfig()
	c.Env = "prod"
	c.RLSMode = RLSModeOff
	c.RLSAcknowledged = false
	c.BootstrapAdminEmail = "admin@example.com"
	c.EncryptionKey = strings.Repeat("k", 32)
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "AGENTHUB_RLS_ACK") {
		t.Fatalf("err = %v, want the acknowledgement requirement", err)
	}

	c.RLSAcknowledged = true
	if err := c.Validate(); err != nil {
		t.Fatalf("acknowledged off must be allowed: %v", err)
	}

	c.RLSMode = "sometimes"
	if err := c.Validate(); err == nil {
		t.Fatal("an unknown RLS mode must be refused")
	}
}

func TestValidateRejectsImpossibleRetention(t *testing.T) {
	c := validBaseConfig()
	c.AuditRetention = -time.Hour
	if err := c.Validate(); err == nil {
		t.Fatal("a negative retention must be refused")
	}

	c = validBaseConfig()
	c.AuditQueue = -1
	if err := c.Validate(); err == nil {
		t.Fatal("a negative queue must be refused")
	}

	c = validBaseConfig()
	c.AuditQueue = 10
	c.AuditBatch = 11
	if err := c.Validate(); err == nil {
		t.Fatal("a batch larger than the queue must be refused")
	}
}

// The shared file store is validated at startup: an operator who typed "S3 " or
// named a credential variable that resolves to nothing has to hear about it
// then, not when the first upload is rejected.
func TestValidateFilesBlock(t *testing.T) {
	c := validBaseConfig()
	c.FilesMaxBytes = 1
	if err := c.Validate(); err == nil {
		t.Fatal("a 1 byte cap must be refused")
	}

	c = validBaseConfig()
	c.FilesStore = "rustfs"
	if err := c.Validate(); err == nil {
		t.Fatal("an unknown store must be refused, not treated as off")
	}

	c = validBaseConfig()
	c.FilesStore = FilesStoreLocal
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "AGENTHUB_FILES_DIR") {
		t.Fatalf("err = %v, want the missing directory", err)
	}
	c.FilesDir = t.TempDir()
	if err := c.Validate(); err != nil {
		t.Fatalf("local store must be valid with a directory: %v", err)
	}
	if !c.FilesEnabled() {
		t.Fatal("a validated local store must report files enabled")
	}

	c = validBaseConfig()
	c.FilesStore = FilesStoreS3
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "AGENTHUB_FILES_S3_ENDPOINT") {
		t.Fatalf("err = %v, want the missing endpoint", err)
	}
	c.FilesS3Endpoint = "https://s3.example.com"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "AGENTHUB_FILES_S3_BUCKET") {
		t.Fatalf("err = %v, want the missing bucket", err)
	}
	c.FilesS3Bucket = "agenthub"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("err = %v, want the unnamed credential variables", err)
	}

	c.FilesS3AccessKeyEnv = "AGENTHUB_TEST_S3_ACCESS"
	c.FilesS3SecretKeyEnv = "AGENTHUB_TEST_S3_SECRET"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "AGENTHUB_TEST_S3_ACCESS") {
		t.Fatalf("err = %v, want the unset variable named", err)
	}

	t.Setenv("AGENTHUB_TEST_S3_ACCESS", "ak")
	t.Setenv("AGENTHUB_TEST_S3_SECRET", "sk")
	if err := c.Validate(); err != nil {
		t.Fatalf("a fully configured s3 store must be valid: %v", err)
	}
	if !c.FilesEnabled() {
		t.Fatal("a validated s3 store must report files enabled")
	}
}
