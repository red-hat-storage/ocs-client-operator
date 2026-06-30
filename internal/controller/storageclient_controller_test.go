package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/templates"

	provider "github.com/red-hat-storage/ocs-operator/services/provider/api/v4"
	"github.com/stretchr/testify/assert"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

func newFakeStorageClientReconcile(t *testing.T, objs ...client.Object) *storageClientReconcile {
	t.Helper()

	scheme := runtime.NewScheme()
	err := kubescheme.AddToScheme(scheme)
	assert.NoError(t, err)
	err = v1alpha1.AddToScheme(scheme)
	assert.NoError(t, err)

	storageClient := v1alpha1.StorageClient{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-storageclient",
			UID:  "test-uid-12345",
		},
	}

	builder := fake.NewClientBuilder().WithScheme(scheme)
	if len(objs) > 0 {
		builder = builder.WithObjects(objs...)
	}
	fakeClient := builder.Build()

	return &storageClientReconcile{
		StorageClientReconciler: &StorageClientReconciler{
			Client: fakeClient,
			Scheme: scheme,
		},
		ctx:           context.Background(),
		log:           ctrllog.Log.WithName("storageclient_controller_test"),
		storageClient: storageClient,
	}
}

func newVolumeAttributesClassBytes(t *testing.T, name, driverName string, params map[string]string) []byte {
	t.Helper()
	vac := storagev1.VolumeAttributesClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "storage.k8s.io/v1",
			Kind:       "VolumeAttributesClass",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		DriverName: driverName,
		Parameters: params,
	}
	data, err := json.Marshal(vac)
	assert.NoError(t, err)
	return data
}

func TestReconcileResourcesByGK_VolumeAttributesClass_Create(t *testing.T) {
	r := newFakeStorageClientReconcile(t)

	vacBytes := newVolumeAttributesClassBytes(t, "test-vac", templates.RBDDriverName, map[string]string{
		"iopsPerGB": "10",
	})

	desiredObjects := map[string]kubeObjectWithOpRecords{
		"VolumeAttributesClass.storage.k8s.io": {
			{
				NamespacedName: types.NamespacedName{Name: "test-vac"},
				bytes:          vacBytes,
				operation:      provider.KubeClientOp_CREATE_OR_UPDATE,
			},
		},
	}

	var combinedErr error
	r.reconcileResourcesByGK(&storagev1.VolumeAttributesClass{}, desiredObjects, &combinedErr)
	assert.NoError(t, combinedErr)

	created := &storagev1.VolumeAttributesClass{}
	err := r.Get(r.ctx, types.NamespacedName{Name: "test-vac"}, created)
	assert.NoError(t, err)
	assert.Equal(t, templates.RBDDriverName, created.DriverName)
	assert.Equal(t, "10", created.Parameters["iopsPerGB"])
	assert.True(t, metav1.IsControlledBy(created, &r.storageClient))
}

func TestReconcileResourcesByGK_VolumeAttributesClass_Update(t *testing.T) {
	r := newFakeStorageClientReconcile(t)

	initialBytes := newVolumeAttributesClassBytes(t, "test-vac", templates.RBDDriverName, map[string]string{
		"iopsPerGB": "10",
	})
	desiredObjects := map[string]kubeObjectWithOpRecords{
		"VolumeAttributesClass.storage.k8s.io": {
			{
				NamespacedName: types.NamespacedName{Name: "test-vac"},
				bytes:          initialBytes,
				operation:      provider.KubeClientOp_CREATE_OR_UPDATE,
			},
		},
	}
	var combinedErr error
	r.reconcileResourcesByGK(&storagev1.VolumeAttributesClass{}, desiredObjects, &combinedErr)
	assert.NoError(t, combinedErr)

	updatedBytes := newVolumeAttributesClassBytes(t, "test-vac", templates.RBDDriverName, map[string]string{
		"iopsPerGB": "20",
	})
	desiredObjects["VolumeAttributesClass.storage.k8s.io"] = kubeObjectWithOpRecords{
		{
			NamespacedName: types.NamespacedName{Name: "test-vac"},
			bytes:          updatedBytes,
			operation:      provider.KubeClientOp_CREATE_OR_UPDATE,
		},
	}

	r.reconcileResourcesByGK(&storagev1.VolumeAttributesClass{}, desiredObjects, &combinedErr)
	assert.NoError(t, combinedErr)

	updated := &storagev1.VolumeAttributesClass{}
	err := r.Get(r.ctx, types.NamespacedName{Name: "test-vac"}, updated)
	assert.NoError(t, err)
	assert.Equal(t, "20", updated.Parameters["iopsPerGB"])
}

func TestReconcileResourcesByGK_VolumeAttributesClass_DeleteOrphan(t *testing.T) {
	storageClient := v1alpha1.StorageClient{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-storageclient",
			UID:  "test-uid-12345",
		},
	}

	orphanVAC := &storagev1.VolumeAttributesClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphan-vac",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         v1alpha1.GroupVersion.String(),
					Kind:               "StorageClient",
					Name:               storageClient.Name,
					UID:                storageClient.UID,
					Controller:         boolPtr(true),
					BlockOwnerDeletion: boolPtr(true),
				},
			},
		},
		DriverName: templates.RBDDriverName,
		Parameters: map[string]string{"iopsPerGB": "5"},
	}

	r := newFakeStorageClientReconcile(t, orphanVAC)

	desiredObjects := map[string]kubeObjectWithOpRecords{}

	var combinedErr error
	r.reconcileResourcesByGK(&storagev1.VolumeAttributesClass{}, desiredObjects, &combinedErr)
	assert.NoError(t, combinedErr)

	deleted := &storagev1.VolumeAttributesClass{}
	err := r.Get(r.ctx, types.NamespacedName{Name: "orphan-vac"}, deleted)
	assert.Error(t, err, "orphaned VolumeAttributesClass should be deleted")
}

func TestReconcileResourcesByGK_VolumeAttributesClass_PreserveUnowned(t *testing.T) {
	unownedVAC := &storagev1.VolumeAttributesClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "unowned-vac",
		},
		DriverName: templates.RBDDriverName,
		Parameters: map[string]string{"iopsPerGB": "5"},
	}

	r := newFakeStorageClientReconcile(t, unownedVAC)

	desiredObjects := map[string]kubeObjectWithOpRecords{}

	var combinedErr error
	r.reconcileResourcesByGK(&storagev1.VolumeAttributesClass{}, desiredObjects, &combinedErr)
	assert.NoError(t, combinedErr)

	preserved := &storagev1.VolumeAttributesClass{}
	err := r.Get(r.ctx, types.NamespacedName{Name: "unowned-vac"}, preserved)
	assert.NoError(t, err, "unowned VolumeAttributesClass should not be deleted")
}

func TestReconcileResourcesByGK_VolumeAttributesClass_MultipleObjects(t *testing.T) {
	r := newFakeStorageClientReconcile(t)

	vac1Bytes := newVolumeAttributesClassBytes(t, "vac-gold", templates.RBDDriverName, map[string]string{
		"iopsPerGB": "50",
	})
	vac2Bytes := newVolumeAttributesClassBytes(t, "vac-silver", templates.RBDDriverName, map[string]string{
		"iopsPerGB": "10",
	})

	desiredObjects := map[string]kubeObjectWithOpRecords{
		"VolumeAttributesClass.storage.k8s.io": {
			{
				NamespacedName: types.NamespacedName{Name: "vac-gold"},
				bytes:          vac1Bytes,
				operation:      provider.KubeClientOp_CREATE_OR_UPDATE,
			},
			{
				NamespacedName: types.NamespacedName{Name: "vac-silver"},
				bytes:          vac2Bytes,
				operation:      provider.KubeClientOp_CREATE_OR_UPDATE,
			},
		},
	}

	var combinedErr error
	r.reconcileResourcesByGK(&storagev1.VolumeAttributesClass{}, desiredObjects, &combinedErr)
	assert.NoError(t, combinedErr)

	gold := &storagev1.VolumeAttributesClass{}
	err := r.Get(r.ctx, types.NamespacedName{Name: "vac-gold"}, gold)
	assert.NoError(t, err)
	assert.Equal(t, templates.RBDDriverName, gold.DriverName)
	assert.Equal(t, "50", gold.Parameters["iopsPerGB"])

	silver := &storagev1.VolumeAttributesClass{}
	err = r.Get(r.ctx, types.NamespacedName{Name: "vac-silver"}, silver)
	assert.NoError(t, err)
	assert.Equal(t, templates.RBDDriverName, silver.DriverName)
	assert.Equal(t, "10", silver.Parameters["iopsPerGB"])
}

func TestReconcileResourcesByGK_VolumeAttributesClass_NoDesiredState(t *testing.T) {
	r := newFakeStorageClientReconcile(t)

	desiredObjects := map[string]kubeObjectWithOpRecords{}

	var combinedErr error
	r.reconcileResourcesByGK(&storagev1.VolumeAttributesClass{}, desiredObjects, &combinedErr)
	assert.NoError(t, combinedErr)
}

func TestKindsToReconcileContainsVolumeAttributesClass(t *testing.T) {
	found := false
	for _, kind := range kindsToReconcile {
		if _, ok := kind.(*storagev1.VolumeAttributesClass); ok {
			found = true
			break
		}
	}
	assert.True(t, found, "kindsToReconcile must contain VolumeAttributesClass")
}

func TestSetupVolumeAttributesClassWatch_SkipsWhenNotRegistered(t *testing.T) {
	r := newFakeStorageClientReconcile(t)

	err := r.setupVolumeAttributesClassWatch()
	assert.NoError(t, err)

	_, found := r.crdsBeingWatched.Load(VolumeAttributesClassResourceName)
	assert.False(t, found, "should not be registered in crdsBeingWatched")
}

func TestSetupVolumeAttributesClassWatch_SkipsWhenAlreadyWatching(t *testing.T) {
	r := newFakeStorageClientReconcile(t)
	r.crdsBeingWatched.Store(VolumeAttributesClassResourceName, true)

	err := r.setupVolumeAttributesClassWatch()
	assert.NoError(t, err)

	val, _ := r.crdsBeingWatched.Load(VolumeAttributesClassResourceName)
	assert.True(t, val.(bool), "should remain true (already watching)")
}

func TestSetupVolumeAttributesClassWatch_SkipsOnNoMatchError(t *testing.T) {
	scheme := runtime.NewScheme()
	err := kubescheme.AddToScheme(scheme)
	assert.NoError(t, err)
	err = v1alpha1.AddToScheme(scheme)
	assert.NoError(t, err)

	noMatchErr := &meta.NoResourceMatchError{
		PartialResource: schema.GroupVersionResource{
			Group:    "storage.k8s.io",
			Version:  "v1",
			Resource: "volumeattributesclasses",
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*storagev1.VolumeAttributesClassList); ok {
					return noMatchErr
				}
				return cl.List(ctx, list, opts...)
			},
		}).
		Build()

	r := &storageClientReconcile{
		StorageClientReconciler: &StorageClientReconciler{
			Client: fakeClient,
			Scheme: scheme,
		},
		ctx: context.Background(),
		log: ctrllog.Log.WithName("storageclient_controller_test"),
	}
	r.crdsBeingWatched.Store(VolumeAttributesClassResourceName, false)

	err = r.setupVolumeAttributesClassWatch()
	assert.NoError(t, err)

	val, _ := r.crdsBeingWatched.Load(VolumeAttributesClassResourceName)
	assert.False(t, val.(bool), "should remain false when API is not available")
}

func TestCrdsBeingWatched_RegisteredWhenVACUnavailable(t *testing.T) {
	r := &StorageClientReconciler{
		AvailableCrds: map[string]bool{},
	}

	if !r.AvailableCrds[VolumeAttributesClassResourceName] {
		r.crdsBeingWatched.Store(VolumeAttributesClassResourceName, false)
	}

	val, found := r.crdsBeingWatched.Load(VolumeAttributesClassResourceName)
	assert.True(t, found, "should be registered in crdsBeingWatched")
	assert.False(t, val.(bool), "should be false (not yet watching)")
}

func TestCrdsBeingWatched_NotRegisteredWhenVACAvailable(t *testing.T) {
	r := &StorageClientReconciler{
		AvailableCrds: map[string]bool{
			VolumeAttributesClassResourceName: true,
		},
	}

	if !r.AvailableCrds[VolumeAttributesClassResourceName] {
		r.crdsBeingWatched.Store(VolumeAttributesClassResourceName, false)
	}

	_, found := r.crdsBeingWatched.Load(VolumeAttributesClassResourceName)
	assert.False(t, found, "should not be in crdsBeingWatched when already available")
}

func boolPtr(b bool) *bool {
	return &b
}
