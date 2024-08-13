package controller

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	testNamespace         = "test-ns"
	fake417ClusterVersion = "4.17.0"
)

var fake418ImageSet = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "418-config",
		Namespace: testNamespace,
		Labels: map[string]string{
			csiImagesConfigMapLabel: "4.18.1",
		},
	},
}

var fake417ImageSet = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "417-config",
		Namespace: testNamespace,
		Labels: map[string]string{
			csiImagesConfigMapLabel: "4.17.2",
		},
	},
}

var fake416ImageSet = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "416-config",
		Namespace: testNamespace,
		Labels: map[string]string{
			csiImagesConfigMapLabel: "4.16.3",
		},
	},
}

var fake415ImageSet = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "415-config",
		Namespace: testNamespace,
		Labels: map[string]string{
			csiImagesConfigMapLabel: "4.15",
		},
	},
}

func newFakeScheme(t *testing.T) *runtime.Scheme {
	scheme := runtime.NewScheme()

	err := kubescheme.AddToScheme(scheme)
	assert.Nil(t, err, "failed to add kube scheme")

	err = configv1.AddToScheme(scheme)
	assert.Nil(t, err, "failed to add OCP config scheme")

	return scheme
}

func newFakeConfigMapReconciler(t *testing.T) OperatorConfigMapReconciler {
	var reconciler OperatorConfigMapReconciler

	reconciler.Scheme = newFakeScheme(t)
	reconciler.log = ctrllog.Log.WithName("configmap_controller_test")
	reconciler.OperatorNamespace = testNamespace

	return reconciler
}

func newFakeClientBuilder(scheme *runtime.Scheme) *fake.ClientBuilder {
	return fake.NewClientBuilder().
		WithScheme(scheme)
}

func TestGetImageSet(t *testing.T) {
	r := newFakeConfigMapReconciler(t)

	r.Client = newFakeClientBuilder(r.Scheme).
		Build()
	_, err := r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.NotNil(t, err, "should fail when no imageset configmaps exist")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake418ImageSet).
		Build()
	_, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.NotNil(t, err, "should fail when imageset configmaps is ahead of platform")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake418ImageSet).
		WithRuntimeObjects(fake417ImageSet).
		Build()
	cm, err := r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when imageset at exact version exists")
	assert.Equal(t, fake417ImageSet.Name, cm, "should pick exact 417 imageset")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake418ImageSet).
		WithRuntimeObjects(fake417ImageSet).
		WithRuntimeObjects(fake416ImageSet).
		Build()
	cm, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when imageset at exact version exists")
	assert.Equal(t, fake417ImageSet.Name, cm, "should prefer 417 imageset")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake418ImageSet).
		WithRuntimeObjects(fake416ImageSet).
		Build()
	cm, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when compatible imagesets  exists")
	assert.Equal(t, fake416ImageSet.Name, cm, "should prefer 416 imageset which is closer to 417")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake418ImageSet).
		WithRuntimeObjects(fake416ImageSet).
		WithRuntimeObjects(fake415ImageSet).
		Build()
	cm, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when compatible imagesets  exists")
	assert.Equal(t, fake416ImageSet.Name, cm, "should prefer 416 imageset which is closer to 417")

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake417ImageSet).
		Build()
	cm, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when imageset at exact version exists")
	assert.Equal(t, fake417ImageSet.Name, cm)

	r.Client = newFakeClientBuilder(r.Scheme).
		WithRuntimeObjects(fake415ImageSet).
		Build()
	cm, err = r.getImageSetConfigMapName(fake417ClusterVersion)
	assert.Nil(t, err, "should not fail when compatible imagesets  exists")
	assert.Equal(t, fake415ImageSet.Name, cm, "should prefer 415 imageset as it is lesser than 417 and nothing closer exists")
}
