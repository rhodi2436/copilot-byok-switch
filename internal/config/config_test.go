package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	if got := Path(); !strings.HasSuffix(got, filepath.Join(".cops", "config.json")) {
		t.Fatalf("Path() = %s, want under %s", got, filepath.Join(dir, ".cops"))
	}

	c := Default()
	c.Providers = []Provider{{
		Name: "glm", BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		APIKey: "sk-test-1234567890", Model: "glm-4.7",
		Models: []string{"glm-4.7", "glm-4-flash"},
		Prices: map[string]ModelPrice{"glm-4.7": {InputPerMillionTokens: 0.5, OutputPerMillionTokens: 2}},
	}}
	c.Active = "glm"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Active != "glm" || len(got.Providers) != 1 || got.Providers[0].Model != "glm-4.7" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.Providers[0].Prices["glm-4.7"].InputPerMillionTokens != 0.5 {
		t.Fatal("price lost")
	}
}

func TestSaveCreatesBackup(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	c := Default()
	c.Providers = []Provider{{Name: "a", BaseURL: "https://a.example.com"}}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c.Providers = []Provider{{Name: "b", BaseURL: "https://b.example.com"}}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 1 || got.Providers[0].Name != "b" {
		t.Fatalf("want b, got %+v", got.Providers)
	}
	bak, err := LoadBackup()
	if err != nil || len(bak.Providers) != 1 || bak.Providers[0].Name != "a" {
		t.Fatalf("backup mismatch: %+v err=%v", bak, err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		p    Provider
		want bool
	}{
		{Provider{Name: "x", BaseURL: "https://api.example.com/v1/"}, true},
		{Provider{Name: "", BaseURL: "https://a.com"}, false},
		{Provider{Name: "a b", BaseURL: "https://a.com"}, false},
		{Provider{Name: "x", BaseURL: "notaurl"}, false},
		{Provider{Name: "x", BaseURL: "ftp://a.com"}, false},
		{Provider{Name: "x", BaseURL: "http://localhost:11434/v1"}, true},
	}
	for _, tc := range cases {
		err := tc.p.Validate()
		if (err == nil) != tc.want {
			t.Errorf("Validate(%+v) = %v, want ok=%v", tc.p, err, tc.want)
		}
	}
}

func TestActiveProvider(t *testing.T) {
	c := Default()
	if c.ActiveProvider() != nil {
		t.Fatal("empty active should return nil")
	}
	c.Providers = []Provider{{Name: "a", BaseURL: "https://a.com"}, {Name: "b", BaseURL: "https://b.com"}}
	c.Active = "b"
	if p := c.ActiveProvider(); p == nil || p.Name != "b" {
		t.Fatalf("want b, got %v", p)
	}
	c.Active = "missing"
	if c.ActiveProvider() != nil {
		t.Fatal("missing active should return nil")
	}
}

func TestPublicURL(t *testing.T) {
	c := Default()
	c.Listen = "127.0.0.1:8317"
	if got := c.PublicURL(); got != "http://127.0.0.1:8317/v1" {
		t.Fatalf("got %s", got)
	}
	c.Listen = "0.0.0.0:9000"
	if got := c.PublicURL(); got != "http://127.0.0.1:9000/v1" {
		t.Fatalf("got %s", got)
	}
}

func TestMaskKey(t *testing.T) {
	if MaskKey("sk-1234567890abcdef") != "sk-1...cdef" {
		t.Fatal(MaskKey("sk-1234567890abcdef"))
	}
	if MaskKey("short") != "*****" {
		t.Fatal(MaskKey("short"))
	}
	if MaskKey("") != "" {
		t.Fatal("empty")
	}
}
