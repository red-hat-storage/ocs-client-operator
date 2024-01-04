package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // ginkgo initialization
	. "github.com/onsi/gomega"    //nolint:revive // gomega initialization
	opv1a1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	opv2 "github.com/operator-framework/api/pkg/operators/v2"
	"github.com/red-hat-storage/ocs-client-operator/api/v1alpha1"
	"github.com/red-hat-storage/ocs-client-operator/pkg/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Upgrade Controller", func() {

	const (
		timeout  = time.Second * 3
		interval = time.Millisecond * 250
	)

	ctx := context.Background()
	clientSubscription := &opv1a1.Subscription{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testSubscriptionName,
			Namespace: testOperatorNamespace,
		},
	}
	clientCondition := &opv2.OperatorCondition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testConditionName,
			Namespace: testOperatorNamespace,
		},
	}
	client1 := &v1alpha1.StorageClient{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testClient1Name,
			Namespace: testClient1Namespace,
		},
	}
	client2 := &v1alpha1.StorageClient{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testClient2Name,
			Namespace: testClient2Namespace,
		},
	}

	// helpers
	keyOf := client.ObjectKeyFromObject

	createClient := func(storageClient *v1alpha1.StorageClient) {
		sc := storageClient.DeepCopy()
		Expect(k8sClient.Create(ctx, sc)).Should(Succeed())
	}

	deleteClient := func(storageClient *v1alpha1.StorageClient) {
		sc := storageClient.DeepCopy()
		Expect(k8sClient.Delete(ctx, sc)).Should(Succeed())
	}

	setDesiredVersion := func(storageClient *v1alpha1.StorageClient, version string) {
		sc := storageClient.DeepCopy()
		Expect(k8sClient.Get(ctx, keyOf(sc), sc)).Should(Succeed())
		sc.Status.Operator.DesiredVersion = version
		Expect(k8sClient.Status().Update(ctx, sc)).Should(Succeed())
	}

	setSubscriptionChannel := func(version string) {
		subscription := clientSubscription.DeepCopy()
		Expect(k8sClient.Get(ctx, keyOf(subscription), subscription)).Should(Succeed())
		subscription.Spec.Channel = testGetChannelFromVersion(version)
		Expect(k8sClient.Update(ctx, subscription)).Should(Succeed())
	}

	statusShouldBe := func(status metav1.ConditionStatus) bool {
		condition := clientCondition.DeepCopy()
		Expect(k8sClient.Get(ctx, keyOf(condition), condition)).Should(Succeed())
		upgrade := utils.Find(condition.Spec.Conditions, func(c *metav1.Condition) bool {
			return c.Type == opv2.Upgradeable
		})
		return upgrade != nil && upgrade.Status == status
	}

	channelShouldBe := func(version string) bool {
		subscription := clientSubscription.DeepCopy()
		Expect(k8sClient.Get(ctx, keyOf(subscription), subscription)).Should(Succeed())
		return subscription.Spec.Channel == testGetChannelFromVersion(version)
	}

	Context("reconcile()", Ordered, func() {

		When("there are no storageclients", func() {
			It("should not change subscription and be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testCurrentVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionTrue)
				}, timeout, interval).Should(BeTrue())
			})
		})

		When("a storageclient desired verison and subscription is same", func() {
			BeforeEach(func() {
				createClient(client1)
				setSubscriptionChannel(testCurrentVersion)
				setDesiredVersion(client1, testCurrentVersion)
			})

			It("should not change subscription and be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testCurrentVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionTrue)
				}, timeout, interval).Should(BeTrue())
			})
		})

		When("a storageclient desired version is ahead of both subscription and platform", func() {
			BeforeEach(func() {
				setSubscriptionChannel(testCurrentVersion)
				setDesiredVersion(client1, testAheadVersion)
			})

			It("should not change subscription and be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testCurrentVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionTrue)
				}, timeout, interval).Should(BeTrue())
			})
		})

		When("subscription is ahead of both platform and desired version", func() {
			BeforeEach(func() {
				setSubscriptionChannel(testAheadVersion)
				setDesiredVersion(client1, testCurrentVersion)
			})

			It("should not change subscription and not be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testAheadVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionFalse)
				}, timeout, interval).Should(BeTrue())
			})
		})

		// auto upgrade
		When("a storageclient desired version is ahead of only subscription but not platform", func() {
			BeforeEach(func() {
				setSubscriptionChannel(testLaggingVersion)
				setDesiredVersion(client1, testCurrentVersion)
			})

			It("should change subscription and be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testCurrentVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionTrue)
				}, timeout, interval).Should(BeTrue())
			})
		})

		// stop upgrade
		When("subscription is ahead of only desired version but not platform", func() {
			BeforeEach(func() {
				setSubscriptionChannel(testCurrentVersion)
				setDesiredVersion(client1, testLaggingVersion)
			})

			It("should not change subscription and not be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testCurrentVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionFalse)
				}, timeout, interval).Should(BeTrue())
			})
		})

		When("platform is ahead but subscription and desired version are same", func() {
			BeforeEach(func() {
				setSubscriptionChannel(testLaggingVersion)
				setDesiredVersion(client1, testLaggingVersion)
			})

			It("should not change subscription and be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testLaggingVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionTrue)
				}, timeout, interval).Should(BeTrue())
			})
		})

		When("one client desired version is ahead of subscription", func() {
			BeforeEach(func() {
				setSubscriptionChannel(testLaggingVersion)
				createClient(client2)
				setDesiredVersion(client2, testLaggingVersion)
				setDesiredVersion(client1, testCurrentVersion)
			})

			It("should not change subscription and be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testLaggingVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionTrue)
				}, timeout, interval).Should(BeTrue())

			})
		})

		When("all clients desired version is ahead of subscription", func() {
			BeforeEach(func() {
				setSubscriptionChannel(testLaggingVersion)
				setDesiredVersion(client1, testCurrentVersion)
				setDesiredVersion(client2, testCurrentVersion)
			})

			It("should change subscription and be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testCurrentVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionTrue)
				}, timeout, interval).Should(BeTrue())
			})
		})

		When("one client desired version is malformed", func() {
			BeforeEach(func() {
				// edge case, this'll fail if subscription if updated before desired version
				setDesiredVersion(client1, "n/a")
				setSubscriptionChannel(testLaggingVersion)
				setDesiredVersion(client2, testCurrentVersion)
			})

			It("should not change subscription and not be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testLaggingVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionFalse)
				}, timeout, interval).Should(BeTrue())
			})
		})

		When("one client desired version is empty", func() {
			BeforeEach(func() {
				// edge case, this'll fail if subscription if updated before desired version
				setDesiredVersion(client1, "n/a")
				setSubscriptionChannel(testLaggingVersion)
				setDesiredVersion(client2, testCurrentVersion)
			})

			It("should not change subscription and not be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testLaggingVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionFalse)
				}, timeout, interval).Should(BeTrue())
			})
		})

		When("faulty client is deleted", func() {
			BeforeEach(func() {
				setSubscriptionChannel(testLaggingVersion)
				deleteClient(client1)
				setDesiredVersion(client2, testCurrentVersion)
			})

			It("should change subscription and be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testCurrentVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionTrue)
				}, timeout, interval).Should(BeTrue())
			})
		})

		When("all clients are deleted", func() {
			BeforeEach(func() {
				deleteClient(client2)
				setSubscriptionChannel(testLaggingVersion)
			})

			It("should not change subscription and be upgradeable", func() {
				Eventually(func() bool {
					return channelShouldBe(testLaggingVersion)
				}, timeout, interval).Should(BeTrue())
				Eventually(func() bool {
					return statusShouldBe(metav1.ConditionTrue)
				}, timeout, interval).Should(BeTrue())
			})

		})

	})

})
