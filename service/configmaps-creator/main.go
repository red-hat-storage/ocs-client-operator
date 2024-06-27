package main

import (
	"context"
	"fmt"
	"os"

	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml/goyaml.v2"
)

const (
	csiImagesConfigPath = "/opt/config/csi-images.yaml"
)

type csiImagesOldFormat struct {
	Version         string `yaml:"version"`
	ContainerImages struct {
		ProvisionerImageURL     string `yaml:"provisionerImageURL"`
		AttacherImageURL        string `yaml:"attacherImageURL"`
		ResizerImageURL         string `yaml:"resizerImageURL"`
		SnapshotterImageURL     string `yaml:"snapshotterImageURL"`
		DriverRegistrarImageURL string `yaml:"driverRegistrarImageURL"`
		CephCSIImageURL         string `yaml:"cephCSIImageURL"`
		CSIADDONSImageURL       string `yaml:"csiaddonsImageURL"`
	} `yaml:"containerImages"`
}

func newClient() (client.Client, error) {
	scheme := runtime.NewScheme()
	if err := kubescheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add kubernetes scheme to runtime scheme: %v", err)
	}
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get rest config: %v", err)
	}
	cl, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create controller-runtime client: %v", err)
	}
	return cl, nil
}

func main() {
	ctx := context.Background()

	operatorNS := os.Getenv(utils.OperatorNamespaceEnvVar)
	if operatorNS == "" {
		klog.Exitf("Env %q is not set", utils.OperatorNamespaceEnvVar)
	}

	cl, err := newClient()
	if err != nil {
		klog.Exitf("Failed to get new kube api client: %v", err)
	}

	yamlFile, err := os.ReadFile(csiImagesConfigPath)
	if err != nil {
		klog.Exitf("Failed to read contents of file %q: %v", csiImagesConfigPath, err)
	}
	csiImagesOld := []csiImagesOldFormat{}
	if err := yaml.Unmarshal(yamlFile, &csiImagesOld); err != nil {
		klog.Exitf("Failed to unmarshal contents of file %q as yaml: %v", csiImagesConfigPath, err)
	}

	for idx := range csiImagesOld {
		cmOldFmt := &csiImagesOld[idx]
		cmNewFmt := &corev1.ConfigMap{}
		cmNewFmt.Name = fmt.Sprintf("csi-images-%s", cmOldFmt.Version)
		cmNewFmt.Namespace = operatorNS
		opResult, err := controllerutil.CreateOrUpdate(ctx, cl, cmNewFmt, func() error {
			// verison in config map name is for human identification and we
			// use labels for programmatic listing/validation
			utils.AddLabel(cmNewFmt, "images.version", cmOldFmt.Version)
			imagesOld := cmOldFmt.ContainerImages
			cmNewFmt.Data = map[string]string{
				"provisioner": imagesOld.ProvisionerImageURL,
				"attacher":    imagesOld.AttacherImageURL,
				"resizer":     imagesOld.ResizerImageURL,
				"snapshotter": imagesOld.SnapshotterImageURL,
				"registrar":   imagesOld.DriverRegistrarImageURL,
				"plugin":      imagesOld.CephCSIImageURL,
				"addons":      imagesOld.CSIADDONSImageURL,
			}
			return nil
		})

		if err != nil {
			klog.Exitf("Failed to create config map for csi images: %v", err)
		}

		klog.Info(fmt.Sprintf("Configmap %q is %q", client.ObjectKeyFromObject(cmNewFmt), opResult))
	}
}
