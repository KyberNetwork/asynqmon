package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigFile(t *testing.T) {
	t.Setenv("ASYNQMON_TEST_PASSWORD", `p@ss"word #1: x`)
	path := writeConfig(t, `
# Comments may mention ${UNDEFINED_IN_COMMENT} without it being required.
port: 9090
read_only: true
max_payload_length: 50
instances:
  - id: prod
    name: "Production"
    redis_addr: "prod:6379"
    redis_db: 2
    redis_password: "${ASYNQMON_TEST_PASSWORD}"
    redis_dial_timeout: "7s"
  - id: staging
    redis_url: "redis://:bar@staging:6381/3"
`)
	cfg := &Config{ConfigFile: path, Port: 8080, MaxPayloadLength: 200, MaxResultLength: 200}
	instances, err := loadInstances(cfg)
	if err != nil {
		t.Fatalf("loadInstances returned error: %v", err)
	}

	if cfg.Port != 9090 || !cfg.ReadOnly || cfg.MaxPayloadLength != 50 || cfg.MaxResultLength != 200 {
		t.Errorf("global settings not applied correctly: %+v", cfg)
	}
	if len(instances) != 2 {
		t.Fatalf("got %d instances, want 2", len(instances))
	}

	prod := instances[0]
	if prod.ID != "prod" || prod.Name != "Production" {
		t.Errorf("prod: got id=%q name=%q", prod.ID, prod.Name)
	}
	opt, ok := prod.RedisConnOpt.(asynq.RedisClientOpt)
	if !ok {
		t.Fatalf("prod: got conn opt %T, want asynq.RedisClientOpt", prod.RedisConnOpt)
	}
	if opt.Addr != "prod:6379" || opt.DB != 2 || opt.Password != `p@ss"word #1: x` || opt.DialTimeout != 7*time.Second {
		t.Errorf("prod: unexpected conn opt %+v", opt)
	}

	staging := instances[1]
	if staging.Name != "staging" {
		t.Errorf("staging: name should default to id, got %q", staging.Name)
	}
	if opt := staging.RedisConnOpt.(asynq.RedisClientOpt); opt.Addr != "staging:6381" || opt.DB != 3 {
		t.Errorf("staging: unexpected conn opt %+v", opt)
	}
}

func TestLoadConfigFileErrors(t *testing.T) {
	tests := []struct {
		desc    string
		content string
		wantErr string
	}{
		{
			desc:    "no instances",
			content: "port: 8080\n",
			wantErr: "at least one instance",
		},
		{
			desc:    "empty file",
			content: "",
			wantErr: "at least one instance",
		},
		{
			desc:    "undefined env var",
			content: "instances:\n  - id: a\n    redis_password: \"${ASYNQMON_TEST_UNDEFINED}\"\n",
			wantErr: "ASYNQMON_TEST_UNDEFINED",
		},
		{
			desc:    "unknown key",
			content: "instances:\n  - id: a\n    redis_pasword: secret\n",
			wantErr: "redis_pasword",
		},
		{
			desc:    "invalid id",
			content: "instances:\n  - id: \"a/b\"\n",
			wantErr: "URL-safe",
		},
		{
			desc:    "missing id",
			content: "instances:\n  - name: x\n",
			wantErr: "URL-safe",
		},
		{
			desc:    "duplicate id",
			content: "instances:\n  - id: a\n  - id: a\n",
			wantErr: "duplicate",
		},
		{
			desc:    "invalid duration",
			content: "instances:\n  - id: a\n    redis_read_timeout: soon\n",
			wantErr: "redis_read_timeout",
		},
	}
	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			cfg := &Config{ConfigFile: writeConfig(t, tc.content)}
			_, err := loadInstances(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("got error %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadInstancesWithoutConfigFile(t *testing.T) {
	cfg := &Config{Redis: RedisConfig{Addr: "localhost:6380", DB: 1}}
	instances, err := loadInstances(cfg)
	if err != nil {
		t.Fatalf("loadInstances returned error: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "default" {
		t.Fatalf("got %+v, want a single default instance", instances)
	}
	if opt := instances[0].RedisConnOpt.(asynq.RedisClientOpt); opt.Addr != "localhost:6380" || opt.DB != 1 {
		t.Errorf("unexpected conn opt %+v", opt)
	}
}
