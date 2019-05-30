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
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud"
	v1 "k8s.io/api/core/v1"

	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	openstackv1beta1 "github.com/takaishi/openstack-sg-controller/pkg/apis/openstack/v1beta1"
	"github.com/takaishi/openstack-sg-controller/pkg/openstack"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	errors_ "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller")
var finalizerName = "finalizer.securitygroups.openstack.repl.info"

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new SecurityGroup Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	osClient, err := openstack.NewClient()
	if err != nil {
		return err
	}

	return add(mgr, newReconciler(mgr, osClient))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager, osClient openstack.OpenStackClientInterface) reconcile.Reconciler {
	return &ReconcileSecurityGroup{Client: mgr.GetClient(), scheme: mgr.GetScheme(), osClient: osClient}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("securitygroup-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to SecurityGroup
	err = c.Watch(&source.Kind{Type: &openstackv1beta1.SecurityGroup{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileSecurityGroup{}

// ReconcileSecurityGroup reconciles a SecurityGroup object
type ReconcileSecurityGroup struct {
	client.Client
	scheme   *runtime.Scheme
	osClient openstack.OpenStackClientInterface
}

func (r *ReconcileSecurityGroup) deleteExternalDependency(instance *openstackv1beta1.SecurityGroup) error {
	log.Info("Info", "deleting the external dependencies", instance.Status.Name)

	sg, err := r.osClient.GetSecurityGroup(instance.Status.ID)
	if err != nil {
		_, notfound := err.(gophercloud.ErrDefault404)
		if notfound {
			log.Info("Info", "already delete security group", instance.Status.Name)
			return nil
		}
		return err
	}

	labelSelector := []string{}
	if hasKey(instance.Spec.NodeSelector, "role") {
		labelSelector = append(labelSelector, fmt.Sprintf("node-role.kubernetes.io/%s", instance.Spec.NodeSelector["role"]))
	}
	var nodes v1.NodeList
	ls, err := convertLabelSelectorToLabelsSelector(strings.Join(labelSelector, ","))
	if err != nil {
		return err
	}

	listOpts := client.ListOptions{
		LabelSelector: ls,
	}
	err = r.List(context.Background(), &listOpts, &nodes)
	if err != nil {
		log.Info("Error", "Failed to NodeList", err.Error())
		return err
	}

	for _, node := range nodes.Items {
		id := node.Status.NodeInfo.SystemUUID
		hasSg, err := r.osClient.ServerHasSG(strings.ToLower(id), instance.Status.Name)
		if err != nil {
			log.Info("Error", "Failed to ServerHasSG", err.Error())
			return err
		}

		if hasSg {
			log.Info("Info", "Dettach SG from Server: ", strings.ToLower(id))
			r.osClient.DettachSG(strings.ToLower(id), instance.Status.Name)
		}
	}

	log.Info("Info", "Delete SG name", sg.Name, "id", sg.ID)
	err = r.osClient.DeleteSecurityGroup(sg.ID)
	if err != nil {
		return err
	}

	return nil
}

// Reconcile reads that state of the cluster for a SecurityGroup object and makes changes based on the state read
// and what is in the SecurityGroup.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.  The scaffolding writes
// a Deployment as an example
// Automatically generate RBAC rules to allow the Controller to read and write Deployments
// +kubebuilder:rbac:groups=,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=,resources=nodes/status,verbs=get
// +kubebuilder:rbac:groups=openstack.repl.info,resources=securitygroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openstack.repl.info,resources=securitygroups/status,verbs=get;update;patch
func (r *ReconcileSecurityGroup) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// Fetch the SecurityGroup instance
	instance := &openstackv1beta1.SecurityGroup{}
	err := r.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors_.IsNotFound(err) {
			log.Info("Debug: instance not found", "SecurityGroup", instance.Name)
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	log.Info("Info: Start reconcile", "sg", instance.Name)
	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		log.Info("Debug: deletion timestamp is zero")
		if err := r.setFinalizer(instance); err != nil {
			return reconcile.Result{}, err
		}
	} else {
		return r.runFinalizer(instance)
	}

	tenant, err := r.osClient.GetTenantByName(os.Getenv("OS_TENANT_NAME"))
	if err != nil {
		return reconcile.Result{}, err
	}

	var sg *groups.SecGroup

	// Check if the SecurityGroup already exists
	sg, err = r.osClient.GetSecurityGroup(instance.Status.ID)
	if err != nil {
		switch err.(type) {
		case gophercloud.ErrDefault404:
			log.Info("Creating SG", "name", instance.Spec.Name)
			name := instance.Spec.Name

			sg, err = r.osClient.CreateSecurityGroup(name, "", tenant.ID)
			if err != nil {
				log.Info("Error", "msg", err.Error())
			}
			instance.Status.ID = sg.ID
			instance.Status.Name = sg.Name
			log.Info("Success creating SG", "name", instance.Spec.Name, "id", sg.ID)
		default:
			log.Info("Debug: errorrrrrr")
			return reconcile.Result{}, err
		}
	}

	// Resource側のルールがない場合、SGにルールを追加
	for _, rule := range instance.Spec.Rules {
		exists := false
		for _, existsRule := range sg.Rules {
			if rule.RemoteIpPrefix == existsRule.RemoteIPPrefix &&
				rule.PortRangeMax == existsRule.PortRangeMax &&
				rule.PortRangeMin == existsRule.PortRangeMin {
				exists = true
			}
		}

		if !exists {
			r.addRule(sg.ID, rule)
			if err != nil {
				log.Info("Error", "addRule", err.Error())
				return reconcile.Result{}, err
			}
		}
	}

	var nodes v1.NodeList
	ls, err := convertLabelSelectorToLabelsSelector(labelSelector(instance))
	if err != nil {
		return reconcile.Result{}, err
	}

	listOpts := client.ListOptions{
		LabelSelector: ls,
	}

	err = r.List(context.Background(), &listOpts, &nodes)
	if err != nil {
		log.Info("Error", "Failed to NodeList", err.Error())
		return reconcile.Result{}, err
	}

	existsNodeIDs := []string{}
	for _, node := range nodes.Items {
		existsNodeIDs = append(existsNodeIDs, strings.ToLower(node.Status.NodeInfo.SystemUUID))
	}

	for _, id := range instance.Status.Nodes {
		if !containsString(existsNodeIDs, id) {
			log.Info("Info", "Dettach SG from Server", strings.ToLower(id))
			err := r.osClient.DettachSG(strings.ToLower(id), sg.Name)
			if err != nil {
				log.Info("Error", "Failed to Detach SG", err.Error())
				return reconcile.Result{}, err
			}

			instance.Status.Nodes = removeString(instance.Status.Nodes, id)
		}
	}

	// SGのルールがResource側にない場合、ルールを削除
	for _, existRule := range sg.Rules {
		delete := true
		for _, rule := range instance.Spec.Rules {
			if existRule.RemoteIPPrefix == rule.RemoteIpPrefix &&
				existRule.PortRangeMax == rule.PortRangeMax &&
				existRule.PortRangeMin == rule.PortRangeMin {
				delete = false
			}
		}
		if delete {
			log.Info("Deleting SG Rule", "cidr", existRule.RemoteIPPrefix, "port", fmt.Sprintf("%d-%d", existRule.PortRangeMin, existRule.PortRangeMax))
			err = r.osClient.DeleteSecurityGroupRule(existRule.ID)
			if err != nil {
				return reconcile.Result{}, err
			}
			log.Info("Success to delete SG Rule", "cidr", existRule.RemoteIPPrefix, "port", fmt.Sprintf("%d-%d", existRule.PortRangeMin, existRule.PortRangeMax))
		}
	}

	for _, node := range nodes.Items {
		id := node.Status.NodeInfo.SystemUUID
		hasSg, err := r.osClient.ServerHasSG(strings.ToLower(id), sg.Name)
		if err != nil {
			log.Info("Error", "Failed to ServerHasSG", err.Error())
			return reconcile.Result{}, err
		}

		if !hasSg {
			log.Info("Info", "Attach SG to Server", strings.ToLower(id))
			if err = r.osClient.AttachSG(strings.ToLower(id), sg.Name); err != nil {
				log.Info("Debug", "failed to attach sg", err.Error())
				return reconcile.Result{}, err
			}
			instance.Status.Nodes = append(instance.Status.Nodes, strings.ToLower(id))
		}
	}

	if err := r.Status().Update(context.Background(), instance); err != nil {
		log.Info("Debug", "failed to update sg", err.Error())
		return reconcile.Result{}, err
	}

	log.Info("Info: Success reconcile", "sg", instance.Name)
	return reconcile.Result{RequeueAfter: 60 * time.Second}, nil
}

func convertLabelSelectorToLabelsSelector(selector string) (labels.Selector, error) {
	s, err := metav1.ParseToLabelSelector(selector)
	if err != nil {
		return nil, err
	}
	return metav1.LabelSelectorAsSelector(s)
}
func (r *ReconcileSecurityGroup) addRule(id string, rule openstackv1beta1.SecurityGroupRule) error {
	createOpts := rules.CreateOpts{
		Direction:      rules.RuleDirection(rule.Direction),
		SecGroupID:     id,
		PortRangeMax:   rule.PortRangeMax,
		PortRangeMin:   rule.PortRangeMin,
		RemoteIPPrefix: rule.RemoteIpPrefix,
		EtherType:      rules.RuleEtherType(rule.EtherType),
		Protocol:       rules.RuleProtocol(rule.Protocol),
	}
	log.Info("Creating SG Rule", "cidr", rule.RemoteIpPrefix, "port", fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax))
	err := r.osClient.AddSecurityGroupRule(createOpts)
	if err != nil {
		return err
	}
	log.Info("Success to create SG Rule", "cidr", rule.RemoteIpPrefix, "port", fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax))

	return nil
}

func (r *ReconcileSecurityGroup) setFinalizer(sg *openstackv1beta1.SecurityGroup) error {
	if !containsString(sg.ObjectMeta.Finalizers, finalizerName) {
		sg.ObjectMeta.Finalizers = append(sg.ObjectMeta.Finalizers, finalizerName)

		if err := r.Update(context.Background(), sg); err != nil {
			log.Info("Debug", "failed to update sg", err.Error())
			return err
		}
	}

	return nil
}

func (r *ReconcileSecurityGroup) runFinalizer(sg *openstackv1beta1.SecurityGroup) (reconcile.Result, error) {
	if containsString(sg.ObjectMeta.Finalizers, finalizerName) {
		if err := r.deleteExternalDependency(sg); err != nil {
			return reconcile.Result{}, err
		}

		sg.ObjectMeta.Finalizers = removeString(sg.ObjectMeta.Finalizers, finalizerName)
		if err := r.Update(context.Background(), sg); err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

func labelSelector(instance *openstackv1beta1.SecurityGroup) string {
	labelSelector := []string{}
	for k, v := range instance.Spec.NodeSelector {
		if k == "role" {
			labelSelector = append(labelSelector, fmt.Sprintf("node-role.kubernetes.io/%s", v))
		} else {
			labelSelector = append(labelSelector, fmt.Sprintf("%s=%s", k, v))
		}

	}

	return strings.Join(labelSelector, ",")
}
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}

func hasKey(dict map[string]string, key string) bool {
	_, ok := dict[key]

	return ok
}
