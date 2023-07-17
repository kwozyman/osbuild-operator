/*
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
*/

package controller

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"text/template"

	osbuildv1alpha1 "github.com/kwozyman/osbuild-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	kubevirt "kubevirt.io/api/core/v1"
	"kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

const defaultSubscriptionSecretName = "osbuild-subscription-secret"

// ImageBuilderReconciler reconciles a ImageBuilder object
type ImageBuilderReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=osbuild.rh-ecosystem-edge.io,resources=imagebuilders,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=osbuild.rh-ecosystem-edge.io,resources=imagebuilders/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=osbuild.rh-ecosystem-edge.io,resources=imagebuilders/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ImageBuilder object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.15.0/pkg/reconcile
func (r *ImageBuilderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("hello operator")

	var imageBuilder osbuildv1alpha1.ImageBuilder
	if err := r.Get(ctx, req.NamespacedName, &imageBuilder); err != nil {
		logger.Error(err, "Unable to fetch ImageBuilder")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var subscriptionSecretName string //this is where we get the RH sub secret
	if imageBuilder.Spec.SubscriptionSecretName == "" {
		logger.Info("spec.subscriptionSecret is not set, using default")
		subscriptionSecretName = defaultSubscriptionSecretName
	} else {
		subscriptionSecretName = imageBuilder.Spec.SubscriptionSecretName
	}
	logger.Info(subscriptionSecretName)
	subscriptionSecret := &corev1.Secret{}

	err := r.Get(ctx, client.ObjectKey{
		Namespace: req.NamespacedName.Namespace,
		Name:      subscriptionSecretName,
	}, subscriptionSecret)
	if err != nil {
		logger.Error(err, "Could not find subscriptionSecret")
		return ctrl.Result{}, err
	}

	logger.Info("Building VM object")
	//logger.Info(fmt.Sprintf("%s", cloudInitData(*subscriptionSecret)))
	vm := r.createVM(ctx, cloudInitData(*subscriptionSecret), imageBuilder.Name, imageBuilder.Namespace)
	logger.Info("Creating VM object")

	if err := r.Create(ctx, &vm); err != nil {
		logger.Error(err, "Could not create VM")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func cloudInitData(subSecret corev1.Secret) string {
	type rhSub struct {
		Username string
		Password string
	}
	secret := rhSub{
		Username: string(subSecret.Data["username"]),
		Password: string(subSecret.Data["password"]),
	}
	const configTemplate = `#cloud-config
user: cloud-user
password: redhat
chpasswd: { expire: False }
ssh_authorized_keys:
  - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMPkccS+SKCZEWGJzH7ew0eNPItvqeGFpOhZprmL9owO fortress_of_solitude
rh_subscription:
  username: {{.Username}}
  password: {{.Password}}
write_files:
  - path: /etc/systemd/system/osbuild-proxy.service
    permissions: "0644"
    content: |
      [Unit]
      Description=OSBuild tcp to socket bridge
      After=osbuild-composer.socket
      Requires=osbuild-composer.socket
      [Service]
      Type=simple
      StandardOutput=syslog
      StandardError=syslog
      SyslogIdentifier=osbuild-proxy
      ExecStart=socat -d -d TCP-LISTEN:8080,fork UNIX-CONNECT:/run/weldr/api.socket
      Restart=always
      [Install]
      WantedBy=multi-user.target
runcmd:
  - [dnf, install, -y, osbuild-composer, composer-cli, socat]
  - [systemctl, daemon-reload]
  - [systemctl, enable, --now, osbuild-composer.socket, osbuild-proxy]
	`
	config, err := template.New("cloudConfig").Parse(configTemplate)
	if err != nil {
		panic(err)
	}
	var renderedTemplate bytes.Buffer
	config.Execute(&renderedTemplate, secret)
	return strings.ReplaceAll(renderedTemplate.String(), "\t", "    ")
}

func (r *ImageBuilderReconciler) createVM(ctx context.Context, cloudInitData string, name string, namespace string) kubevirt.VirtualMachine {
	logger := log.FromContext(ctx)
	rootVolumeName := fmt.Sprintf("%s-vm-volume", name)
	logger.Info("built rootVolumeName")

	dataVolumeTemplateSpec := kubevirt.DataVolumeTemplateSpec{}
	dataVolumeTemplateSpec.Kind = "DataVolume"
	dataVolumeTemplateSpec.Name = rootVolumeName
	dataVolumeTemplateSpec.Spec.SourceRef = &v1beta1.DataVolumeSourceRef{
		Kind: "DataSource",
	}
	dataVolumeTemplateSpec.Spec.SourceRef.Name = "rhel9"
	dataVolumeTemplateSpecNamespace := "openshift-virtualization-os-images"
	dataVolumeTemplateSpec.Spec.SourceRef.Namespace = &dataVolumeTemplateSpecNamespace

	dataVolumeTemplateSpec.Spec.Storage = &v1beta1.StorageSpec{
		Resources: corev1.ResourceRequirements{
			Requests: map[corev1.ResourceName]resource.Quantity{
				corev1.ResourceStorage: resource.MustParse("30Gi"),
			},
		},
	}

	vmInstanceTemplateSpec := kubevirt.VirtualMachineInstanceTemplateSpec{}
	vmInstanceTemplateSpec.ObjectMeta.Labels = map[string]string{
		"eci": fmt.Sprintf("%s-image-builder", name),
	}
	vmInstanceTemplateSpec.ObjectMeta.Annotations = map[string]string{
		"vm.kubevirt.io/flavor":   "small",
		"vm.kubevirt.io/os":       "rhel9",
		"vm.kubevirt.io/workload": "server",
	}
	vmInstanceTemplateSpec.Spec.Domain.CPU = &kubevirt.CPU{
		Cores:   2,
		Sockets: 1,
		Threads: 1,
	}

	vmInstanceTemplateSpec.Spec.Domain.Resources = kubevirt.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	}

	vmInstanceTemplateSpec.Spec.Domain.Firmware = &kubevirt.Firmware{
		Bootloader: &kubevirt.Bootloader{
			EFI: &kubevirt.EFI{},
		},
	}
	vmInstanceTemplateSpec.Spec.Domain.Features = &kubevirt.Features{
		SMM: &kubevirt.FeatureState{
			Enabled: pointer.Bool(true),
		},
	}

	vmInstanceTemplateSpec.Spec.Domain.Devices = kubevirt.Devices{
		Disks: []kubevirt.Disk{
			{
				Name: "rootdisk",
				DiskDevice: kubevirt.DiskDevice{
					Disk: &kubevirt.DiskTarget{
						Bus: kubevirt.DiskBusVirtio,
					},
				},
			},
			{
				Name: "cloudinitdisk",
				DiskDevice: kubevirt.DiskDevice{
					Disk: &kubevirt.DiskTarget{
						Bus: kubevirt.DiskBusVirtio,
					},
				},
			},
		},
		Interfaces: []kubevirt.Interface{
			{
				Name:  "default",
				Model: "virtio",
				InterfaceBindingMethod: kubevirt.InterfaceBindingMethod{
					Masquerade: &kubevirt.InterfaceMasquerade{},
				},
			},
		},
		NetworkInterfaceMultiQueue: pointer.Bool(true),
		Rng:                        &kubevirt.Rng{},
	}

	vmInstanceTemplateSpec.Spec.Networks = []kubevirt.Network{
		{
			Name: "default",
			NetworkSource: kubevirt.NetworkSource{
				Pod: &kubevirt.PodNetwork{},
			},
		},
	}

	vmInstanceTemplateSpec.Spec.Volumes = []kubevirt.Volume{
		{
			Name: "rootdisk",
			VolumeSource: kubevirt.VolumeSource{
				DataVolume: &kubevirt.DataVolumeSource{
					Name: rootVolumeName,
				},
			},
		},
		{
			Name: "cloudinitdisk",
			VolumeSource: kubevirt.VolumeSource{
				CloudInitNoCloud: &kubevirt.CloudInitNoCloudSource{
					UserData: cloudInitData,
				},
			},
		},
	}

	vmInstace := kubevirt.VirtualMachine{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "kubevirt.io/v1",
			Kind:       "VirtualMachine",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: kubevirt.VirtualMachineSpec{
			DataVolumeTemplates: []kubevirt.DataVolumeTemplateSpec{
				dataVolumeTemplateSpec,
			},
			Template: &vmInstanceTemplateSpec,
			Running:  pointer.Bool(true),
		},
	}

	return vmInstace
}

// SetupWithManager sets up the controller with the Manager.
func (r *ImageBuilderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&osbuildv1alpha1.ImageBuilder{}).
		Complete(r)
}
