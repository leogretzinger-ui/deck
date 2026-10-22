package doctor

import (
	"net"
	"strconv"
	"strings"
)

func lsofListenerPIDs(output, bind string) []string {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return nil
	}
	var pids []string
	var pid string
	for _, line := range strings.Split(output, "\n") {
		if value, ok := strings.CutPrefix(line, "p"); ok {
			pid = ""
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				pid = value
			}
		}
		if address, ok := strings.CutPrefix(line, "n"); ok && pid != "" {
			gotHost, gotPort, err := net.SplitHostPort(address)
			ip := net.ParseIP(gotHost)
			if err == nil && gotPort == port && (gotHost == "*" || gotHost == host || (ip != nil && ip.Equal(net.ParseIP(host)))) {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}
