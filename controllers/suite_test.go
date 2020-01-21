/*
Copyright 2020 Ryo TAKAISHI.

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
	"fmt"
	"github.com/golang/mock/gomock"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	"github.com/takaishi/openstack-sg-controller/internal"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	openstackv1beta1 "github.com/takaishi/openstack-sg-controller/api/v1beta1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var k8sManager ctrl.Manager
var testEnv *envtest.Environment
var mockCtrl *gomock.Controller

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{envtest.NewlineReporter{}})
}

var _ = BeforeSuite(func(done Done) {
	fmt.Println("[DEBUG] BeforeSuite")
	logf.SetLogger(zap.LoggerTo(GinkgoWriter, true))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")},
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(cfg).ToNot(BeNil())

	err = scheme.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	err = openstackv1beta1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(k8sClient).ToNot(BeNil())

	close(done)
}, 60)

var _ = AfterSuite(func() {
	By("[DEBUG] AfterSuite")
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).ToNot(HaveOccurred())
})

func newOpenStackClientMock(controller *gomock.Controller) internal.OpenStackClientInterface {
	tenant := projects.Project{ID: "test-tenant-id", Name: "test-tenant"}
	sg := groups.SecGroup{ID: "test-sg-id", Name: "test-sg-abcde"}
	osClient := internal.NewMockOpenStackClientInterface(controller)
	// During create SecurityGroup
	firstGetTenantByName := osClient.EXPECT().GetTenantByName("test-tenant").Return(tenant, nil)
	firstGetSecurityGroup := osClient.EXPECT().GetSecurityGroup("").Return(&sg, gophercloud.ErrDefault404{}).After(firstGetTenantByName)
	CreateSecurityGroup := osClient.EXPECT().CreateSecurityGroup("test-sg", "", "test-tenant-id").Return(&sg, nil).After(firstGetSecurityGroup)
	createOpts := rules.CreateOpts{
		Direction:      "ingress",
		EtherType:      "IPv4",
		Protocol:       "tcp",
		PortRangeMax:   8888,
		PortRangeMin:   8888,
		SecGroupID:     "test-sg-id",
		RemoteIPPrefix: "127.0.0.1",
	}
	sg2 := groups.SecGroup{
		ID:   "test-sg-id",
		Name: "test-sg-abcde",
		Rules: []rules.SecGroupRule{
			{RemoteIPPrefix: "127.0.0.1",
				PortRangeMin: 8888,
				PortRangeMax: 8888,
			},
		},
	}
	firstAddSecurityGroupRule := osClient.EXPECT().AddSecurityGroupRule(createOpts).Return(nil).AnyTimes().After(CreateSecurityGroup)
	secondGetTenantByName := osClient.EXPECT().GetTenantByName("test-tenant").Return(tenant, nil).After(firstAddSecurityGroupRule)
	osClient.EXPECT().GetSecurityGroup("test-sg-id").Return(&sg2, nil).After(secondGetTenantByName)

	// During delete SecurityGroup
	osClient.EXPECT().GetSecurityGroup("test-sg-id").Return(&sg2, nil)
	osClient.EXPECT().DeleteSecurityGroup("test-sg-id").Return(nil)

	return osClient
}
