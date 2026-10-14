package main

import (
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	env := map[string]string{
		"TEMOVEX_ADDR": "192.168.3.2:26486",
	}
	cfg, err := loadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.temovexAddr != "192.168.3.2:26486" {
		t.Fatalf("addr = %s", cfg.temovexAddr)
	}
	if cfg.httpAddr != ":8080" {
		t.Fatalf("http = %s", cfg.httpAddr)
	}
	if cfg.dialTimeout != 10*time.Second || cfg.readTimeout != 3*time.Second {
		t.Fatalf("timeouts = %s %s", cfg.dialTimeout, cfg.readTimeout)
	}
}

func TestLoadConfigRequiresHostPort(t *testing.T) {
	_, err := loadConfig(func(string) string { return "" })
	if err == nil || err.Error() == "" {
		t.Fatal("expected missing address error")
	}

	_, err = loadConfig(func(key string) string {
		if key == "TEMOVEX_ADDR" {
			return "192.168.3.2"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected host:port error")
	}
}

func TestLoadConfigRejectsBadDuration(t *testing.T) {
	env := map[string]string{
		"TEMOVEX_ADDR":         "ventilation.localdomain:26486",
		"HTTP_ADDR":            "127.0.0.1:9090",
		"TEMOVEX_READ_TIMEOUT": "0s",
	}
	_, err := loadConfig(func(key string) string { return env[key] })
	if err == nil {
		t.Fatal("expected duration error")
	}

	env["TEMOVEX_READ_TIMEOUT"] = "nope"
	_, err = loadConfig(func(key string) string { return env[key] })
	if err == nil {
		t.Fatal("expected parse error")
	}

	env["TEMOVEX_READ_TIMEOUT"] = "5s"
	env["TEMOVEX_DIAL_TIMEOUT"] = "1s"
	cfg, err := loadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.httpAddr != "127.0.0.1:9090" || cfg.readTimeout != 5*time.Second || cfg.dialTimeout != time.Second {
		t.Fatalf("cfg = %+v", cfg)
	}
}
