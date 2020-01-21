package controllers

import (
	"context"
	"fmt"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	openstackv1beta1 "github.com/takaishi/openstack-sg-controller-v2/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"log"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sync"
	"time"
)

const timeout = time.Second * 5
const interval = time.Second * 1

func SetupTestReconcile(inner reconcile.Reconciler) (reconcile.Reconciler, chan reconcile.Request) {
	requests := make(chan reconcile.Request)
	fn := reconcile.Func(func(req reconcile.Request) (reconcile.Result, error) {
		result, err := inner.Reconcile(req)
		requests <- req
		return result, err
	})
	return fn, requests
}

// StartTestManager adds recFn
func StartTestManager(mgr manager.Manager, g *GomegaWithT) (chan struct{}, *sync.WaitGroup) {
	stop := make(chan struct{})
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		g.Expect(mgr.Start(stop)).NotTo(HaveOccurred())
	}()
	return stop, wg
}

var _ = Describe("SecurityGroup Controller", func() {
	Context("aaa", func() {
		var stopMgr chan struct{}
		var mgrStopped *sync.WaitGroup
		var requests chan reconcile.Request
		var reconciler reconcile.Reconciler

		BeforeEach(func() {
			fmt.Println("[DEBUG] BeforeEach")

			if err := os.Setenv("OS_TENANT_NAME", "test-tenant"); err != nil {
				log.Fatal(err)
				return
			}
			k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
				Scheme: scheme.Scheme,
			})
			Expect(err).NotTo(HaveOccurred())

			mockCtrl = gomock.NewController(GinkgoT())

			reconciler, requests = SetupTestReconcile(&SecurityGroupReconciler{
				Client:   k8sManager.GetClient(),
				Log:      ctrl.Log.WithName("controllers").WithName("SecretScope"),
				osClient: newOpenStackClientMock(mockCtrl),
			})

			_, err = ctrl.NewControllerManagedBy(k8sManager).
				For(&openstackv1beta1.SecurityGroup{}).
				Build(reconciler)
			Expect(err).ToNot(HaveOccurred())

			g := NewGomegaWithT(GinkgoT())
			stopMgr, mgrStopped = StartTestManager(k8sManager, g)
		})

		AfterEach(func() {
			mockCtrl.Finish()
			close(stopMgr)
			mgrStopped.Wait()
		})

		It("bbb", func() {
			toCreate := &openstackv1beta1.SecurityGroup{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "default",
				},
				Spec: openstackv1beta1.SecurityGroupSpec{
					Name:   "test-sg",
					Tenant: "test-tenant-id",
					NodeSelector: map[string]string{
						"role": "node",
					},
					Rules: []openstackv1beta1.SecurityGroupRule{
						{
							Direction:      "ingress",
							EtherType:      "IPv4",
							Protocol:       "tcp",
							PortRangeMax:   8888,
							PortRangeMin:   8888,
							RemoteIpPrefix: "127.0.0.1",
						},
					},
				},
				Status: openstackv1beta1.SecurityGroupStatus{
					ID: "test-sg-id",
				},
			}

			// Create the SecurityGroup object and expect the Reconcile and Deployment to be created
			By("Creating the SecurityGroup successfully")
			Expect(k8sClient.Create(context.Background(), toCreate)).Should(Succeed())
			var expectedRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "foo", Namespace: "default"}}
			// Wait first Reconcile to set finalizer.
			Eventually(requests, timeout, interval).Should(Receive(Equal(expectedRequest)))
			// Wait second Reconcile to create external resources.
			Eventually(requests, timeout, interval).Should(Receive(Equal(expectedRequest)))
			// Wait third Reconcile occurred by updating status in second Reconcile.
			Eventually(requests, timeout, interval).Should(Receive(Equal(expectedRequest)))

			fetched := &openstackv1beta1.SecurityGroup{}

			key := types.NamespacedName{Name: "foo", Namespace: "default"}
			Eventually(func() bool {
				k8sClient.Get(context.Background(), key, fetched)
				return fetched.Name == "foo"
			}, timeout, interval).Should(BeTrue())

			By("Deleting the SecurityGroup")
			Eventually(func() error {
				f := &openstackv1beta1.SecurityGroup{}
				k8sClient.Get(context.Background(), key, f)
				return k8sClient.Delete(context.Background(), f)
			}, timeout, interval).Should(Succeed())
			Eventually(requests, timeout).Should(Receive(Equal(expectedRequest)))

			Eventually(func() error {
				f := &openstackv1beta1.SecurityGroup{}
				return k8sClient.Get(context.Background(), key, f)
			}, timeout, interval).Should(MatchError("securitygroups.openstack.repl.info \"foo\" not found"))
		})
	})
})