package config

import (
	"strings"
	"testing"
)

// required sets everything Load insists on, so each test changes one thing.
func required(t *testing.T) {
	t.Helper()
	t.Setenv("JWT_SECRET", "secret")
	t.Setenv("DB_USER", "user")
	t.Setenv("DB_NAME", "db")
	t.Setenv("S3_BUCKET", "bucket")
	t.Setenv("GOOGLE_MAPS_API_KEY", "maps")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
}

func TestLoadWithoutAWSKeysUsesDefaultCredentials(t *testing.T) {
	required(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("no keys should be allowed (task role on ECS): %v", err)
	}
	if cfg.AWSAccessKeyID != "" || cfg.AWSSecretKey != "" {
		t.Fatalf("keys = %q/%q, want empty", cfg.AWSAccessKeyID, cfg.AWSSecretKey)
	}
}

func TestLoadWithBothAWSKeys(t *testing.T) {
	required(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRefusesOneAWSKeyAlone(t *testing.T) {
	required(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("err = %v, want a complaint about the missing secret key", err)
	}
}

func TestLoadRequiresBucket(t *testing.T) {
	required(t)
	t.Setenv("S3_BUCKET", "")
	if _, err := Load(); err == nil {
		t.Fatal("want an error without S3_BUCKET")
	}
}
