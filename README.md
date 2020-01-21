# openstack-sg-controller

[![Build Status](https://travis-ci.org/takaishi/openstack-sg-controller.svg?branch=master)](https://travis-ci.org/takaishi/openstack-sg-controller)

## Install

```
$ kubectl create namespace openstack-sg-controller-system
$ kubectl -n openstack-sg-controller-system create secret generic openstack-sg-controller \
  --from-literal=os_auth_url='https://your-endpoint/auth/v3' \
  --from-literal=os_username='yourname' \
  --from-literal=os_password='password' \
  --from-literal=os_project_name='tenant-name' \
  --from-literal=os_tenant_name='tenant-name' \
  --from-literal=os_region_name='RegionOne' \
  --from-literal=os_identity_api_version='3' \
  --from-literal=os_domain_name='Default' \
  --from-literal=os_project_domain_name='Default' \
  --from-literal=os_user_domain_name='Default'
$ make deploy
```

## Usage

Running on localhost:

```
$ make run
```

Build image :

```
$ make docker-build
```

Push image:

```
$ make docker-push
```

Deploy to Kubernetes:

```
$ make deploy
```