/*
Copyright 2019 TAKAISHI Ryo.

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

package securitygroup

import (
	"github.com/golang/mock/gomock"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	"github.com/takaishi/openstack-sg-controller/mock"
	"github.com/takaishi/openstack-sg-controller/pkg/openstack"
	"os"
	"testing"
	"time"

	"github.com/onsi/gomega"
	openstackv1beta1 "github.com/takaishi/openstack-sg-controller/pkg/apis/openstack/v1beta1"
	"golang.org/x/net/context"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var c client.Client

var expectedRequest = reconcile.Request{NamespacedName: types.NamespacedName{Name: "foo", Namespace: "default"}}
var depKey = types.NamespacedName{Name: "foo-deployment", Namespace: "default"}

const timeout = time.Second * 5

func newOpenStackClientMock(controller *gomock.Controller) openstack.OpenStackClientInterface {
	tenant := projects.Project{ID: "test-tenant-id", Name: "test-tenant"}
	sg := groups.SecGroup{ID: "test-sg-id", Name: "test-sg-abcde"}
	osClient := mock_openstack.NewMockOpenStackClientInterface(controller)
	osClient.EXPECT().GetTenantByName("test-tenant").Return(tenant, nil).Times(2)
	osClient.EXPECT().GetSecurityGroup("").Return(&sg, gophercloud.ErrDefault404{})
	osClient.EXPECT().GetSecurityGroup("test-sg-id").Return(&sg, nil)
	osClient.EXPECT().CreateSecurityGroup("test-sg-abcde", "", "test-tenant-id").Return(&sg, nil)
	createOpts := rules.CreateOpts{
		Direction:      "ingress",
		EtherType:      "IPv4",
		Protocol:       "tcp",
		PortRangeMax:   8888,
		PortRangeMin:   8888,
		SecGroupID:     "test-sg-id",
		RemoteIPPrefix: "127.0.0.1",
	}
	osClient.EXPECT().AddSecurityGroupRule(createOpts).Return(nil).AnyTimes()
	//osClient.EXPECT().DeleteSecurityGroupRule("rule-id").Return(nil)
	//osClient.EXPECT().AttachSG("server-id", "sg-name").Return(nil)
	osClient.EXPECT().RandomString().Return("abcde")

	return osClient
}

func TestReconcile(t *testing.T) {
	if err := os.Setenv("OS_TENANT_NAME", "test-tenant"); err != nil {
		t.Logf("failed to set env variable: %v", err)
		return
	}

	g := gomega.NewGomegaWithT(t)
	instance := &openstackv1beta1.SecurityGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
		},
		Spec: openstackv1beta1.SecurityGroupSpec{
			Name: "test-sg",
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

	// Setup the Manager and Controller.  Wrap the Controller Reconcile function so it writes each request to a
	// channel when it is finished.
	mgr, err := manager.New(cfg, manager.Options{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	c = mgr.GetClient()

	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	osClient := newOpenStackClientMock(mockCtrl)

	recFn, requests := SetupTestReconcile(newReconciler(mgr, osClient))
	g.Expect(add(mgr, recFn)).NotTo(gomega.HaveOccurred())

	stopMgr, mgrStopped := StartTestManager(mgr, g)

	defer func() {
		close(stopMgr)
		mgrStopped.Wait()
	}()

	// Create the SecurityGroup object and expect the Reconcile and Deployment to be created
	err = c.Create(context.TODO(), instance)
	// The instance object may not be a valid object because it might be missing some required fields.
	// Please modify the instance object by adding required fields and then remove the following if statement.
	if apierrors.IsInvalid(err) {
		t.Logf("failed to create object, got an invalid object error: %v", err)
		return
	}
	g.Expect(err).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), instance)
	g.Eventually(requests, timeout).Should(gomega.Receive(gomega.Equal(expectedRequest)))

	//deploy := &appsv1.Deployment{}
	//g.Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
	//	Should(gomega.Succeed())
	//
	//Delete the Deployment and expect Reconcile to be called for Deployment deletion
	//g.Expect(c.Delete(context.TODO(), deploy)).NotTo(gomega.HaveOccurred())
	//g.Eventually(requests, timeout).Should(gomega.Receive(gomega.Equal(expectedRequest)))
	//g.Eventually(func() error { return c.Get(context.TODO(), depKey, deploy) }, timeout).
	//	Should(gomega.Succeed())

	//Manually delete Deployment since GC isn't enabled in the test control plane
	//g.Eventually(func() error { return c.Delete(context.TODO(), deploy) }, timeout).
	//	Should(gomega.MatchError("deployments.apps \"foo-deployment\" not found"))

}
