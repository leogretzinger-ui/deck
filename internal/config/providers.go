package config

// ProviderEntry pairs each provider's configuration with its native executable.
type ProviderEntry struct {
	Name   string
	Binary string
	Config *Provider
}

func (p *Providers) Entries() []ProviderEntry {
	return []ProviderEntry{
		{"claude", "claude", &p.Claude},
		{"cline", "cline", &p.Cline},
		{"command-code", "command-code", &p.CommandCode},
		{"codex", "codex", &p.Codex},
		{"cursor", "cursor-agent", &p.Cursor},
		{"devin", "devin", &p.Devin},
		{"grok", "grok", &p.Grok},
		{"kimi", "kimi", &p.Kimi},
		{"kilo", "kilo", &p.Kilo},
		{"mimo", "mimo", &p.Mimo},
		{"omp", "omp", &p.Omp},
		{"opencode", "opencode", &p.Opencode},
		{"pi", "pi", &p.Pi},
		{"qoder", "qoder", &p.Qoder},
		{"qwen", "qwen", &p.Qwen},
		{"shell", "bash", &p.Shell},
	}
}
