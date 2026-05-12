package cli

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTencentBundleTypeCandidatesForClass(t *testing.T) {
	tests := []struct {
		class string
		want  []string
	}{
		{class: "standard", want: []string{"XL"}},
		{class: "fast", want: []string{"24GB_A"}},
		{class: "large", want: []string{"3XL"}},
		{class: "beast", want: []string{"4XL"}},
		{class: "custom", want: []string{"custom"}},
	}
	for _, tt := range tests {
		t.Run(tt.class, func(t *testing.T) {
			got := tencentBundleTypeCandidatesForClass(tt.class)
			if len(got) != len(tt.want) {
				t.Fatalf("candidates=%v want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("candidates=%v want %v", got, tt.want)
				}
			}
		})
	}
}

func TestTencentStaticHourlyPriceCNY(t *testing.T) {
	tests := []struct {
		bundle string
		want   float64
	}{
		{bundle: "XL", want: 1.20},
		{bundle: "24GB_A", want: 3.60},
		{bundle: "3XL", want: 3.60},
		{bundle: "4XL", want: 0},
		{bundle: "unknown", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.bundle, func(t *testing.T) {
			if got := tencentStaticHourlyPriceCNY(tt.bundle); got != tt.want {
				t.Fatalf("price=%v want %v", got, tt.want)
			}
		})
	}
}

func TestTencentInquirePriceDecodesHAIDiscountUnitPrice(t *testing.T) {
	data := []byte(`{"Response":{"Price":{"InstancePrice":{"UnitPrice":2.4,"DiscountUnitPrice":1.2},"CloudDiskPrice":{"UnitPrice":0.4,"DiscountUnitPrice":0.2}},"RequestId":"req-1"}}`)
	var resp haiInquirePriceRunInstancesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	price := resp.Response.Price.InstancePrice.DiscountUnitPrice
	if price != 1.2 {
		t.Fatalf("discount unit price=%v want 1.2", price)
	}
	if fallback := resp.Response.Price.InstancePrice.UnitPrice; fallback != 2.4 {
		t.Fatalf("unit price=%v want 2.4", fallback)
	}
	if total := resp.Response.Price.hourlyUnitPriceCNY(); math.Abs(total-1.4) > 1e-9 {
		t.Fatalf("total hourly price=%v want 1.4", total)
	}
}

func TestTencentPriceIgnoresDiskOnlyResponse(t *testing.T) {
	price := haiPrice{CloudDiskPrice: haiItemPrice{DiscountUnitPrice: 0.2}}
	if got := price.hourlyUnitPriceCNY(); got != 0 {
		t.Fatalf("disk-only price=%v want 0", got)
	}
}

func TestValidateTencentHAISSHConfigAcceptsReadableKey(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(key, []byte("not-a-real-key-but-readable"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig()
	cfg.SSHUser = "runner"
	cfg.SSHKey = key
	if err := validateTencentHAISSHConfig(cfg); err != nil {
		t.Fatalf("validateTencentHAISSHConfig() error = %v", err)
	}
}

func TestValidateTencentHAISSHConfigRequiresReadableKey(t *testing.T) {
	cfg := baseConfig()
	cfg.SSHUser = "runner"
	cfg.SSHKey = filepath.Join(t.TempDir(), "missing")
	err := validateTencentHAISSHConfig(cfg)
	if err == nil {
		t.Fatal("validateTencentHAISSHConfig() succeeded with missing key")
	}
	var exitErr ExitError
	if !AsExitError(err, &exitErr) || exitErr.Code != 3 {
		t.Fatalf("error=%v want exit code 3", err)
	}
	if !strings.Contains(err.Error(), "CRABBOX_SSH_KEY") {
		t.Fatalf("error=%q should mention CRABBOX_SSH_KEY", err.Error())
	}
}

func TestValidateTencentHAISSHConfigRejectsPublicKeyPath(t *testing.T) {
	key := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(key, []byte("ssh-ed25519 AAAA test"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig()
	cfg.SSHUser = "runner"
	cfg.SSHKey = key
	err := validateTencentHAISSHConfig(cfg)
	if err == nil {
		t.Fatal("validateTencentHAISSHConfig() accepted public key path")
	}
	if !strings.Contains(err.Error(), "private key") {
		t.Fatalf("error=%q should mention private key", err.Error())
	}
}
