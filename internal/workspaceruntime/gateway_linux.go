//go:build linux

package workspaceruntime

import (
	"context"
	"encoding/json"
	"strings"
)

const gatewayNetworkFormat = `{"ports":{{json .NetworkSettings.Ports}},"network":{{json .HostConfig.NetworkMode}}}`

// Inspect emits only fixed boolean markers, never the container's environment
// values: OIDC credentials live beside these nonsecret transport settings.
const gatewayTransportFormat = `{{range .Config.Env}}{{if eq . "NOISEDECK_SERVER_BIND="}}b{{end}}{{if eq . "NOISEDECK_SERVER_UNIX_SOCKET=/data/gateway/backend.sock"}}s{{end}}{{if eq . "NOISEDECK_PROJECT_PREVIEWS_BIND="}}p{{end}}{{if eq . "NOISEDECK_EXPERIMENTS_UNCONFINED_CAPACITY_ROUTING=false"}}c{{end}}{{if eq . "NOISEDECK_AUTH_MODE=oidc"}}a{{end}}{{end}}`

func (c *Controller) verifyGatewayIngress(ctx context.Context) error {
	ids := map[string]bool{c.cfg.AppContainer: true}
	for _, label := range []string{"com.docker.compose.project", "io.podman.compose.project"} {
		out, err := c.command(ctx, c.cfg.PodmanPath, []string{"ps", "--all", "--no-trunc", "--filter",
			"label=" + label + "=" + c.cfg.ComposeProject, "--format", "{{.ID}}"}, nil, true)
		if err != nil {
			return ErrUnavailable
		}
		for _, id := range strings.Fields(string(out)) {
			if !containerID.MatchString(id) {
				return ErrUnavailable
			}
			ids[id] = true
		}
	}
	for id := range ids {
		out, err := c.command(ctx, c.cfg.PodmanPath, []string{"inspect", "--format", gatewayNetworkFormat, id}, nil, true)
		var network struct {
			Ports   json.RawMessage `json:"ports"`
			Network string          `json:"network"`
		}
		if err != nil || json.Unmarshal(out, &network) != nil || network.Network == "" ||
			network.Network == "host" || strings.HasPrefix(network.Network, "ns:") || strings.HasPrefix(network.Network, "container:") {
			return ErrUnsafeStorage
		}
		var ports map[string][]json.RawMessage
		if len(network.Ports) == 0 || json.Unmarshal(network.Ports, &ports) != nil {
			return ErrUnsafeStorage
		}
		for _, published := range ports {
			if len(published) != 0 {
				return ErrUnsafeStorage
			}
		}
	}
	out, err := c.command(ctx, c.cfg.PodmanPath, []string{"inspect", "--format", gatewayTransportFormat, c.cfg.AppContainer}, nil, true)
	markers := strings.TrimSpace(string(out))
	if err != nil || len(markers) != 5 {
		return ErrUnsafeStorage
	}
	for _, marker := range "bspca" {
		if strings.Count(markers, string(marker)) != 1 {
			return ErrUnsafeStorage
		}
	}
	return nil
}
