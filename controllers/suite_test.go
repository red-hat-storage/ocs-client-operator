/*
Copyright 2022 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/blang/semver/v4"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // ginkgo initialization
	. "github.com/onsi/gomega"    //nolint:revive // gomega initialization
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	opv2 "github.com/operator-framework/api/pkg/operators/v2"
	"github.com/operator-framework/operator-lib/conditions"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	testEnv   *envtest.Environment
	k8sClient client.Client
	ctx       context.Context
	cancel    context.CancelFunc

	// reconcilers that are under test
	testUpgradeReconciler *UpgradeReconciler
)

const (
	testOperatorNamespace = "client-op-ns"
	testConditionName     = "client.condition"
	testPlatformVersion   = "4.14.4"

	testSubscriptionName = "client-sub"

	testAheadVersion   = "4.15"
	testCurrentVersion = "4.14"
	testLaggingVersion = "4.13"

	testClient1Name      = "client-1"
	testClient1Namespace = "client-1-ns"
	testClient2Name      = "client-2"
	testClient2Namespace = "client-2-ns"
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "controller Suite")
}

func testGetChannelFromVersion(version string) string {
	version += dummyPatchVersion
	ver, _ := semver.Make(version)
	return getChannelFromVersion(ver)
}

var _ = BeforeSuite(func() {
	done := make(chan struct{})

	go func() {
		defer GinkgoRecover()
		// gitignored path
		dirPath := filepath.Join("..", "testbin")
		err := os.MkdirAll(dirPath, os.ModePerm)
		Expect(err).ToNot(HaveOccurred())

		logFile, err := os.Create(filepath.Join(dirPath, "reconciler.log"))
		Expect(err).ToNot(HaveOccurred())

		// write logs from reconciler to a log file
		GinkgoWriter.TeeTo(logFile)
		logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

		ctx, cancel = context.WithCancel(context.Background())

		By("bootstrapping test environment")
		testEnv = &envtest.Environment{
			CRDDirectoryPaths: []string{
				filepath.Join("..", "config", "crd", "bases"),
				filepath.Join("..", "shim", "crds"),
			},
			CRDInstallOptions: envtest.CRDInstallOptions{
				CleanUpAfterUse: true,
			},
		}

		cfg, err := testEnv.Start()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())

		err = v1alpha1.AddToScheme(scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())

		err = opv1a1.AddToScheme(scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())

		err = opv2.AddToScheme(scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())

		//+kubebuilder:scaffold:scheme

		// client to be used by test code
		k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient).NotTo(BeNil())

		// create manager
		k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme: scheme.Scheme,
		})
		Expect(err).ToNot(HaveOccurred())

		// setup reconciler
		testUpgradeReconciler = &UpgradeReconciler{
			Client: k8sManager.GetClient(),
			Scheme: k8sManager.GetScheme(),
			OperatorCondition: &mockCondition{
				namespacedName: types.NamespacedName{
					Name:      testConditionName,
					Namespace: testOperatorNamespace,
				},
				condType: opv2.ConditionType(opv2.Upgradeable),
				client:   k8sManager.GetClient(),
			},
			OperatorNamespace: testOperatorNamespace,
			// platform version isn't updated in any of the tests
			PlatformVersion: testPlatformVersion,
		}
		err = (testUpgradeReconciler).SetupWithManager(k8sManager)
		Expect(err).ToNot(HaveOccurred())

		// start the manager
		go func() {
			defer GinkgoRecover()
			err := k8sManager.Start(ctx)
			Expect(err).ToNot(HaveOccurred(), "failed to run manager")
		}()

		// create auxiliary resources
		operatorNS := &corev1.Namespace{}
		operatorNS.Name = testOperatorNamespace
		Expect(k8sClient.Create(ctx, operatorNS)).Should(Succeed())

		client1NS := &corev1.Namespace{}
		client1NS.Name = testClient1Namespace
		Expect(k8sClient.Create(ctx, client1NS)).Should(Succeed())

		client2NS := &corev1.Namespace{}
		client2NS.Name = testClient2Namespace
		Expect(k8sClient.Create(ctx, client2NS)).Should(Succeed())

		subscription := &opv1a1.Subscription{}
		subscription.Name = testSubscriptionName
		subscription.Namespace = testOperatorNamespace
		subscription.Spec = &opv1a1.SubscriptionSpec{
			Channel: testGetChannelFromVersion(testCurrentVersion),
			Package: ocsClientOperatorSubscriptionPackageName,
		}
		Expect(k8sClient.Create(ctx, subscription)).Should(Succeed())

		condition := &opv2.OperatorCondition{}
		condition.Name = testConditionName
		condition.Namespace = testOperatorNamespace
		Expect(k8sClient.Create(ctx, condition)).Should(Succeed())

		close(done)
	}()

	// wait for setup to complete
	Eventually(done, 60).Should(BeClosed())
})

var _ = AfterSuite(func() {
	cancel()
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
	GinkgoWriter.ClearTeeWriters()
})

// copy of https://github.com/operator-framework/operator-lib/blob/main/conditions/conditions.go
type mockCondition struct {
	namespacedName types.NamespacedName
	condType       opv2.ConditionType
	client         client.Client
}

var _ conditions.Condition = &mockCondition{}

func (c *mockCondition) Get(ctx context.Context) (*metav1.Condition, error) {
	operatorCond := &opv2.OperatorCondition{}
	err := c.client.Get(ctx, c.namespacedName, operatorCond)
	if err != nil {
		return nil, err
	}
	con := meta.FindStatusCondition(operatorCond.Spec.Conditions, string(c.condType))

	if con == nil {
		return nil, fmt.Errorf("conditionType %v not found", c.condType)
	}
	return con, nil
}

func (c *mockCondition) Set(ctx context.Context, status metav1.ConditionStatus, option ...conditions.Option) error {
	operatorCond := &opv2.OperatorCondition{}
	err := c.client.Get(ctx, c.namespacedName, operatorCond)
	if err != nil {
		return err
	}

	newCond := &metav1.Condition{
		Type:   string(c.condType),
		Status: status,
	}

	for _, opt := range option {
		opt(newCond)
	}
	meta.SetStatusCondition(&operatorCond.Spec.Conditions, *newCond)
	return c.client.Update(ctx, operatorCond)
}
