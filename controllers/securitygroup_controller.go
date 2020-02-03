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
	"github.com/pkg/errors"
	"github.com/takaishi/openstack-sg-controller/internal"
	v1 "k8s.io/api/core/v1"
	errors_ "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
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
	Log             logr.Logger
	Scheme          *runtime.Scheme
	OpenStackClient internal.OpenStackClientInterface
	recorder        record.EventRecorder
}

// +kubebuilder:rbac:groups=openstack.repl.info,resources=securitygroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=openstack.repl.info,resources=securitygroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=openstack.repl.info,resources=events,verbs=get;update;patch
// +kubebuilder:rbac:groups=,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=,resources=nodes/status,verbs=get

func (r *SecurityGroupReconciler) Reconcile(req ctrl.Request) (_ ctrl.Result, reterr error) {
	_ = context.Background()
	_ = r.Log.WithValues("securitygroup", req.NamespacedName)

	// your logic here
	// Fetch the SecurityGroup instance
	instance := &openstackv1beta1.SecurityGroup{}
	err := r.Get(context.TODO(), req.NamespacedName, instance)
	if err != nil {
		if errors_.IsNotFound(err) {
			r.Log.Info("SecurityGroup not found", "Namespace", req.Namespace, "Name", req.Name)
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	nodes, err := r.getNodes(instance)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Always attempt to update SecurityGroup status after reconcile.
	defer func() {
		if err := r.Status().Update(context.Background(), instance); err != nil {
			reterr = err
		}
	}()

	// Handle deletion reconcile loop
	if !instance.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(instance, nodes)
	}

	// Handle normal reconcile loop
	return r.reconcile(instance, nodes)
}

func (r *SecurityGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.recorder = mgr.GetEventRecorderFor("cluster-controller")

	return ctrl.NewControllerManagedBy(mgr).
		For(&openstackv1beta1.SecurityGroup{}).
		Complete(r)
}

func (r *SecurityGroupReconciler) reconcile(instance *openstackv1beta1.SecurityGroup, nodes []v1.Node) (_ ctrl.Result, reterr error) {
	var sg *groups.SecGroup

	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		if !containsString(instance.ObjectMeta.Finalizers, finalizerName) {
			return r.setFinalizer(instance)
		}
	}

	tenant, err := r.OpenStackClient.GetTenantByName(os.Getenv("OS_TENANT_NAME"))
	if err != nil {
		return reconcile.Result{}, err
	}

	// Create the SecurityGroup when it's no exists
	sg, err = r.ensureSG(instance, tenant)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Add the rule to securityGroup when that does'nt have.
	err = r.addRule(instance, sg)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Detach the securityGroup from node when node's label are unmatch.
	err = r.detachSG(instance, sg, nodes)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Delete the rule from securityGroup when spec doesn't have.
	err = r.deleteRule(instance, sg)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Atach securityGroup to node.
	err = r.attachSG(instance, sg, nodes)
	if err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *SecurityGroupReconciler) deleteExternalDependency(instance *openstackv1beta1.SecurityGroup, nodes []v1.Node) error {
	sg, err := r.OpenStackClient.GetSecurityGroup(instance.Status.ID)
	if err != nil {
		_, notfound := err.(gophercloud.ErrDefault404)
		if notfound {
			r.recorder.Eventf(instance, v1.EventTypeNormal, "SecurityGroupAlreadyDeleted", "SecurityGroup %s has deleted already.", instance.Spec.Name)
			return nil
		}
		return errors.Wrap(err, "GetSecurityGroup failed")
	}

	for _, node := range nodes {
		id := strings.ToLower(node.Status.NodeInfo.SystemUUID)
		hasSg, err := r.OpenStackClient.ServerHasSG(id, instance.Status.Name)
		if err != nil {
			return errors.Wrap(err, "ServerHasSG failed")
		}

		if hasSg {
			err = r.OpenStackClient.DetachSG(id, instance.Status.Name)
			if err != nil {
				r.recorder.Eventf(instance, v1.EventTypeWarning, "FailureDetachSecurityGroup", "Failed to detach SecurityGroup %s from %s: %s", instance.Spec.Name, id, err.Error())
			}
		}
	}

	err = r.OpenStackClient.DeleteSecurityGroup(sg.ID)
	if err != nil {
		r.recorder.Eventf(instance, v1.EventTypeWarning, "FailureDeleteSecurityGroup", "Failed to delete SecurityGroup %s %s", instance.Spec.Name, err.Error())
		return errors.Wrap(err, "DeleteSecurityGroup failed")
	}
	r.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulDeleteExternalDependency", "Deleted all external dependency")

	return nil
}

func (r *SecurityGroupReconciler) ensureSG(instance *openstackv1beta1.SecurityGroup, tenant projects.Project) (*groups.SecGroup, error) {
	sg, err := r.OpenStackClient.GetSecurityGroup(instance.Status.ID)
	if err != nil {
		switch err.(type) {
		case gophercloud.ErrDefault404:
			sg, err = r.OpenStackClient.CreateSecurityGroup(instance.Spec.Name, "", tenant.ID)
			if err != nil {
				r.recorder.Eventf(instance, v1.EventTypeWarning, "FailureEnsure", "Failed to create SecurityGroup %s: %s", instance.Spec.Name, err.Error())
				return nil, err
			}
			instance.Status.ID = sg.ID
			instance.Status.Name = sg.Name
			r.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulEnsure", "Created SecurityGroup %s", instance.Spec.Name)
		default:
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
			err := r.OpenStackClient.AddSecurityGroupRule(createOpts)
			if err != nil {
				r.recorder.Eventf(instance, v1.EventTypeWarning, "FailureAddRule", "Failed to create SecurityGroupRule cidr=%s, port=%s: %s", rule.RemoteIpPrefix, fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax), err.Error())
				return err
			}
			r.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulAddRule", "Created SecurityGroupRule cidr=%s, port=%s", rule.RemoteIpPrefix, fmt.Sprintf("%d-%d", rule.PortRangeMin, rule.PortRangeMax))
		}
	}
	return nil
}

func (r *SecurityGroupReconciler) setFinalizer(sg *openstackv1beta1.SecurityGroup) (reconcile.Result, error) {
	sg.ObjectMeta.Finalizers = append(sg.ObjectMeta.Finalizers, finalizerName)
	if err := r.Update(context.Background(), sg); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *SecurityGroupReconciler) reconcileDelete(sg *openstackv1beta1.SecurityGroup, nodes []v1.Node) (reconcile.Result, error) {
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
			err := r.OpenStackClient.DetachSG(strings.ToLower(id), sg.Name)
			if err != nil {
				r.recorder.Eventf(instance, v1.EventTypeWarning, "FailureDetachSecurityGroup", "Failed to detach SecurityGroup %s: %s", instance.Spec.Name, err.Error())
				return err
			}
			instance.Status.Nodes = removeString(instance.Status.Nodes, id)
			r.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessDetachSecurityGroup", "Detached SecurityGroup %s from %s", instance.Spec.Name, id)
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
			err := r.OpenStackClient.DeleteSecurityGroupRule(existRule.ID)
			if err != nil {
				r.recorder.Eventf(instance, v1.EventTypeWarning, "FailureDeleteRule", "Failed to delete SecurityGroupRulecidr=%s, port=%s: %s", existRule.RemoteIPPrefix, fmt.Sprintf("%d-%d", existRule.PortRangeMin, existRule.PortRangeMax), err.Error())
				return err
			}
			r.recorder.Eventf(instance, v1.EventTypeNormal, "SuccessfulDeleteRule", "Deleted SecurityGroupRule cidr=%s, port=%s", existRule.RemoteIPPrefix, fmt.Sprintf("%d-%d", existRule.PortRangeMin, existRule.PortRangeMax))
		}
	}
	return nil
}

func (r *SecurityGroupReconciler) attachSG(instance *openstackv1beta1.SecurityGroup, sg *groups.SecGroup, nodes []v1.Node) error {
	for _, node := range nodes {
		id := node.Status.NodeInfo.SystemUUID
		hasSg, err := r.OpenStackClient.ServerHasSG(strings.ToLower(id), sg.Name)
		if err != nil {
			return err
		}

		if !hasSg {
			if err = r.OpenStackClient.AttachSG(strings.ToLower(id), sg.Name); err != nil {
				r.recorder.Eventf(instance, v1.EventTypeWarning, "FailureAttachSecurityGroup", "Failed to attach SecurityGroup %s to %s: %s", sg.Name, id, err.Error())
				return err
			}
			instance.Status.Nodes = append(instance.Status.Nodes, strings.ToLower(id))
			r.recorder.Eventf(instance, v1.EventTypeNormal, "AttachSecurityGroup", "Attached SecurityGroup %s to %s", sg.Name, id)
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
