package controller

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"
)

func marshalVAC(t *testing.T, vac *storagev1.VolumeAttributesClass) []byte {
	t.Helper()
	b, err := json.Marshal(vac)
	assert.NoError(t, err)
	return b
}

func vacTypeMeta() metav1.TypeMeta {
	return metav1.TypeMeta{APIVersion: "storage.k8s.io/v1", Kind: "VolumeAttributesClass"}
}

// TestReconcileResource_PreservesNamespacedName verifies that name and namespace
// are restored after zeroing even when the provider bytes omit them due to omitempty.
func TestReconcileResource_PreservesNamespacedName(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cm",
			Namespace: "test-ns",
		},
	}
	r := newFakeStorageClientReconcile(t, existing)

	// Marshal a ConfigMap with empty namespace; omitempty drops it from JSON.
	desired := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "test-cm"},
	}
	b, err := json.Marshal(desired)
	assert.NoError(t, err)

	err = r.reconcileResource(
		&corev1.ConfigMap{},
		b,
		types.NamespacedName{Name: "test-cm", Namespace: "test-ns"},
	)
	assert.NoError(t, err)

	result := &corev1.ConfigMap{}
	assert.NoError(t, r.Get(r.ctx, types.NamespacedName{Name: "test-cm", Namespace: "test-ns"}, result))
	assert.Equal(t, "test-cm", result.Name, "name must be restored from namespacedName after zero")
	assert.Equal(t, "test-ns", result.Namespace, "namespace must be restored from namespacedName after zero")
}

// TestReconcileResource_ClearsAbsentFields is the regression test for the
// read-affinity class of bugs: a field present on the live object but absent
// from the provider response must be cleared, not silently retained.
func TestReconcileResource_ClearsAbsentFields(t *testing.T) {
	existing := &storagev1.VolumeAttributesClass{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vac"},
		DriverName: templates.RBDDriverName,
		Parameters: map[string]string{"iopsPerGB": "10"},
	}
	r := newFakeStorageClientReconcile(t, existing)

	// Parameters omitempty: nil map is absent from JSON, mimicking a provider
	// that no longer sends this field (e.g. readAffinity disabled).
	desired := &storagev1.VolumeAttributesClass{
		TypeMeta:   vacTypeMeta(),
		ObjectMeta: metav1.ObjectMeta{Name: "test-vac"},
		DriverName: templates.RBDDriverName,
	}
	err := r.reconcileResource(
		&storagev1.VolumeAttributesClass{},
		marshalVAC(t, desired),
		types.NamespacedName{Name: "test-vac"},
	)
	assert.NoError(t, err)

	result := &storagev1.VolumeAttributesClass{}
	assert.NoError(t, r.Get(r.ctx, types.NamespacedName{Name: "test-vac"}, result))
	assert.Empty(t, result.Parameters, "field absent from provider response must be cleared on the live object")
}

// TestReconcileResource_PreservesMetadata verifies that labels, annotations,
// and finalizers set externally on the live object survive a reconcile even
// when the provider response carries none of them.
func TestReconcileResource_PreservesMetadata(t *testing.T) {
	existing := &storagev1.VolumeAttributesClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-vac",
			Labels:      map[string]string{"external": "label"},
			Annotations: map[string]string{"external": "annotation"},
			Finalizers:  []string{"some-controller/finalizer"},
		},
		DriverName: templates.RBDDriverName,
	}
	r := newFakeStorageClientReconcile(t, existing)

	desired := &storagev1.VolumeAttributesClass{
		TypeMeta:   vacTypeMeta(),
		ObjectMeta: metav1.ObjectMeta{Name: "test-vac"},
		DriverName: templates.RBDDriverName,
	}
	err := r.reconcileResource(
		&storagev1.VolumeAttributesClass{},
		marshalVAC(t, desired),
		types.NamespacedName{Name: "test-vac"},
	)
	assert.NoError(t, err)

	result := &storagev1.VolumeAttributesClass{}
	assert.NoError(t, r.Get(r.ctx, types.NamespacedName{Name: "test-vac"}, result))
	assert.Equal(t, "label", result.Labels["external"], "labels must be preserved across reconcile")
	assert.Equal(t, "annotation", result.Annotations["external"], "annotations must be preserved across reconcile")
	assert.Contains(t, result.Finalizers, "some-controller/finalizer", "finalizers must be preserved across reconcile")
}
