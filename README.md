# osbuild-operator
Operator for Openshift designed to simplitfy usage of [Image Builder](https://access.redhat.com/documentation/en-us/red_hat_enterprise_linux/9/html/composing_a_customized_rhel_system_image/index).

## Description
Even though a comprehensive [OSBuild Ansible collection](https://github.com/redhat-cop/infra.osbuild) exists, there is still a gap in running OSBuild on top of Openshift. This operator provides an opinionated way to simplify image generatation using an Openshift cluster.

## Getting Started
You’ll need an Openshift cluster to run against with enough available resources to start a 4Gi / 2 core virtual machine. Please note if your cluster is already running inside virtual machines, you will need nested virtualization.

[Openshift Virtualization](https://docs.openshift.com/container-platform/4.13/virt/about-virt.html) and [Openshift Pipelines](https://docs.openshift.com/container-platform/4.13/cicd/pipelines/op-release-notes.html) are required in your cluster.

### Running on the cluster using prebuilt image

If you just want to run the operator using the prebuilt image at `quay.io/cgament/osbuild-operator:latest`, you just need to run:

```sh
make deploy
```

This will create a `osbuild-operator-controller-manager` deployment in the `osbuild-operator-system` namespace and install the required CRDs.

If you want to use a custom image:

```
IMG=<registry>/<image>:<tag> make deploy
```

### Uninstallation

```sh
make undeploy
```

## Test it Out

There are two CRDs at the moment:

1. ImageBuilder

```yaml
apiVersion: osbuild.rh-ecosystem-edge.io/v1alpha1
kind: ImageBuilder
metadata:
  name: <name>
spec:
  sshKey: "<ssh-key>"    # optional
  subscriptionSecret:    # optional; default=osbuild-subscription-secret
  servicePort:           # optional; default=8080
```

`ImageBuilder` is a namespaced resource, with the following fields:
  * `spec.sshKey`: optional, the key to be used for accessing the virtual machine with the user `cloud-user`
  * `spec.subscriptionSecret`: optional, default is `osbuild-subscription-secret`, the name of the secret that holds the Red Hat subscription username and password; must be in the same namespace as `ImageBuilder` resource
  * `spec.servicePort`: optional, defaults to `8080`, the port on which the osbuild service will be exposed

Creating this resource will run and configure a virtual machine that runs OSBuild and exposes the API via a Openshift service.

Deleting this resource will cleanup and delete all the resources associated with it.

2. ImageBuilderImage

```yaml
apiVersion: osbuild.rh-ecosystem-edge.io/v1alpha1
kind: ImageBuilderImage
metadata:
  name: image
spec:
  imageBuilder: <imagebuilder>          # optional
  userName: "<user-name>"               # optional; default=root
  sshKey: "<ssh-key>"                   # optional
  isoTarget: "<target>"                 # optional; default=edge-installer
  installationDevice: "<device>"        # optional
  fdoManufacturingServerUrl: "<url>"    # optional
  persistentVolumeName: <pvc-name>      # optional; default=<name>-data
  blueprintTemplate: "<go-template>"    # optional; specify a Go Template for the commit blueprint
  blueprintIsoTemplate: "<go-template>" # optional; specify a Go Temaplte for the installer iso blueprint
```

`ImageBuilderImage` is a namespaced resource, with the following fields:
  * `spec.imageBuilder`: optional, name the of `ImageBuilder` service to be used. If missing, the operator will try to use an existing resource in the current namespace
  * `spec.userName`: optional, defaults to `root`, user to embed in the image
  * `spec.sshKey`: optional, the ssh key used for accesing the image for the specified user
  * `spec.persistentVolumeName`: optional, defaults to `<ImageBuilderImage.name>-data`. the volume used for storing generated images and temporary data. The PVC needs to exist, the operator will not create a new one.
  * `spec.isoTarget`: optional, defaults to `edge-installer`. Can be `edge-installer` or `edge-simplified-installer` for FDO
  * `spec.installationDevice`: optional, the installation device; required `edge-simplified-installer`
  * `spec.fdoManufacturingServerUrl`: optional, the FDO Manufacturing server url; required for `edge-simplified-installer` target
  * `spec.blueprintTemplate`: optional, a Go Template can be specified for the commit blueprint using the Spec variables. The default is usually good enough.
  * `spec.blueprintIsoTemplate`: optional, a Go Template can be specified for the installation blueprint using the Spec variables. The default is usually good enough.

Creating this resource will generate and create several Openshift objects, including a `Pipeline` and a paused `PipelineRun`. Starting this pipeline will generate an ostree commit and the associated installation iso. The artifacts can be accessed as follows:

```sh
url=$(oc get routes.route.openshift.io --selector osbuild-operator-image -o json | jq -r '.items[].spec.host')
curl -LO "${url}/installation.iso"
curl -L "${url}/repo/"
```

## Development

Build and push your image to the location specified by `IMG`:

```sh
make docker-build docker-push IMG=<registry>/<image>:<tag>
```

To install the CRDs from the cluster:
```sh
make install
```

To delete the CRDs from the cluster:

```sh
make uninstall
```

## License

Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
