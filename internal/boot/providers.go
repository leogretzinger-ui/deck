package boot

import (
	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/provider"
)

// buildProviders constructs configured descriptors. Missing configuration or
// construction failures omit a provider; an unavailable executable is logged
// while its descriptor remains registered.
func buildProviders(cfg *config.Config, log zerolog.Logger) map[provider.Name]provider.Descriptor {
	out := make(map[provider.Name]provider.Descriptor, 16)
	add := func(name provider.Name, build func() (provider.Descriptor, error), execPath string) {
		if execPath == "" {
			log.Warn().Str("provider", string(name)).Msg("provider not configured; unavailable")
			return
		}
		d, err := build()
		if err != nil {
			log.Warn().Str("provider", string(name)).Err(err).Msg("provider not constructible; unavailable")
			return
		}
		if err := d.Availability(); err != nil {
			log.Warn().Str("provider", string(name)).Err(err).Msg("provider unavailable")
		}
		out[name] = d
	}
	add(provider.Claude, func() (provider.Descriptor, error) {
		return provider.NewClaude(cfg.Providers.Claude.Executable)
	}, cfg.Providers.Claude.Executable)
	add(provider.Cline, func() (provider.Descriptor, error) {
		return provider.NewCline(cfg.Providers.Cline.Executable)
	}, cfg.Providers.Cline.Executable)
	add(provider.CommandCode, func() (provider.Descriptor, error) {
		return provider.NewCommandCode(cfg.Providers.CommandCode.Executable, cfg.Providers.CommandCode.HookConfig, cfg.Providers.CommandCode.HookReceiver)
	}, cfg.Providers.CommandCode.Executable)
	add(provider.Codex, func() (provider.Descriptor, error) {
		return provider.NewCodex(cfg.Providers.Codex.Executable, cfg.Providers.Codex.HookConfig, cfg.Providers.Codex.HookReceiver)
	}, cfg.Providers.Codex.Executable)
	add(provider.Cursor, func() (provider.Descriptor, error) {
		return provider.NewCursor(cfg.Providers.Cursor.Executable)
	}, cfg.Providers.Cursor.Executable)
	add(provider.Devin, func() (provider.Descriptor, error) {
		return provider.NewDevin(cfg.Providers.Devin.Executable, cfg.Providers.Devin.HookConfig, cfg.Providers.Devin.HookReceiver)
	}, cfg.Providers.Devin.Executable)
	add(provider.Grok, func() (provider.Descriptor, error) {
		return provider.NewGrok(cfg.Providers.Grok.Executable)
	}, cfg.Providers.Grok.Executable)
	add(provider.Kimi, func() (provider.Descriptor, error) {
		return provider.NewKimi(cfg.Providers.Kimi.Executable, cfg.Providers.Kimi.HookConfig, cfg.Providers.Kimi.HookReceiver)
	}, cfg.Providers.Kimi.Executable)
	add(provider.Kilo, func() (provider.Descriptor, error) {
		return provider.NewKilo(cfg.Providers.Kilo.Executable, cfg.Providers.Kilo.HookConfig)
	}, cfg.Providers.Kilo.Executable)
	add(provider.Mimo, func() (provider.Descriptor, error) {
		return provider.NewMimo(cfg.Providers.Mimo.Executable)
	}, cfg.Providers.Mimo.Executable)
	add(provider.Opencode, func() (provider.Descriptor, error) {
		return provider.NewOpencode(cfg.Providers.Opencode.Executable, cfg.Providers.Opencode.HookConfig)
	}, cfg.Providers.Opencode.Executable)
	add(provider.Omp, func() (provider.Descriptor, error) {
		return provider.NewOmp(cfg.Providers.Omp.Executable, cfg.Providers.Omp.HookConfig)
	}, cfg.Providers.Omp.Executable)
	add(provider.Pi, func() (provider.Descriptor, error) {
		return provider.NewPi(cfg.Providers.Pi.Executable, cfg.Providers.Pi.HookConfig)
	}, cfg.Providers.Pi.Executable)
	add(provider.Qoder, func() (provider.Descriptor, error) {
		return provider.NewQoder(cfg.Providers.Qoder.Executable)
	}, cfg.Providers.Qoder.Executable)
	add(provider.Qwen, func() (provider.Descriptor, error) {
		return provider.NewQwen(cfg.Providers.Qwen.Executable)
	}, cfg.Providers.Qwen.Executable)
	add(provider.Shell, func() (provider.Descriptor, error) {
		return provider.NewShell(cfg.Providers.Shell.Executable)
	}, cfg.Providers.Shell.Executable)
	return out
}
