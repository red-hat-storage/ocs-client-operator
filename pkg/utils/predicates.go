package utils

import (
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// Name Predicate return a predicate the filter events produced
// by resources that matches the given name
func NamePredicate(name string) predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == name
	})
}

func CrdCreateAndDeletePredicate(log *logr.Logger, crdName string, crdExists bool) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			if !crdExists {
				log.Info("CustomResourceDefinition was Created.", "CustomResourceDefinition", crdName)
			}
			return !crdExists
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			if crdExists {
				log.Info("CustomResourceDefinition was Deleted.", "CustomResourceDefinition", crdName)
			}
			return crdExists
		},
		UpdateFunc: func(_ event.UpdateEvent) bool {
			return false
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
	}
}
