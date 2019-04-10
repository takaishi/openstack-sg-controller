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
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"k8s.io/client-go/kubernetes"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"strconv"
	"strings"

	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	"github.com/takaishi/openstack-sg-controller/pkg/openstack"

	openstackv1beta1 "github.com/takaishi/openstack-sg-controller/pkg/apis/openstack/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/api/errors"
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

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new SecurityGroup Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileSecurityGroup{Client: mgr.GetClient(), scheme: mgr.GetScheme()}
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

	// TODO(user): Modify this to be the types you create
	// Uncomment watch a Deployment created by SecurityGroup - change this for objects you create
	err = c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &openstackv1beta1.SecurityGroup{},
	})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileSecurityGroup{}

// ReconcileSecurityGroup reconciles a SecurityGroup object
type ReconcileSecurityGroup struct {
	client.Client
	scheme *runtime.Scheme
}

func (r *ReconcileSecurityGroup) deleteExternalDependency(instance *openstackv1beta1.SecurityGroup) error {
	log.Info("Debug: deleting the external dependencies")

	osClient, err := openstack.NewClient()
	if err != nil {
		return err
	}
	sg, err := osClient.GetSecurityGroupByName(instance.Spec.Name)
	if err != nil {
		return err
	}

	cfg, err := config.GetConfig()
	if err != nil {
		log.Info("Error", "Failed to get config", err.Error())
		return err
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Info("Error", "Failed to NewForConfig", err.Error())
		return err
	}

	labelSelector := []string{}
	if hasKey(instance.Spec.NodeSelector, "role") {
		labelSelector = append(labelSelector, fmt.Sprintf("node-role.kubernetes.io/%s", instance.Spec.NodeSelector["role"]))
	}
	listOpts := metav1.ListOptions{
		LabelSelector: strings.Join(labelSelector, ","),
	}
	nodes, err := clientset.CoreV1().Nodes().List(listOpts)
	if err != nil {
		log.Info("Error", "Failed to NodeLIst", err.Error())
		return err
	}
	for _, node := range nodes.Items {
		id := node.Status.NodeInfo.SystemUUID
		hasSg, err := osClient.ServerHasSG(strings.ToLower(id), instance.Spec.Name)
		if err != nil {
			log.Info("Error", "Failed to ServerHasSG", err.Error())
			return err
		}

		fmt.Printf("%v\n", hasSg)
		if hasSg {
			log.Info("Info", "Dettach SG from Server: ", strings.ToLower(id))
			osClient.DettachSG(strings.ToLower(id), instance.Spec.Name)
		}
	}

	err = osClient.DeleteSecurityGroup(sg.ID)
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
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=openstack.repl.info,resources=securitygroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openstack.repl.info,resources=securitygroups/status,verbs=get;update;patch
func (r *ReconcileSecurityGroup) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	// Fetch the SecurityGroup instance
	instance := &openstackv1beta1.SecurityGroup{}
	err := r.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Debug: instance not found")

			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err

	}
	finalizerName := "finalizer.securitygroups.openstack.repl.info"
	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		log.Info("Debug: deletion timestamp is zero")
		if !containsString(instance.ObjectMeta.Finalizers, finalizerName) {
			instance.ObjectMeta.Finalizers = append(instance.ObjectMeta.Finalizers, finalizerName)
			if err := r.Update(context.Background(), instance); err != nil {
				log.Info("Debug", "err", err.Error())
				return reconcile.Result{}, err
			}
		}
	} else {
		if containsString(instance.ObjectMeta.Finalizers, finalizerName) {
			if err := r.deleteExternalDependency(instance); err != nil {
				return reconcile.Result{}, err
			}

			instance.ObjectMeta.Finalizers = removeString(instance.ObjectMeta.Finalizers, finalizerName)
			if err := r.Update(context.Background(), instance); err != nil {
				return reconcile.Result{}, err
			}
		}
		return reconcile.Result{}, nil
	}

	log.Info("Debug", "spec", instance.Spec)

	osClient, err := openstack.NewClient()
	if err != nil {
		return reconcile.Result{}, err
	}

	tenant, err := osClient.GetTenantByName(os.Getenv("OS_TENANT_NAME"))
	if err != nil {
		return reconcile.Result{}, err
	}
	log.Info("Debug", "tenant.ID", tenant.ID)

	var sg groups.SecGroup

	// Check if the SecurityGroup already exists
	sg, err = osClient.GetSecurityGroupByName(instance.Spec.Name)
	if err != nil {
		log.Info("Creating SG", "name", instance.Spec.Name)
		sg, err := osClient.CreateSecurityGroup(instance.Spec.Name, "", tenant.ID)
		if err != nil {
			log.Info("Error", "msg", err.Error())
			return reconcile.Result{}, err
		}
		log.Info("Success creating SG", "name", instance.Spec.Name, "id", sg.ID)

		for _, rule := range instance.Spec.Rules {
			err = r.addRule(osClient, sg.ID, rule)
			if err != nil {
				log.Info("Error", "msg", err.Error())
				return reconcile.Result{}, err
			}
		}
	}
	sg, err = osClient.GetSecurityGroupByName(instance.Spec.Name)
	if err != nil {
		return reconcile.Result{}, err

	}

	// Resource側のルールがない場合、SGにルールを追加
	for _, rule := range instance.Spec.Rules {
		exists := false
		for _, existsRule := range sg.Rules {
			if rule.RemoteIpPrefix == existsRule.RemoteIPPrefix && rule.PortRangeMax == strconv.Itoa(existsRule.PortRangeMax) && rule.PortRangeMin == strconv.Itoa(existsRule.PortRangeMin) {
				exists = true
			}
		}

		if !exists {
			r.addRule(osClient, sg.ID, rule)
			if err != nil {
				log.Info("Error", "addRule", err.Error())
				return reconcile.Result{}, err
			}
		}
	}

	// SGのルールがResource側にない場合、ルールを削除
	for _, existRule := range sg.Rules {
		delete := true
		for _, rule := range instance.Spec.Rules {
			if existRule.RemoteIPPrefix == rule.RemoteIpPrefix && strconv.Itoa(existRule.PortRangeMax) == rule.PortRangeMax && strconv.Itoa(existRule.PortRangeMin) == rule.PortRangeMin {
				delete = false
			}
		}
		if delete {
			log.Info("Deleting SG Rule", "cidr", existRule.RemoteIPPrefix, "port", fmt.Sprintf("%d-%d", existRule.PortRangeMin, existRule.PortRangeMax))
			err = osClient.DeleteSecurityGroupRule(existRule.ID)
			if err != nil {
				return reconcile.Result{}, err
			}
			log.Info("Success to delete SG Rule", "cidr", existRule.RemoteIPPrefix, "port", fmt.Sprintf("%d-%d", existRule.PortRangeMin, existRule.PortRangeMax))
		}
	}

	cfg, err := config.GetConfig()
	if err != nil {
		log.Info("Error", "Failed to get config", err.Error())
		return reconcile.Result{}, err
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Info("Error", "Failed to NewForConfig", err.Error())
		return reconcile.Result{}, err
	}
	labelSelector := []string{}
	for k, v := range instance.Spec.NodeSelector {
		if k == "role" {
			labelSelector = append(labelSelector, fmt.Sprintf("node-role.kubernetes.io/%s", v))
		} else {
			labelSelector = append(labelSelector, fmt.Sprintf("%s=%s", k, v))
		}

	}
	listOpts := metav1.ListOptions{
		LabelSelector: strings.Join(labelSelector, ","),
	}
	log.Info("Info", "labelSelector", labelSelector)
	nodes, err := clientset.CoreV1().Nodes().List(listOpts)
	if err != nil {
		log.Info("Error", "Failed to NodeLIst", err.Error())
		return reconcile.Result{}, err
	}

	existsNodeIDs := []string{}
	for _, node := range nodes.Items {
		existsNodeIDs = append(existsNodeIDs, strings.ToLower(node.Status.NodeInfo.SystemUUID))
	}

	for _, id := range instance.Status.Nodes {
		if !containsString(existsNodeIDs, id) {
			log.Info("Info", "Dettach SG from Server: ", strings.ToLower(id))
			osClient.DettachSG(strings.ToLower(id), instance.Spec.Name)
			instance.Status.Nodes = deleteFromSlice(instance.Status.Nodes, id)
		}
	}

	for _, node := range nodes.Items {
		id := node.Status.NodeInfo.SystemUUID
		hasSg, err := osClient.ServerHasSG(strings.ToLower(id), instance.Spec.Name)
		if err != nil {
			log.Info("Error", "Failed to ServerHasSG", err.Error())
			return reconcile.Result{}, err
		}

		fmt.Printf("%v\n", hasSg)
		if !hasSg {
			log.Info("Info", "Attach SG to Server: ", strings.ToLower(id))
			osClient.AttachSG(strings.ToLower(id), instance.Spec.Name)
			instance.Status.Nodes = append(instance.Status.Nodes, strings.ToLower(id))
		}
	}

	if err := r.Update(context.Background(), instance); err != nil {
		log.Info("Debug", "err", err.Error())
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileSecurityGroup) addRule(osClient *openstack.OpenStackClient, id string, rule openstackv1beta1.SecurityGroupRule) error {
	max, err := strconv.Atoi(rule.PortRangeMax)
	if err != nil {
		return err
	}
	min, err := strconv.Atoi(rule.PortRangeMin)
	if err != nil {
		return err
	}
	createOpts := rules.CreateOpts{
		Direction:      "ingress",
		SecGroupID:     id,
		PortRangeMax:   max,
		PortRangeMin:   min,
		RemoteIPPrefix: rule.RemoteIpPrefix,
		EtherType:      "IPv4",
		Protocol:       "TCP",
	}
	log.Info("Creating SG Rule", "cidr", rule.RemoteIpPrefix, "port", fmt.Sprintf("%d-%d", max, min))
	err = osClient.AddSecurityGroupRule(createOpts)
	if err != nil {
		return err
	}
	log.Info("Success to create SG Rule", "cidr", rule.RemoteIpPrefix, "port", fmt.Sprintf("%d-%d", max, min))

	return nil
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

func deleteFromSlice(items []string, search string) []string {
	r := []string{}
	for _, item := range items {
		if item != search {
			r = append(r, item)
		}
	}

	return r
}
