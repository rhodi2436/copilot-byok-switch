package update

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"新补丁版", "v0.4.0", "v0.4.1", true},
		{"新次版本", "0.4.9", "0.5.0", true},
		{"新主版本", "v0.9.9", "v1.0.0", true},
		{"相同版本", "v0.4.1", "v0.4.1", false},
		{"相同版本无v", "0.4.1", "v0.4.1", false},
		{"旧版本", "v0.5.0", "v0.4.9", false},
		{"dev构建对正式版", "dev", "v0.4.1", true},
		{"dev-sha对正式版", "dev-a1b2c3d", "v0.1.0", true},
		{"段数不齐补零", "v0.4", "v0.4.1", true},
		{"段数不齐相等", "v0.4.0", "v0.4", false},
		{"大数字段", "v0.9.9", "v0.10.0", true},
		{"四段版本", "v0.4.0.1", "v0.4.0.2", true},
		{"后缀忽略", "v0.4.1-beta", "v0.4.1", false},
		{"latest非semver", "v0.4.1", "latest", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsNewer(c.current, c.latest); got != c.want {
				t.Fatalf("IsNewer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
			}
		})
	}
}
