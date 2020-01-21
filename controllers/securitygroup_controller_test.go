package controllers

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	openstackv1beta1 "github.com/takaishi/openstack-sg-controller/api/v1beta1"
	"github.com/takaishi/openstack-sg-controller/internal"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"log"
	"os"
	"reflect"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sync"
	"testing"
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

func TestSecurityGroupReconciler_attachSG(t *testing.T) {
	type fields struct {
		Client   client.Client
		Log      logr.Logger
		Scheme   *runtime.Scheme
		osClient internal.OpenStackClientInterface
	}
	type args struct {
		instance *openstackv1beta1.SecurityGroup
		sg       *groups.SecGroup
		nodes    []v1.Node
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		before  func(controller *gomock.Controller) internal.OpenStackClientInterface
		wantErr bool
	}{
		{
			name: "server does'nt has security group",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().ServerHasSG("aaa", "test-sg").Return(false, nil)
				osClient.EXPECT().AttachSG("aaa", "test-sg").Return(nil)
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{},
				sg:       &groups.SecGroup{Name: "test-sg"},
				nodes:    []v1.Node{{Status: v1.NodeStatus{NodeInfo: v1.NodeSystemInfo{SystemUUID: "aaa"}}}},
			},
			wantErr: false,
		},
		{
			name: "server has already security group",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().ServerHasSG("aaa", "test-sg").Return(true, nil)
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{},
				sg:       &groups.SecGroup{Name: "test-sg"},
				nodes:    []v1.Node{{Status: v1.NodeStatus{NodeInfo: v1.NodeSystemInfo{SystemUUID: "aaa"}}}},
			},
			wantErr: false,
		},
		{
			name: "ServerhasSG return error",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().ServerHasSG("aaa", "test-sg").Return(false, fmt.Errorf("error"))
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{},
				sg:       &groups.SecGroup{Name: "test-sg"},
				nodes:    []v1.Node{{Status: v1.NodeStatus{NodeInfo: v1.NodeSystemInfo{SystemUUID: "aaa"}}}},
			},
			wantErr: true,
		},
		{
			name: "AttachSG return error",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().ServerHasSG("aaa", "test-sg").Return(false, nil)
				osClient.EXPECT().AttachSG("aaa", "test-sg").Return(fmt.Errorf("error"))
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{},
				sg:       &groups.SecGroup{Name: "test-sg"},
				nodes:    []v1.Node{{Status: v1.NodeStatus{NodeInfo: v1.NodeSystemInfo{SystemUUID: "aaa"}}}},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCtrl = gomock.NewController(t)
			defer mockCtrl.Finish()
			osClient := tt.before(mockCtrl)
			r := &SecurityGroupReconciler{
				Log:      ctrl.Log.WithName("controllers").WithName("SecurityGroup"),
				osClient: osClient,
			}
			if err := r.attachSG(tt.args.instance, tt.args.sg, tt.args.nodes); (err != nil) != tt.wantErr {
				t.Errorf("attachSG() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSecurityGroupReconciler_detachSG(t *testing.T) {
	type fields struct {
		Client   client.Client
		Log      logr.Logger
		Scheme   *runtime.Scheme
		osClient internal.OpenStackClientInterface
	}
	type args struct {
		instance *openstackv1beta1.SecurityGroup
		sg       *groups.SecGroup
		nodes    []v1.Node
	}
	tests := []struct {
		name       string
		fields     fields
		args       args
		before     func(controller *gomock.Controller) internal.OpenStackClientInterface
		wantErr    bool
		wantErrMsg string
	}{
		{
			name: "server has security group",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().DetachSG("aaa", "test-sg").Return(nil)
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Status: openstackv1beta1.SecurityGroupStatus{
						Nodes: []string{"aaa"},
					},
				},
				sg:    &groups.SecGroup{Name: "test-sg"},
				nodes: []v1.Node{{Status: v1.NodeStatus{NodeInfo: v1.NodeSystemInfo{SystemUUID: "bbb"}}}},
			},
			wantErr: false,
		},
		{
			name: "DetachSG return error",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().DetachSG("aaa", "test-sg").Return(fmt.Errorf("DetachSG failed."))
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Status: openstackv1beta1.SecurityGroupStatus{
						Nodes: []string{"aaa"},
					},
				},
				sg:    &groups.SecGroup{Name: "test-sg"},
				nodes: []v1.Node{{Status: v1.NodeStatus{NodeInfo: v1.NodeSystemInfo{SystemUUID: "bbb"}}}},
			},
			wantErr:    true,
			wantErrMsg: "DetachSG failed.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCtrl = gomock.NewController(t)
			defer mockCtrl.Finish()
			r := &SecurityGroupReconciler{
				Client:   tt.fields.Client,
				Log:      ctrl.Log.WithName("controllers").WithName("SecurityGroup"),
				Scheme:   tt.fields.Scheme,
				osClient: tt.before(mockCtrl),
			}

			err := r.detachSG(tt.args.instance, tt.args.sg, tt.args.nodes)
			if (err != nil) != tt.wantErr {
				t.Errorf("detachSG() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr && err.Error() != tt.wantErrMsg {
				t.Errorf("detachSG() error = %v, wantErr %v", err, tt.wantErrMsg)
			}
		})
	}
}

func TestSecurityGroupReconciler_ensureSG(t *testing.T) {
	type fields struct {
		Client   client.Client
		Log      logr.Logger
		Scheme   *runtime.Scheme
		osClient internal.OpenStackClientInterface
	}
	type args struct {
		instance *openstackv1beta1.SecurityGroup
		tenant   projects.Project
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		before  func(controller *gomock.Controller) internal.OpenStackClientInterface
		want    *groups.SecGroup
		wantErr bool
	}{
		{
			name: "security group does not exist.",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				sg := &groups.SecGroup{
					ID:   "test-secgroup-id",
					Name: "test-secgroup",
				}
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().GetSecurityGroup("test-sg-id").Return(nil, gophercloud.ErrDefault404{})
				osClient.EXPECT().CreateSecurityGroup("test-sg", "", "test-tenant-id").Return(sg, nil)
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Spec: openstackv1beta1.SecurityGroupSpec{
						Name:   "test-sg",
						Tenant: "aaa",
					},
					Status: openstackv1beta1.SecurityGroupStatus{
						ID: "test-sg-id",
					},
				},
				tenant: projects.Project{
					ID: "test-tenant-id",
				},
			},
			wantErr: false,
			want: &groups.SecGroup{
				ID:   "test-secgroup-id",
				Name: "test-secgroup",
			},
		},
		{
			name: "security group already exists.",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				sg := &groups.SecGroup{
					ID:   "test-secgroup-id",
					Name: "test-secgroup",
				}
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().GetSecurityGroup("test-sg-id").Return(sg, nil)
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Spec: openstackv1beta1.SecurityGroupSpec{
						Name:   "test-sg",
						Tenant: "aaa",
					},
					Status: openstackv1beta1.SecurityGroupStatus{
						ID: "test-sg-id",
					},
				},
				tenant: projects.Project{
					ID: "test-tenant-id",
				},
			},
			wantErr: false,
			want: &groups.SecGroup{
				ID:   "test-secgroup-id",
				Name: "test-secgroup",
			},
		},
		{
			name: "CreateSecurityGroup return error.",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().GetSecurityGroup("test-sg-id").Return(nil, gophercloud.ErrDefault404{})

				osClient.EXPECT().CreateSecurityGroup("test-sg", "", "test-tenant-id").Return(nil, fmt.Errorf("error"))
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Spec: openstackv1beta1.SecurityGroupSpec{
						Name:   "test-sg",
						Tenant: "aaa",
					},
					Status: openstackv1beta1.SecurityGroupStatus{
						ID: "test-sg-id",
					},
				},
				tenant: projects.Project{
					ID: "test-tenant-id",
				},
			},
			wantErr: true,
			want:    nil,
		},
		{
			name: "GetSecurityGroup return error (not 404).",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().GetSecurityGroup("test-sg-id").Return(nil, fmt.Errorf("error"))
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Spec: openstackv1beta1.SecurityGroupSpec{
						Name:   "test-sg",
						Tenant: "aaa",
					},
					Status: openstackv1beta1.SecurityGroupStatus{
						ID: "test-sg-id",
					},
				},
				tenant: projects.Project{
					ID: "test-tenant-id",
				},
			},
			wantErr: true,
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCtrl = gomock.NewController(t)
			defer mockCtrl.Finish()
			osClient := tt.before(mockCtrl)
			r := &SecurityGroupReconciler{
				Log:      ctrl.Log.WithName("controllers").WithName("SecurityGroup"),
				osClient: osClient,
			}
			got, err := r.ensureSG(tt.args.instance, tt.args.tenant)
			if (err != nil) != tt.wantErr {
				t.Errorf("ensureSG() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ensureSG() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSecurityGroupReconciler_addRule(t *testing.T) {
	type fields struct {
		Client   client.Client
		Log      logr.Logger
		Scheme   *runtime.Scheme
		osClient internal.OpenStackClientInterface
	}
	type args struct {
		instance *openstackv1beta1.SecurityGroup
		sg       *groups.SecGroup
	}
	tests := []struct {
		name    string
		fields  fields
		before  func(controller *gomock.Controller) internal.OpenStackClientInterface
		args    args
		wantErr bool
	}{
		{
			name: "security group does not have rule.",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				opts := rules.CreateOpts{
					Direction:      "ingress",
					SecGroupID:     "test-secgroup-id",
					PortRangeMax:   8888,
					PortRangeMin:   8888,
					RemoteIPPrefix: "192.0.2.0/24",
					EtherType:      rules.RuleEtherType("IPv4"),
					Protocol:       rules.RuleProtocol("tcp"),
				}
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().AddSecurityGroupRule(opts).Return(nil)
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Spec: openstackv1beta1.SecurityGroupSpec{
						Name:   "test-sg",
						Tenant: "aaa",
						Rules: []openstackv1beta1.SecurityGroupRule{
							{
								Direction:      "ingress",
								PortRangeMax:   8888,
								PortRangeMin:   8888,
								RemoteIpPrefix: "192.0.2.0/24",
								EtherType:      "IPv4",
								Protocol:       "tcp",
							},
						},
					},
					Status: openstackv1beta1.SecurityGroupStatus{
						ID: "test-sg-id",
					},
				},
				sg: &groups.SecGroup{
					ID:    "test-secgroup-id",
					Rules: []rules.SecGroupRule{},
				},
			},
			wantErr: false,
		},
		{
			name: "security group have rule already.",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Spec: openstackv1beta1.SecurityGroupSpec{
						Name:   "test-sg",
						Tenant: "aaa",
						Rules: []openstackv1beta1.SecurityGroupRule{
							{
								Direction:      "ingress",
								PortRangeMax:   8888,
								PortRangeMin:   8888,
								RemoteIpPrefix: "192.0.2.0/24",
								EtherType:      "IPv4",
								Protocol:       "tcp",
							},
						},
					},
					Status: openstackv1beta1.SecurityGroupStatus{
						ID: "test-sg-id",
					},
				},
				sg: &groups.SecGroup{
					ID: "test-secgroup-id",
					Rules: []rules.SecGroupRule{
						{
							Direction:      "ingress",
							PortRangeMax:   8888,
							PortRangeMin:   8888,
							RemoteIPPrefix: "192.0.2.0/24",
							EtherType:      "IPv4",
							Protocol:       "tcp",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "AddSecurityGroupRule return error.",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				opts := rules.CreateOpts{
					Direction:      "ingress",
					SecGroupID:     "test-secgroup-id",
					PortRangeMax:   8888,
					PortRangeMin:   8888,
					RemoteIPPrefix: "192.0.2.0/24",
					EtherType:      rules.RuleEtherType("IPv4"),
					Protocol:       rules.RuleProtocol("tcp"),
				}
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().AddSecurityGroupRule(opts).Return(fmt.Errorf("error"))
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Spec: openstackv1beta1.SecurityGroupSpec{
						Name:   "test-sg",
						Tenant: "aaa",
						Rules: []openstackv1beta1.SecurityGroupRule{
							{
								Direction:      "ingress",
								PortRangeMax:   8888,
								PortRangeMin:   8888,
								RemoteIpPrefix: "192.0.2.0/24",
								EtherType:      "IPv4",
								Protocol:       "tcp",
							},
						},
					},
					Status: openstackv1beta1.SecurityGroupStatus{
						ID: "test-sg-id",
					},
				},
				sg: &groups.SecGroup{
					ID:    "test-secgroup-id",
					Rules: []rules.SecGroupRule{},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCtrl = gomock.NewController(t)
			defer mockCtrl.Finish()
			osClient := tt.before(mockCtrl)
			r := &SecurityGroupReconciler{
				Client:   tt.fields.Client,
				Log:      ctrl.Log.WithName("controllers").WithName("SecurityGroup"),
				Scheme:   tt.fields.Scheme,
				osClient: osClient,
			}
			if err := r.addRule(tt.args.instance, tt.args.sg); (err != nil) != tt.wantErr {
				t.Errorf("addRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSecurityGroupReconciler_deleteRule(t *testing.T) {
	type fields struct {
		Client   client.Client
		Log      logr.Logger
		Scheme   *runtime.Scheme
		osClient internal.OpenStackClientInterface
	}
	type args struct {
		instance *openstackv1beta1.SecurityGroup
		sg       *groups.SecGroup
	}
	tests := []struct {
		name    string
		fields  fields
		before  func(controller *gomock.Controller) internal.OpenStackClientInterface
		args    args
		wantErr bool
	}{
		{
			name: "SecurityGroup does not have rule",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().DeleteSecurityGroupRule("test-rule-id").Return(nil)
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Spec: openstackv1beta1.SecurityGroupSpec{
						Name:   "test-sg",
						Tenant: "aaa",
						Rules:  []openstackv1beta1.SecurityGroupRule{},
					},
					Status: openstackv1beta1.SecurityGroupStatus{
						ID: "test-sg-id",
					},
				},
				sg: &groups.SecGroup{
					ID: "test-secgroup-id",
					Rules: []rules.SecGroupRule{
						{
							ID:             "test-rule-id",
							Direction:      "ingress",
							PortRangeMax:   8888,
							PortRangeMin:   8888,
							RemoteIPPrefix: "192.0.2.0/24",
							EtherType:      "IPv4",
							Protocol:       "tcp",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "SecurityGroup does not have rule",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Spec: openstackv1beta1.SecurityGroupSpec{
						Name:   "test-sg",
						Tenant: "aaa",
						Rules: []openstackv1beta1.SecurityGroupRule{
							{
								Direction:      "ingress",
								PortRangeMax:   8888,
								PortRangeMin:   8888,
								RemoteIpPrefix: "192.0.2.0/24",
								EtherType:      "IPv4",
								Protocol:       "tcp",
							},
						},
					},
					Status: openstackv1beta1.SecurityGroupStatus{
						ID: "test-sg-id",
					},
				},
				sg: &groups.SecGroup{
					ID: "test-secgroup-id",
					Rules: []rules.SecGroupRule{
						{
							ID:             "test-rule-id",
							Direction:      "ingress",
							PortRangeMax:   8888,
							PortRangeMin:   8888,
							RemoteIPPrefix: "192.0.2.0/24",
							EtherType:      "IPv4",
							Protocol:       "tcp",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "DeleteSecurityGroupRule return error",
			before: func(controller *gomock.Controller) internal.OpenStackClientInterface {
				osClient := internal.NewMockOpenStackClientInterface(controller)
				osClient.EXPECT().DeleteSecurityGroupRule("test-rule-id").Return(fmt.Errorf("error"))
				return osClient
			},
			args: args{
				instance: &openstackv1beta1.SecurityGroup{
					Spec: openstackv1beta1.SecurityGroupSpec{
						Name:   "test-sg",
						Tenant: "aaa",
						Rules:  []openstackv1beta1.SecurityGroupRule{},
					},
					Status: openstackv1beta1.SecurityGroupStatus{
						ID: "test-sg-id",
					},
				},
				sg: &groups.SecGroup{
					ID: "test-secgroup-id",
					Rules: []rules.SecGroupRule{
						{
							ID:             "test-rule-id",
							Direction:      "ingress",
							PortRangeMax:   8888,
							PortRangeMin:   8888,
							RemoteIPPrefix: "192.0.2.0/24",
							EtherType:      "IPv4",
							Protocol:       "tcp",
						},
					},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCtrl = gomock.NewController(t)
			defer mockCtrl.Finish()
			osClient := tt.before(mockCtrl)
			r := &SecurityGroupReconciler{
				Client:   tt.fields.Client,
				Log:      ctrl.Log.WithName("controllers").WithName("SecurityGroup"),
				Scheme:   tt.fields.Scheme,
				osClient: osClient,
			}
			if err := r.deleteRule(tt.args.instance, tt.args.sg); (err != nil) != tt.wantErr {
				t.Errorf("deleteRule() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
