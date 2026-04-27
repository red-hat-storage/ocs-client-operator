package utils

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestLabelExistsPredicate(t *testing.T) {
	const labelKey = "ocs.openshift.io/storageclient.name"

	tests := []struct {
		name     string
		obj      *corev1.ConfigMap
		expected bool // true if any event path should allow; we assert Create matches filter(obj)
	}{
		{
			name: "label present non-empty value",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{labelKey: "client-a"},
				},
			},
			expected: true,
		},
		{
			name: "label present empty value",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{labelKey: ""},
				},
			},
			expected: true,
		},
		{
			name: "label key absent",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"other": "x"},
				},
			},
			expected: false,
		},
		{
			name: "labels map nil",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expected: false,
		},
		{
			name: "labels map empty",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+"_Create", func(t *testing.T) {
			p := LabelExistsPredicate(labelKey)
			got := p.Create(event.CreateEvent{Object: tt.obj})
			if got != tt.expected {
				t.Fatalf("Create = %v, expect %v", got, tt.expected)
			}
		})
		t.Run(tt.name+"_Delete", func(t *testing.T) {
			p := LabelExistsPredicate(labelKey)
			got := p.Delete(event.DeleteEvent{Object: tt.obj})
			if got != tt.expected {
				t.Fatalf("Delete = %v, expect %v", got, tt.expected)
			}
		})
		t.Run(tt.name+"_Generic", func(t *testing.T) {
			p := LabelExistsPredicate(labelKey)
			got := p.Generic(event.GenericEvent{Object: tt.obj})
			if got != tt.expected {
				t.Fatalf("Generic = %v, expect %v", got, tt.expected)
			}
		})
		t.Run(tt.name+"_Update", func(t *testing.T) {
			p := LabelExistsPredicate(labelKey)
			// NewPredicateFuncs only inspects ObjectNew on Update.
			old := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "ns"},
			}
			got := p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: tt.obj})
			if got != tt.expected {
				t.Fatalf("Update = %v, expect %v", got, tt.expected)
			}
		})
	}

	t.Run("Create nil object", func(t *testing.T) {
		p := LabelExistsPredicate(labelKey)
		if p.Create(event.CreateEvent{Object: nil}) {
			t.Fatal("Create with nil object should be false")
		}
	})

	t.Run("Update uses ObjectNew only not ObjectOld", func(t *testing.T) {
		p := LabelExistsPredicate(labelKey)
		cases := []struct {
			name     string
			old      *corev1.ConfigMap
			newObj   *corev1.ConfigMap
			expected bool
		}{
			{
				name: "old has label new does not",
				old: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{labelKey: "client-a"},
					},
				},
				newObj: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"other": "x"},
					},
				},
				expected: false,
			},
			{
				name: "old lacks label new has label",
				old: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"other": "x"},
					},
				},
				newObj: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{labelKey: "client-a"},
					},
				},
				expected: true,
			},
			{
				name: "both have label value may change",
				old: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{labelKey: "client-a"},
					},
				},
				newObj: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{labelKey: "client-b"},
					},
				},
				expected: true,
			},
			{
				name: "neither has label key",
				old: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"other": "1"},
					},
				},
				newObj: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"other": "2"},
					},
				},
				expected: false,
			},
			{
				name: "old nil labels new has label",
				old: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "ns"},
				},
				newObj: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{labelKey: "client-a"},
					},
				},
				expected: true,
			},
			{
				name: "old has label new nil labels",
				old: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{labelKey: "client-a"},
					},
				},
				newObj: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "ns"},
				},
				expected: false,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := p.Update(event.UpdateEvent{ObjectOld: tc.old, ObjectNew: tc.newObj})
				if got != tc.expected {
					t.Fatalf("Update = %v, expect %v", got, tc.expected)
				}
			})
		}
	})
}
