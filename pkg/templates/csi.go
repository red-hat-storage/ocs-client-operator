package templates

import (
	"fmt"

	csiopv1a1 "github.com/ceph/ceph-csi-operator/api/v1alpha1"
	secv1 "github.com/openshift/api/security/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

const RBDDriverName = "openshift-storage.rbd.csi.ceph.com"
const CephFsDriverName = "openshift-storage.cephfs.csi.ceph.com"
const NfsDriverName = "openshift-storage.nfs.csi.ceph.com"

// security context constraints
const SCCName = "ceph-csi-op-scc"

// TODO: could pull directly from ceph-csi-operator when available
var securityContextConstraints = secv1.SecurityContextConstraints{
	// CSI daemonset pod needs to run as privileged
	AllowPrivilegedContainer: true,
	// CSI daemonset pod needs hostnetworking
	AllowHostNetwork: true,
	// This need to be set to true as we use HostPath
	AllowHostDirVolumePlugin: true,
	// Required for csi addons
	AllowHostPorts: true,
	// Needed as we are setting this in RBD plugin pod
	AllowHostPID: true,
	// Required for multus and encryption
	AllowHostIPC: true,
	// SYS_ADMIN is needed for rbd to execute rbd map command
	AllowedCapabilities: []corev1.Capability{"SYS_ADMIN"},
	// # Set to false as we write to RootFilesystem inside csi containers
	ReadOnlyRootFilesystem: false,
	RunAsUser: secv1.RunAsUserStrategyOptions{
		Type: secv1.RunAsUserStrategyRunAsAny,
	},
	SELinuxContext: secv1.SELinuxContextStrategyOptions{
		Type: secv1.SELinuxStrategyRunAsAny,
	},
	FSGroup: secv1.FSGroupStrategyOptions{
		Type: secv1.FSGroupStrategyRunAsAny,
	},
	SupplementalGroups: secv1.SupplementalGroupsStrategyOptions{
		Type: secv1.SupplementalGroupsStrategyRunAsAny,
	},
	Volumes: []secv1.FSType{
		secv1.FSTypeHostPath,
		secv1.FSTypeConfigMap,
		secv1.FSTypeEmptyDir,
		secv1.FSProjected,
	},
}

func SetSecurityContextConstraintsDesiredState(scc *secv1.SecurityContextConstraints, ns string) {
	// Make sure metadata is preserved
	metadata := scc.ObjectMeta
	securityContextConstraints.DeepCopyInto(scc)
	scc.ObjectMeta = metadata

	scc.Users = []string{
		fmt.Sprintf("system:serviceaccount:%s:ceph-csi-cephfs-ctrlplugin-sa", ns),
		fmt.Sprintf("system:serviceaccount:%s:ceph-csi-cephfs-nodeplugin-sa", ns),
		fmt.Sprintf("system:serviceaccount:%s:ceph-csi-nfs-ctrlplugin-sa", ns),
		fmt.Sprintf("system:serviceaccount:%s:ceph-csi-nfs-nodeplugin-sa", ns),
		fmt.Sprintf("system:serviceaccount:%s:ceph-csi-rbd-ctrlplugin-sa", ns),
		fmt.Sprintf("system:serviceaccount:%s:ceph-csi-rbd-nodeplugin-sa", ns),
	}
}

// Ceph CSI Operator Config
const CSIOperatorConfigName = "ceph-csi-operator-config"

var CSIOperatorConfigSpec = csiopv1a1.OperatorConfigSpec{
	DriverSpecDefaults: &csiopv1a1.DriverSpec{
		Log: &csiopv1a1.LogSpec{
			Verbosity: 5,
			Rotation: &csiopv1a1.LogRotationSpec{
				Periodicity: csiopv1a1.DailyPeriod,
				MaxLogSize:  resource.MustParse("500M"),
				MaxFiles:    7,
				LogHostPath: "/var/lib/cephcsi",
			},
		},
		AttachRequired:  ptr.To(true),
		DeployCsiAddons: ptr.To(true),
		FsGroupPolicy:   storagev1.FileFSGroupPolicy,
		ControllerPlugin: &csiopv1a1.ControllerPluginSpec{
			Privileged: ptr.To(true),
			Resources: csiopv1a1.ControllerPluginResourcesSpec{
				LogRotator: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("32Mi"),
					},
				},
			},
			PodCommonSpec: csiopv1a1.PodCommonSpec{
				PrioritylClassName: ptr.To("system-cluster-critical"),
				ImagePullPolicy:    corev1.PullIfNotPresent,
				Tolerations: []corev1.Toleration{
					{
						Effect:   corev1.TaintEffectNoSchedule,
						Key:      "node.ocs.openshift.io/storage",
						Operator: corev1.TolerationOpEqual,
						Value:    "true",
					},
				},
			},
			Replicas:    ptr.To(int32(2)),
			HostNetwork: ptr.To(true),
		},
		NodePlugin: &csiopv1a1.NodePluginSpec{
			EnableSeLinuxHostMount: ptr.To(true),
			Resources: csiopv1a1.NodePluginResourcesSpec{
				LogRotator: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("32Mi"),
					},
				},
			},
			KubeletDirPath: "/var/lib/kubelet",
			PodCommonSpec: csiopv1a1.PodCommonSpec{
				PrioritylClassName: ptr.To("system-node-critical"),
				ImagePullPolicy:    corev1.PullIfNotPresent,
				Tolerations: []corev1.Toleration{
					{
						Key:      "node-role.kubernetes.io/master",
						Operator: corev1.TolerationOpExists,
						Effect:   corev1.TaintEffectNoSchedule,
					},
					{
						Effect:   corev1.TaintEffectNoSchedule,
						Key:      "node.ocs.openshift.io/storage",
						Operator: corev1.TolerationOpEqual,
						Value:    "true",
					},
				},
			},
		},
	},
}
