package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir); t.Setenv("HOME", dir)
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
	t.Setenv("USERPROFILE", t.TempDir()); t.Setenv("HOME", t.TempDir())
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

func TestParseContextWindow(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"128k", 128000},
		{"128K", 128000},
		{"1.5m", 1500000},
		{"1M", 1000000},
		{"200000", 200000},
		{" 8k ", 8000},
		{"0.5m", 500000},
	}
	for _, tc := range cases {
		got, err := ParseContextWindow(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseContextWindow(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", " ", "abc", "k", "12x", "-128k", "0", "-5"} {
		if _, err := ParseContextWindow(bad); err == nil {
			t.Errorf("ParseContextWindow(%q) should fail", bad)
		}
	}
}

func TestModelContextValidateAndNormalize(t *testing.T) {
	p := Provider{Name: "x", BaseURL: "https://a.com",
		ModelContext: map[string]int{"m1": 128000, "m2": -1}}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "正整数") {
		t.Fatalf("negative context should be rejected, got %v", err)
	}
	p.ModelContext["m2"] = 0
	if err := p.Validate(); err == nil {
		t.Fatal("zero context should also be rejected by Validate")
	}
	delete(p.ModelContext, "m2")
	p.ModelContext[" "] = 100
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "空模型名") {
		t.Fatalf("blank model name should be rejected, got %v", err)
	}
	p.ModelContext["m2"] = 0 // 与 " " 一并交给清洗
	p.cleanModelContext()
	if _, ok := p.ModelContext["m2"]; ok {
		t.Error("zero context entry should be cleaned by fillDefaults")
	}
	if _, ok := p.ModelContext[" "]; ok {
		t.Error("blank key should be cleaned by fillDefaults")
	}
	if p.ModelContext["m1"] != 128000 {
		t.Error("valid entry lost")
	}
}

func TestModelContextRoundtrip(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir()); t.Setenv("HOME", t.TempDir())
	c := Default()
	c.Providers = []Provider{{
		Name: "glm", BaseURL: "https://open.bigmodel.cn/api/paas/v4",
		ModelContext:  map[string]int{"glm-4.7": 128000, "glm-4-flash": 8000},
		ModelMaxOutput: map[string]int{"glm-4.7": 8192, "glm-4-flash": 4096, "stale": 0},
	}}
	c.Active = "glm"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Providers[0].ModelContext["glm-4.7"] != 128000 || got.Providers[0].ModelContext["glm-4-flash"] != 8000 {
		t.Fatalf("modelContext roundtrip mismatch: %+v", got.Providers[0].ModelContext)
	}
	if got.Providers[0].ModelMaxOutput["glm-4.7"] != 8192 || got.Providers[0].ModelMaxOutput["glm-4-flash"] != 4096 {
		t.Fatalf("modelMaxOutput roundtrip mismatch: %+v", got.Providers[0].ModelMaxOutput)
	}
	if _, ok := got.Providers[0].ModelMaxOutput["stale"]; ok {
		t.Fatal("非正值 modelMaxOutput 条目应被清洗")
	}
}

func TestModelMaxOutputValidate(t *testing.T) {
	p := Provider{Name: "x", BaseURL: "https://a.com",
		ModelMaxOutput: map[string]int{"m1": 8192, "m2": -1}}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "最大输出") {
		t.Fatalf("negative max output should be rejected, got %v", err)
	}
	p.ModelMaxOutput["m2"] = 4096
	if err := p.Validate(); err != nil {
		t.Fatalf("valid max output rejected: %v", err)
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
