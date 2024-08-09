package utils

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

var DelegateCSI = func() bool {
	return strings.ToLower(os.Getenv(CSIReconcileEnvVar)) == "delegate"
}()

func ExtractMonitor(monitorData []byte) ([]string, error) {
	data := map[string]string{}
	monitorIPs := []string{}
	err := json.Unmarshal(monitorData, &data)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal data: %v", err)
	}
	// Ip will be in the format of "b=172.30.60.238:6789","c=172.30.162.124:6789","a=172.30.1.100:6789"
	monIPs := strings.Split(data["data"], ",")
	for _, monIP := range monIPs {
		ip := strings.Split(monIP, "=")
		if len(ip) != 2 {
			return nil, fmt.Errorf("invalid mon ips: %s", monIPs)
		}
		monitorIPs = append(monitorIPs, ip[1])
	}
	slices.Sort(monitorIPs)
	return monitorIPs, nil
}
