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

	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	"github.com/takaishi/openstack-sg-controller/pkg/openstack"

	openstackv1beta1 "github.com/takaishi/openstack-sg-controller/pkg/apis/openstack/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

	return nil
}

var _ reconcile.Reconciler = &ReconcileSecurityGroup{}

// ReconcileSecurityGroup reconciles a SecurityGroup object
type ReconcileSecurityGroup struct {
	client.Client
	scheme *runtime.Scheme
}

func (r *ReconcileSecurityGroup) deleteExternalDependency(instance *openstackv1beta1.SecurityGroup) error {
	log.Info("Info", "deleting the external dependencies", instance.Spec.Name)

	osClient, err := openstack.NewClient()
	if err != nil {
		return err
	}
	sg, err := osClient.GetSecurityGroupByName(instance.Spec.Name)
	if err != nil {
		return err
	}

	clientset, err := kubeClient()
	if err != nil {
		log.Info("Error", "Failed to create kubeClient", err.Error())
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
		if errors_.IsNotFound(err) {
			log.Info("Debug: instance not found", "SecurityGroup", instance.Name)

			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err

	}

	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		log.Info("Debug: deletion timestamp is zero")
		if err := r.setFinalizer(instance); err != nil {
			return reconcile.Result{}, err
		}
	} else {
		return r.runFinalizer(instance)
	}

	osClient, err := openstack.NewClient()
	if err != nil {
		return reconcile.Result{}, err
	}

	tenant, err := osClient.GetTenantByName(os.Getenv("OS_TENANT_NAME"))
	if err != nil {
		return reconcile.Result{}, err
	}

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
			if rule.RemoteIpPrefix == existsRule.RemoteIPPrefix && rule.PortRangeMax == existsRule.PortRangeMax && rule.PortRangeMin == existsRule.PortRangeMin {
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
			if existRule.RemoteIPPrefix == rule.RemoteIpPrefix && existRule.PortRangeMax == rule.PortRangeMax && existRule.PortRangeMin == rule.PortRangeMin {
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

	clientset, err := kubeClient()
	if err != nil {
		log.Info("Error", "Failed to create kubeClient", err.Error())
		return reconcile.Result{}, err
	}

	nodes, err := clientset.CoreV1().Nodes().List(metav1.ListOptions{LabelSelector: labelSelector(instance)})
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
			osClient.DettachSG(strings.ToLower(id), instance.Spec.Name)
			instance.Status.Nodes = removeString(instance.Status.Nodes, id)
		}
	}

	for _, node := range nodes.Items {
		id := node.Status.NodeInfo.SystemUUID
		hasSg, err := osClient.ServerHasSG(strings.ToLower(id), instance.Spec.Name)
		if err != nil {
			log.Info("Error", "Failed to ServerHasSG", err.Error())
			return reconcile.Result{}, err
		}

		if !hasSg {
			log.Info("Info", "Attach SG to Server", strings.ToLower(id))
			osClient.AttachSG(strings.ToLower(id), instance.Spec.Name)
			instance.Status.Nodes = append(instance.Status.Nodes, strings.ToLower(id))
		}
	}

	if err := r.Update(context.Background(), instance); err != nil {
		log.Info("Debug", "err", err.Error())
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: 60 * time.Second}, nil
}

func (r *ReconcileSecurityGroup) addRule(osClient *openstack.OpenStackClient, id string, rule openstackv1beta1.SecurityGroupRule) error {
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
	err := osClient.AddSecurityGroupRule(createOpts)
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
			log.Info("Debug", "err", err.Error())
			return  err
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

func kubeClient() (*kubernetes.Clientset, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		log.Info("Error", "Failed to get config", err.Error())
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Info("Error", "Failed to NewForConfig", err.Error())
		return nil, err
	}

	return clientset, nil
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
