package validatesubscription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	admv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	contentTypeApplicationJSON = "application/json"
	contentTypeTextPlain       = "text/plain"
	errFailureToCaller         = "Failed in processing admission review: %q"
)

func HandleMessage(w http.ResponseWriter, r *http.Request, cl client.Client) {
	switch r.Method {
	case "POST":
		handlePost(w, r, cl)
	default:
		// kubernetes server doesn't make calls other than "POST", this is only a safegaurd
		handleUnsupportedMethod(w, r)
	}

}

func handlePost(w http.ResponseWriter, r *http.Request, cl client.Client) {
	klog.Info("POST method on /validate-subscription endpoint is invoked")

	admRequest, err := extractAdmissionRequest(r)
	if err != nil {
		klog.Errorf("failed to extract admission request: %v", err)
		http.Error(w, fmt.Sprintf(errFailureToCaller, err), http.StatusBadRequest)
		return
	}

	admResponse, err := generateAdmissionResponse(r.Context(), cl, admRequest)
	if err != nil {
		klog.Errorf("failed to generate admission response: %v", err)
		http.Error(w, fmt.Sprintf(errFailureToCaller, err), http.StatusInternalServerError)
		return
	}

	writeAdmissionReviewResponse(w, admResponse)
}

// extractAdmissionRequest parses the request body, marshal's the body into admissionreview and returns admissionrequest from it
func extractAdmissionRequest(r *http.Request) (*admv1.AdmissionRequest, error) {
	contentType := r.Header.Get("Content-Type")
	if contentType != contentTypeApplicationJSON {
		return nil, fmt.Errorf("Content-Type: %q should be %q", contentType, contentTypeApplicationJSON)
	}

	buf := new(bytes.Buffer)
	if bytesRead, err := buf.ReadFrom(r.Body); err != nil {
		return nil, err
	} else if bytesRead == 0 {
		return nil, fmt.Errorf("empty request body")
	}
	body := buf.Bytes()

	var admReview admv1.AdmissionReview
	if err := json.Unmarshal(body, &admReview); err != nil {
		return nil, err
	}

	if admReview.Request == nil {
		return nil, fmt.Errorf("empty admission request")
	}

	klog.Info("Parsed and extracted admission request successfully")
	return admReview.Request, nil
}

// generateAdmissionResponse takes in the admissionrequest, generates admissionresponse by validating the subscription channel in the request
// conforms to all the existing storageclients desired operator subscription channel or not
func generateAdmissionResponse(ctx context.Context, cl client.Client, admRequest *admv1.AdmissionRequest) (*admv1.AdmissionResponse, error) {
	subscription, err := extractSubscription(admRequest)
	if err != nil {
		return nil, err
	}

	storageClients := &v1alpha1.StorageClientList{}
	if err = cl.List(ctx, storageClients); err != nil {
		return nil, err
	}

	rejectionMsg := "subscription channel %q not allowed as it'll violate storageclient %q requirements"
	for idx := range storageClients.Items {
		storageClient := &storageClients.Items[idx]
		if storageClient.Status.DesiredOperatorSubscriptionChannel != subscription.Spec.Channel {
			return generateReviewResponse(admRequest.UID, false, http.StatusForbidden,
				fmt.Sprintf(rejectionMsg, subscription.Spec.Channel, client.ObjectKeyFromObject(storageClient))), nil
		}
	}

	klog.Info("Subscription channel in the request matches all storageclients desired operator subscription channel")
	return generateReviewResponse(admRequest.UID, true, http.StatusAccepted, "valid channel"), nil
}

func extractSubscription(admRequest *admv1.AdmissionRequest) (*opv1a1.Subscription, error) {
	if admRequest.Kind.Kind != "Subscription" {
		return nil, fmt.Errorf("only subscriptions admission reviews are supported, but received: %q", admRequest.Kind.Kind)
	}

	if admRequest.Operation != admv1.Update {
		return nil, fmt.Errorf("only validation of subscriptions update is supported, but received: %q", admRequest.Operation)
	}

	// interested only in new object, in admission request "object == new object" and "oldObject == old object"
	sub := &opv1a1.Subscription{}
	if err := json.Unmarshal(admRequest.Object.Raw, sub); err != nil {
		return nil, err
	}

	return sub, nil
}

func generateReviewResponse(uid types.UID, allowed bool, httpCode int32, reason string) *admv1.AdmissionResponse {
	return &admv1.AdmissionResponse{
		UID:     uid,
		Allowed: allowed,
		Result: &metav1.Status{
			Code:    httpCode,
			Message: reason,
		},
	}
}

// writeAdmissionReviewResponse writes the supplied admission response to http responsewriter
func writeAdmissionReviewResponse(w http.ResponseWriter, admResponse *admv1.AdmissionResponse) {
	admReview := &admv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{
			Kind:       "AdmissionReview",
			APIVersion: "admission.k8s.io/v1",
		},
		Response: admResponse,
	}
	admReviewBytes, err := json.Marshal(admReview)
	if err != nil {
		klog.Errorf("failed to marshal admission review response: %v", err)
		http.Error(w, fmt.Sprintf(errFailureToCaller, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentTypeApplicationJSON)
	if _, err := w.Write(admReviewBytes); err != nil {
		klog.Errorf("failed to write data to response writer, %v", err)
	}
}

func handleUnsupportedMethod(w http.ResponseWriter, r *http.Request) {
	klog.Info("Only POST method should be used to send data to this endpoint /validate-subscription")
	w.WriteHeader(http.StatusMethodNotAllowed)
	w.Header().Set("Content-Type", contentTypeTextPlain)
	w.Header().Set("Allow", "POST")

	if _, err := w.Write([]byte(fmt.Sprintf("Unsupported method : %s", r.Method))); err != nil {
		klog.Errorf("failed to write data to response writer: %v", err)
	}
}
