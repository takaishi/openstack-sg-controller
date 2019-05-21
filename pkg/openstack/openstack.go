package openstack

import (
	"crypto/tls"
	"fmt"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/secgroups"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"io/ioutil"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"net/http"
	"os"

	"github.com/gophercloud/gophercloud"
	_openstack "github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v2/tenants"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/projects"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/groups"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/security/rules"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/pkg/errors"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
)

var log = logf.Log.WithName("controller")

type OpenStackClientInterface interface {
	CreateSecurityGroup(name string, description string, tenantID string) (*groups.SecGroup, error)
	DeleteSecurityGroup(id string) error
	AddSecurityGroupRule(opts rules.CreateOpts) error
	DeleteSecurityGroupRule(id string) error
	GetSecurityGroup(id string) (*groups.SecGroup, error)
	GetSecurityGroupByName(name string) (groups.SecGroup, error)

	GetTenant(id string) (tenants.Tenant, error)
	GetTenantByName(name string) (projects.Project, error)

	ServerHasSG(id string, sgName string) (bool, error)
	AttachSG(id string, sgName string) error
	DettachSG(id string, sgName string) error
	RandomString() string
}

type OpenStackClient struct {
	providerClient *gophercloud.ProviderClient
	regionName     string
}

func NewClient() (OpenStackClientInterface, error) {
	client := OpenStackClient{}
	client.regionName = os.Getenv("OS_REGION_NAME")
	cert := os.Getenv("OS_CERT")
	key := os.Getenv("OS_KEY")

	authOpts, err := _openstack.AuthOptionsFromEnv()
	if err != nil {
		return nil, errors.Wrapf(err, "Failed to create AuthOptions from env")
	}

	client.providerClient, err = _openstack.NewClient(authOpts.IdentityEndpoint)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{}
	if cert != "" && key != "" {
		clientCert, err := ioutil.ReadFile(cert)
		if err != nil {
			return nil, err
		}
		clientKey, err := ioutil.ReadFile(key)
		if err != nil {
			return nil, err
		}
		cert, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
		tlsConfig.BuildNameToCertificate()
		transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: tlsConfig}

		client.providerClient.HTTPClient.Transport = transport
	}

	err = _openstack.Authenticate(client.providerClient, authOpts)
	if err != nil {
		return nil, err
	}

	return &client, nil
}

func (client *OpenStackClient) CreateSecurityGroup(name string, description string, tenantID string) (*groups.SecGroup, error) {
	networkClient, err := _openstack.NewNetworkV2(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return nil, err
	}

	createOpts := groups.CreateOpts{
		Name:        name,
		Description: description,
		TenantID:    tenantID,
	}

	res := groups.Create(networkClient, createOpts)
	if res.Err != nil {
		return nil, res.Err
	}

	sg, err := res.Extract()
	if err != nil {
		return nil, err
	}

	return sg, nil
}

func (client *OpenStackClient) DeleteSecurityGroup(id string) error {
	networkClient, err := _openstack.NewNetworkV2(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return err
	}
	res := groups.Delete(networkClient, id)
	if res.Err != nil {
		return res.Err
	}

	return nil
}
func (client *OpenStackClient) AddSecurityGroupRule(opts rules.CreateOpts) error {
	networkClient, err := _openstack.NewNetworkV2(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return err
	}

	res := rules.Create(networkClient, opts)
	if res.Err != nil {
		return res.Err
	}

	return nil
}

func (client *OpenStackClient) DeleteSecurityGroupRule(id string) error {
	networkClient, err := _openstack.NewNetworkV2(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return err
	}

	res := rules.Delete(networkClient, id)
	if res.Err != nil {
		return res.Err
	}

	return nil
}

func (client *OpenStackClient) GetSecurityGroup(id string) (*groups.SecGroup, error) {
	networkClient, err := _openstack.NewNetworkV2(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return nil, err
	}

	return groups.Get(networkClient, id).Extract()
}

func (client *OpenStackClient) GetSecurityGroupByName(name string) (groups.SecGroup, error) {
	networkClient, err := _openstack.NewNetworkV2(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return groups.SecGroup{}, err
	}

	listOpts := groups.ListOpts{
		Name: name,
	}

	resp := []groups.SecGroup{}
	groups.List(networkClient, listOpts).EachPage(func(page pagination.Page) (bool, error) {
		extracted, err := groups.ExtractGroups(page)
		if err != nil {
			return false, err
		}

		for _, item := range extracted {
			resp = append(resp, item)
		}

		return true, nil
	})

	if len(resp) == 0 {
		return groups.SecGroup{}, errors.New("Not found")
	}

	if len(resp) > 1 {
		return groups.SecGroup{}, errors.New("Found sg same name")
	}

	return resp[0], nil
}

func (client *OpenStackClient) GetTenant(id string) (tenants.Tenant, error) {
	identityClient, err := _openstack.NewIdentityV3(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return tenants.Tenant{}, err
	}

	res := tenants.Get(identityClient, id)
	if res.Err != nil {
		return tenants.Tenant{}, res.Err
	}

	t, err := res.Extract()
	if err != nil {
		return tenants.Tenant{}, err
	}

	return *t, nil
}

func (client *OpenStackClient) GetTenantByName(name string) (projects.Project, error) {
	identityClient, err := _openstack.NewIdentityV3(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return projects.Project{}, err
	}

	listOpts := projects.ListOpts{}
	resp := []projects.Project{}
	projects.List(identityClient, &listOpts).EachPage(func(page pagination.Page) (bool, error) {
		extracted, err := projects.ExtractProjects(page)
		if err != nil {
			return false, err
		}

		for _, item := range extracted {
			if item.Name == name {
				resp = append(resp, item)
			}
		}

		return true, nil
	})

	if len(resp) == 0 {
		return projects.Project{}, errors.New(fmt.Sprintf("Cound not found tenant '%s'", name))
	}

	if len(resp) > 1 {
		return projects.Project{}, errors.New(fmt.Sprintf("Found some tenant has same name '%s'", name))
	}
	return resp[0], nil
}

func (client *OpenStackClient) ServerHasSG(id string, sgName string) (bool, error) {
	computeClient, err := _openstack.NewComputeV2(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return false, err
	}

	res := servers.Get(computeClient, id)
	if res.Err != nil {
		return false, res.Err
	}
	server, err := res.Extract()
	if res.Err != nil {
		return false, res.Err
	}
	for _, sg := range server.SecurityGroups {
		if sg["name"] == sgName {
			return true, nil
		}
	}

	return false, nil
}

func (client *OpenStackClient) AttachSG(id string, sgName string) error {
	computeClient, err := _openstack.NewComputeV2(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return err
	}

	res := secgroups.AddServer(computeClient, id, sgName)
	if res.Err != nil {
		return res.Err
	}

	return nil
}

func (client *OpenStackClient) DettachSG(id string, sgName string) error {
	computeClient, err := _openstack.NewComputeV2(client.providerClient, gophercloud.EndpointOpts{Region: client.regionName})
	if err != nil {
		return err
	}

	res := secgroups.RemoveServer(computeClient, id, sgName)
	if res.Err != nil {
		return res.Err
	}

	return nil
}

func (client *OpenStackClient) RandomString() string {
	randomLength := 5
	return utilrand.String(randomLength)
}
