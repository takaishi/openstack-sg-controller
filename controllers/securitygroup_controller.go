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
	"context"
	"fmt"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	"github.com/takaishi/openstack-sg-controller/internal"
	v1 "k8s.io/api/core/v1"
	errors_ "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	openstackv1beta1 "github.com/takaishi/openstack-sg-controller/api/v1beta1"
)

var finalizerName = "finalizer.securitygroups.openstack.repl.info"

// SecurityGroupReconciler reconciles a SecurityGroup object
type SecurityGroupReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	osClient internal.OpenStackClientInterface
}

// +kubebuilder:rbac:groups=openstack.repl.info,resources=securitygroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openstack.repl.info,resources=securitygroups/status,verbs=get;update;patch

func (r *SecurityGroupReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	_ = context.Background()
	_ = r.Log.WithValues("securitygroup", req.NamespacedName)

	// your logic here
	// Fetch the SecurityGroup instance
	instance := &openstackv1beta1.SecurityGroup{}
	err := r.Get(context.TODO(), req.NamespacedName, instance)
	if err != nil {
		if errors_.IsNotFound(err) {
			r.Log.Info("Debug: instance not found", "SecurityGroup", instance.Name)
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	r.Log.Info("Info: Start reconcile", "sg", instance.Name)
	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		r.Log.Info("Debug: deletion timestamp is zero")
		if !containsString(instance.ObjectMeta.Finalizers, finalizerName) {
			r.Log.Info("Debug: Set Finalizer")
			return r.setFinalizer(instance)
		}
	} else {
		nodes, err := r.getNodes(instance)
		if err != nil {
			r.Log.Info("Error", "Failed to get Nodes", err.Error())
			return reconcile.Result{}, err
		}

		return r.runFinalizer(instance, nodes)
	}

	r.Log.Info("Debug", "called", "GetTenantByName", "tenant", os.Getenv("OS_TENANT_NAME"))
	tenant, err := r.osClient.GetTenantByName(os.Getenv("OS_TENANT_NAME"))
	if err != nil {
		return reconcile.Result{}, err
	}

	nodes, err := r.getNodes(instance)
	if err != nil {
		r.Log.Info("Error", "Failed to get Nodes", err.Error())
		return reconcile.Result{}, err
	}

	var sg *groups.SecGroup

	// Create the SecurityGroup when it's no exists
	sg, err = r.ensureSG(instance, tenant)
	if err != nil {
		r.Log.Info("Error", "Failed to ensureSG", err.Error())
		return reconcile.Result{}, err
	}

	// Add the rule to securityGroup when that does'nt have.
	err = r.addRule(instance, sg)
	if err != nil {
		r.Log.Info("Error", "addRule", err.Error())
		return reconcile.Result{}, err
	}

	// Detach the securityGroup from node when node's label are unmatch.
	err = r.detachSG(instance, sg, nodes)
	if err != nil {
		r.Log.Info("Error", "Failed to detachSG", err.Error())
		return reconcile.Result{}, err
	}

	// Delete the rule from securityGroup when spec doesn't have.
	err = r.deleteRule(instance, sg)
	if err != nil {
		r.Log.Info("Error", "Failed to deleteRule", err.Error())
		return reconcile.Result{}, err
	}

	// Atach securityGroup to node.
	err = r.attachSG(instance, sg, nodes)
	if err != nil {
		r.Log.Info("Error", "Failed to attachSG", err.Error())
		return reconcile.Result{}, err
	}

	if err := r.Status().Update(context.Background(), instance); err != nil {
		r.Log.Info("Debug", "failed to update sg", err.Error())
		return reconcile.Result{}, err
	}

	r.Log.Info("Info: Success reconcile", "sg", instance.Name)
	return reconcile.Result{}, nil
}

func (r *SecurityGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&openstackv1beta1.SecurityGroup{}).
		Complete(r)
}

func (r *SecurityGroupReconciler) deleteExternalDependency(instance *openstackv1beta1.SecurityGroup, nodes []v1.Node) error {
	r.Log.Info("Info", "deleting the external dependencies", instance.Status.Name)

	r.Log.Info("Call GetSecurityGroup", "instance.Statu.ID", instance.Status.ID)
	sg, err := r.osClient.GetSecurityGroup(instance.Status.ID)
	if err != nil {
		_, notfound := err.(gophercloud.ErrDefault404)
		if notfound {
			r.Log.Info("Info", "already delete security group", instance.Status.Name)
			return nil
		}
		return err
	}

	for _, node := range nodes {
		id := strings.ToLower(node.Status.NodeInfo.SystemUUID)
		r.Log.Info("Call ServerHasSG", "id", id, "instance.Status.Name", instance.Status.Name)
		hasSg, err := r.osClient.ServerHasSG(id, instance.Status.Name)
		if err != nil {
			r.Log.Info("Error", "Failed to ServerHasSG", err.Error())
			return err
		}

		if hasSg {
			r.Log.Info("Call: DetachSG", "id", id, "instance.Status.Name", instance.Status.Name)
			r.osClient.DetachSG(id, instance.Status.Name)
		}
	}

	r.Log.Info("Call: DeleteSecurityGroup", "sg.ID", sg.ID)
	return r.osClient.DeleteSecurityGroup(sg.ID)
}

func (r *SecurityGroupReconciler) ensureSG(instance *openstackv1beta1.SecurityGroup, tenant projects.Project) (*groups.SecGroup, error) {
	r.Log.Info("Call GetSecurityGroup", "instance.Statu.ID", instance.Status.ID)
	sg, err := r.osClient.GetSecurityGroup(instance.Status.ID)
	if err != nil {
		switch err.(type) {
		case gophercloud.ErrDefault404:
			r.Log.Info("Creating SG", "name", instance.Spec.Name)
			r.Log.Info("Debug", "name", instance.Spec.Name, "tenant", tenant.ID)
			sg, err = r.osClient.CreateSecurityGroup(instance.Spec.Name, "", tenant.ID)
			if err != nil {
				r.Log.Info("Error", "msg", err.Error())
				return nil, err
			}
			instance.Status.ID = sg.ID
			instance.Status.Name = sg.Name
			r.Log.Info("Success creating SG", "name", instance.Spec.Name, "id", sg.ID)
		default:
			r.Log.Info("Debug: errorrrrrr")
			return nil, err
		}
	}

	return sg, nil
}

func (r *SecurityGroupReconciler) addRule(instance *openstackv1beta1.SecurityGroup, sg *groups.SecGroup) error {
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
			createOpts := rules.CreateOpts{
				Direction:      rules.RuleDirection(rule.Direction),
				SecGroupID:     sg.ID,
				PortRangeMax:   rule.PortRangeMax,
				PortRangeMin:   rule.PortRangeMin,
				RemoteIPPrefix: rule.RemoteIpPrefix,
				EtherType:      rules.RuleEtherType(rule.EtherType),
				Protocol:       rules.RuleProtocol(rule.Protocol),
			}
			r.Log.Info("Creating SG Rule", "cidr", rule.RemoteIpPrefix, "port", fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax))
			err := r.osClient.AddSecurityGroupRule(createOpts)
			if err != nil {
				r.Log.Info("Error", "addRule", err.Error())
				return err
			}
			r.Log.Info("Success to create SG Rule", "cidr", rule.RemoteIpPrefix, "port", fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax))
		}
	}
	return nil
}

func (r *SecurityGroupReconciler) setFinalizer(sg *openstackv1beta1.SecurityGroup) (reconcile.Result, error) {
	sg.ObjectMeta.Finalizers = append(sg.ObjectMeta.Finalizers, finalizerName)
	if err := r.Update(context.Background(), sg); err != nil {
		r.Log.Info("Debug", "failed to update sg", err.Error())
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *SecurityGroupReconciler) runFinalizer(sg *openstackv1beta1.SecurityGroup, nodes []v1.Node) (reconcile.Result, error) {
	if containsString(sg.ObjectMeta.Finalizers, finalizerName) {
		if err := r.deleteExternalDependency(sg, nodes); err != nil {
			return reconcile.Result{}, err
		}

		sg.ObjectMeta.Finalizers = removeString(sg.ObjectMeta.Finalizers, finalizerName)
		if err := r.Update(context.Background(), sg); err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}

func (r *SecurityGroupReconciler) getNodes(instance *openstackv1beta1.SecurityGroup) ([]v1.Node, error) {
	var nodes v1.NodeList
	ls, err := convertLabelSelectorToLabelsSelector(labelSelector(instance))
	if err != nil {
		return nil, err
	}

	listOpts := client.ListOptions{
		LabelSelector: ls,
	}

	err = r.List(context.Background(), &nodes, &listOpts)
	if err != nil {
		return nil, err
	}

	return nodes.Items, nil
}

// detachSG detaches securityGroup from node when node's label are'nt match.
func (r *SecurityGroupReconciler) detachSG(instance *openstackv1beta1.SecurityGroup, sg *groups.SecGroup, nodes []v1.Node) error {
	existsNodeIDs := []string{}
	for _, node := range nodes {
		existsNodeIDs = append(existsNodeIDs, strings.ToLower(node.Status.NodeInfo.SystemUUID))
	}

	for _, id := range instance.Status.Nodes {
		if !containsString(existsNodeIDs, id) {
			r.Log.Info("Info", "Detach SG from Server", strings.ToLower(id))
			err := r.osClient.DetachSG(strings.ToLower(id), sg.Name)
			if err != nil {
				r.Log.Info("Error", "Failed to DetachSG", err.Error())
				return err
			}
			instance.Status.Nodes = removeString(instance.Status.Nodes, id)
		}
	}

	return nil
}

func (r *SecurityGroupReconciler) deleteRule(instance *openstackv1beta1.SecurityGroup, sg *groups.SecGroup) error {
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
			r.Log.Info("Deleting SG Rule", "cidr", existRule.RemoteIPPrefix, "port", fmt.Sprintf("%d-%d", existRule.PortRangeMin, existRule.PortRangeMax))
			err := r.osClient.DeleteSecurityGroupRule(existRule.ID)
			if err != nil {
				return err
			}
			r.Log.Info("Success to delete SG Rule", "cidr", existRule.RemoteIPPrefix, "port", fmt.Sprintf("%d-%d", existRule.PortRangeMin, existRule.PortRangeMax))
		}
	}
	return nil
}

func (r *SecurityGroupReconciler) attachSG(instance *openstackv1beta1.SecurityGroup, sg *groups.SecGroup, nodes []v1.Node) error {
	for _, node := range nodes {
		id := node.Status.NodeInfo.SystemUUID
		r.Log.Info("Call ServerHasSG", "id", id, "sg.Name", sg.Name)
		hasSg, err := r.osClient.ServerHasSG(strings.ToLower(id), sg.Name)
		if err != nil {
			r.Log.Info("Error", "Failed to ServerHasSG", err.Error())
			return err
		}

		if !hasSg {
			r.Log.Info("Call AttachSG", "id", id, "sg.Name", sg.Name)
			if err = r.osClient.AttachSG(strings.ToLower(id), sg.Name); err != nil {
				r.Log.Info("Debug", "failed to attach sg", err.Error())
				return err
			}
			instance.Status.Nodes = append(instance.Status.Nodes, strings.ToLower(id))
		}
	}

	return nil
}

func convertLabelSelectorToLabelsSelector(selector string) (labels.Selector, error) {
	s, err := metav1.ParseToLabelSelector(selector)
	if err != nil {
		return nil, err
	}
	return metav1.LabelSelectorAsSelector(s)
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
