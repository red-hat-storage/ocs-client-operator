package utils

import (
	"log"
	"os"
	"strconv"

	corev1 "k8s.io/api/core/v1"
)

func GetTolerationForCSIPods() corev1.Toleration {

	runOnMaster := true
	var err error
	rom := os.Getenv(runCSIDaemonsetOnMaster)
	if rom != "" {
		runOnMaster, err = strconv.ParseBool(rom)
		if err != nil {
			log.Fatal(err)
		}
	}

	if runOnMaster {
		toleration := corev1.Toleration{
			Key:      "node-role.kubernetes.io/master",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		}
		return toleration
	}
	return corev1.Toleration{}
}
