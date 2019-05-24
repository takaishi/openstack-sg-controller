# openstack-sg-controller

[![Build Status](https://travis-ci.org/takaishi/openstack-sg-controller.svg?branch=master)](https://travis-ci.org/takaishi/openstack-sg-controller)

## Install

```

$ kubectl create namespace openstack-sg-controller-system
$ kubectl apply -f ./config/crds/
$ kubectl -n openstack-sg-controller-system create secret generic openstack-sg-controller \
  --from-literal=os_auth_url='' \
  --from-literal=os_username='' \
  --from-literal=os_password='' \
  --from-literal=os_project_name='' \
  --from-literal=os_tenant_name='' \
  --from-literal=os_region_name='' \
  --from-literal=os_domain_name='' \
  --from-literal=os_identity_api_version='' \
  --from-literal=os_project_domain_name='' \
  --from-literal=os_user_domain_name=''
$ kubectl apply -f ./config/default/
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



