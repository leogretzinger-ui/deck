package config

import "testing"

func TestSwapPolicyEnvironmentAndValidation(t *testing.T) {
	validEnv(t)
	for _, policy := range []string{"", "personal", "paranoid", "typo"} {
		t.Run(policy, func(t *testing.T) {
			t.Setenv("NOISEDECK_SWAP_POLICY", policy)
			cfg, err := Load("")
			if policy == "typo" {
				if err == nil {
					t.Fatal("unknown swap policy accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Sealed.SwapPolicy != policy {
				t.Fatalf("policy lost during load: %q", cfg.Sealed.SwapPolicy)
			}
		})
	}
}
