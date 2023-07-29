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

	"k8s.io/apimachinery/pkg/api/errors"
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
const defaultImageBuilderPort int32 = 8080
const imageBuilderLabel = "osbuild-operator-builder"

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

	labels := map[string]string{
		imageBuilderLabel: req.Name,
	}

	var imageBuilder osbuildv1alpha1.ImageBuilder
	if err := r.Get(ctx, req.NamespacedName, &imageBuilder); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Resource not found, must have been deleted")
			if err := DeleteAllObjectsWithLabel(ctx, r.Client, "Service", "v1", imageBuilderLabel, req.Name); err != nil {
				logger.Error(err, "Could not delete services")
				return ctrl.Result{}, err
			}
			if err := DeleteAllObjectsWithLabel(ctx, r.Client, "VirtualMachine", "kubevirt.io/v1", imageBuilderLabel, req.Name); err != nil {
				logger.Error(err, "Could not delete vm")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Unable to fetch ImageBuilder")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var servicePort int32
	if imageBuilder.Spec.ServicePort == 0 {
		logger.Info(fmt.Sprintf("spec.servicePort is not set, using default %v", defaultImageBuilderPort))
		servicePort = defaultImageBuilderPort
	} else {
		servicePort = imageBuilder.Spec.ServicePort
	}

	var subscriptionSecretName string //this is where we get the RH sub secret
	if imageBuilder.Spec.SubscriptionSecretName == "" {
		logger.Info(fmt.Sprintf("spec.subscriptionSecret is not set, using default %s", defaultSubscriptionSecretName))
		subscriptionSecretName = defaultSubscriptionSecretName
	} else {
		subscriptionSecretName = imageBuilder.Spec.SubscriptionSecretName
	}
	subscriptionSecret := &corev1.Secret{}

	err := r.Get(ctx, client.ObjectKey{
		Namespace: req.NamespacedName.Namespace,
		Name:      subscriptionSecretName,
	}, subscriptionSecret)
	if err != nil {
		logger.Error(err, "Could not get subscriptionSecret")
		return ctrl.Result{}, err
	}

	logger.Info("Building VM object")
	vm := r.createVM(cloudInitData(*subscriptionSecret, imageBuilder.Spec.SshKey), metav1.ObjectMeta{
		Name:      imageBuilder.Name,
		Namespace: imageBuilder.Namespace,
		Labels:    labels,
	})
	logger.Info("Creating VM object")
	if err := r.Create(ctx, &vm); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Image Builder VM already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create Image Builder VM")
			return ctrl.Result{}, err
		}
	}

	service := r.createVMService(metav1.ObjectMeta{
		Name:      imageBuilder.Name,
		Namespace: imageBuilder.Namespace,
		Labels:    labels,
	}, servicePort)
	logger.Info("Creating service object")
	if err := r.Create(ctx, &service); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Image Builder VM already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create Image Builder Service")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *ImageBuilderReconciler) createVMService(objectMeta metav1.ObjectMeta, port int32) corev1.Service {
	service := corev1.Service{
		ObjectMeta: objectMeta,
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Protocol: "TCP",
					Port:     int32(port),
				},
			},
			Selector: map[string]string{
				"vm.kubevirt.io/name": objectMeta.Name,
			},
		},
	}

	return service
}

func cloudInitData(subSecret corev1.Secret, key string) string {
	type templateValues struct {
		Username string
		Password string
		SshKey   string
	}
	values := templateValues{
		Username: string(subSecret.Data["username"]),
		Password: string(subSecret.Data["password"]),
		SshKey:   key,
	}
	const configTemplate = `#cloud-config
user: cloud-user
password: redhat
chpasswd: { expire: False }
{{if ne .SshKey ""}}ssh_authorized_keys:
  - {{.SshKey}}
{{end}}rh_subscription:
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
	config.Execute(&renderedTemplate, values)
	return strings.ReplaceAll(renderedTemplate.String(), "\t", "    ")
}

func (r *ImageBuilderReconciler) createVM(cloudInitData string, objectMeta metav1.ObjectMeta) kubevirt.VirtualMachine {
	rootVolumeName := fmt.Sprintf("%s-vm-volume", objectMeta.Name)

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
		"eci": fmt.Sprintf("%s-image-builder", objectMeta.Name),
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
		ObjectMeta: objectMeta,
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
