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
	"context"

	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"bytes"
	"fmt"
	"text/template"

	osbuildv1alpha1 "github.com/kwozyman/osbuild-operator/api/v1alpha1"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
)

const ubiImage = "registry.access.redhat.com/ubi9:latest"
const utilsImage = "quay.io/cgament/composer-cli"
const imageBuilderImageLabel = "osbuild-operator-image"
const defaultIsoTarget = "edge-installer"
const defaultBlueprintTemplate = `name = "{{ .Name }}"
version = "0.0.1"
modules = []
groups = []

[[customizations.sshkey]]
user = "{{ .UserName }}"
key = "{{ .SshKey }}"
`

const defaultIsoBlueprintTemplate = `name = "{{ .Name }}-iso"
version = "0.0.1"
modules = []
groups = []
distro = ""

{{ if eq $.IsoTarget "edge-simplified-installer" }}
[customizations]
installation_device = "{{ .InstallationDevice }}"

[customizations.fdo]
manufacturing_server_url = "{{ .FdoManufacturingServerUrl }}"
diun_pub_key_insecure = "true"
{{ end }}
`

const waitScriptTemplate = `#!/bin/bash
compose_id=$(jq '.build_id' -r /workspace/shared-volume/$(params.blueprintName)/${compose_file})
while /usr/bin/curl "${api}/compose/queue" --silent | jq -r '.run[].id' | grep ${compose_id} || usr/bin/curl "${api}/compose/queue" --silent | jq -r '.new[].id' | grep ${compose_id}; do sleep 30; done
/usr/bin/curl "${api}/compose/failed" --silent | jq -r '.failed[].id' | grep "${compose_id}" && echo "Compose ${compose_id} failed!" && exit 1
/usr/bin/curl "${api}/compose/finished" --silent | jq -r --arg id "${composer_id}" '.finished[] | select (.id==$id)'
`

// ImageBuilderImageReconciler reconciles a ImageBuilderImage object
type ImageBuilderImageReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	PipelineWorkspaces []tektonv1.WorkspaceDeclaration
	PipelineParams     tektonv1.ParamSpecs
	IsoTarget          string
}

//+kubebuilder:rbac:groups=osbuild.rh-ecosystem-edge.io,resources=imagebuilderimages,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=osbuild.rh-ecosystem-edge.io,resources=imagebuilderimages/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=osbuild.rh-ecosystem-edge.io,resources=imagebuilderimages/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;delete
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;delete;update
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;create;delete;watch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;create;delete
//+kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;create;delete
//+kubebuilder:rbac:groups=tekton.dev,resources=tasks,verbs=get;list;create;delete
//+kubebuilder:rbac:groups=tekton.dev,resources=pipelines,verbs=get;list;create;delete
//+kubebuilder:rbac:groups=tekton.dev,resources=pipelineruns,verbs=get;list;create;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ImageBuilderImage object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.15.0/pkg/reconcile
func (r *ImageBuilderImageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	labels := map[string]string{
		imageBuilderImageLabel: req.Name,
	}

	// get new ImageBuilderImage object
	var imageBuilderImage osbuildv1alpha1.ImageBuilderImage
	if err := r.Get(ctx, req.NamespacedName, &imageBuilderImage); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Resource not found, must have been deleted")
			if err := DeleteAllObjectsWithLabel(ctx, r.Client, "PipelineRun", "tekton.dev/v1", imageBuilderImageLabel, req.Name); err != nil {
				return ctrl.Result{}, err
			}
			if err := DeleteAllObjectsWithLabel(ctx, r.Client, "Pipeline", "tekton.dev/v1", imageBuilderImageLabel, req.Name); err != nil {
				return ctrl.Result{}, err
			}
			if err := DeleteAllObjectsWithLabel(ctx, r.Client, "Task", "tekton.dev/v1", imageBuilderImageLabel, req.Name); err != nil {
				return ctrl.Result{}, err
			}
			if err := DeleteAllObjectsWithLabel(ctx, r.Client, "ConfigMap", "v1", imageBuilderImageLabel, req.Name); err != nil {
				return ctrl.Result{}, err
			}
			if err := DeleteAllObjectsWithLabel(ctx, r.Client, "Route", "route.openshift.io/v1", imageBuilderImageLabel, req.Name); err != nil {
				return ctrl.Result{}, err
			}
			if err := DeleteAllObjectsWithLabel(ctx, r.Client, "Service", "v1", imageBuilderImageLabel, req.Name); err != nil {
				return ctrl.Result{}, err
			}
			if err := DeleteAllObjectsWithLabel(ctx, r.Client, "Deployment", "apps/v1", imageBuilderImageLabel, req.Name); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Unable to fetch ImageBuilderImage")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// installer compose type
	if imageBuilderImage.Spec.IsoTarget == "" {
		logger.Info("No installer target specified, using default")
		imageBuilderImage.Spec.IsoTarget = defaultIsoTarget
		r.IsoTarget = defaultIsoTarget
	} else {
		r.IsoTarget = defaultIsoTarget
	}

	// to what ImageBuilder are we tying this?
	var imageBuilder osbuildv1alpha1.ImageBuilder
	if imageBuilderImage.Spec.ImageBuilder == "" {
		logger.Info("ImageBuilder instance is not specified in ImageBuilderImage, trying to find default")
		u := &osbuildv1alpha1.ImageBuilderList{}
		u.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "osbuild.rh-ecosystem-edge.io",
			Kind:    "ImageBuilder",
			Version: "v1alpha1",
		})
		if err := r.List(ctx, u); err != nil {
			logger.Error(err, "Could not get ImageBuilder list")
			return ctrl.Result{}, err
		}
		if len(u.Items) != 1 {
			logger.Error(nil, "No suitable ImageBuilder found or too many")
			return ctrl.Result{}, nil
		}
		imageBuilder = u.Items[0]
		logger.Info(fmt.Sprintf("Using %s ImageBuilder", imageBuilder.Name))
	} else {
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: req.Namespace,
			Name:      req.Name,
		}, &imageBuilder); err != nil {
			logger.Error(err, "Could not get ImageBuilder")
			return ctrl.Result{}, err
		}
	}

	// the ImageBuilder Service we are communicating through
	imageService := corev1.Service{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: imageBuilder.Namespace,
		Name:      imageBuilder.Name,
	}, &imageService); err != nil {
		logger.Error(err, "Could not get image service")
	}

	// fill defaults to this spec, do not modify the main object
	imageSpec := imageBuilderImage.Spec
	if imageSpec.Name == "" {
		imageSpec.Name = imageBuilderImage.Name
	}

	// templates used for blueprints
	var blueprintTemplate string
	if imageBuilderImage.Spec.BlueprintTemplate == "" {
		logger.Info("No defined spec.blueprintTemplate, using default")
		blueprintTemplate = defaultBlueprintTemplate
	} else {
		blueprintTemplate = imageBuilderImage.Spec.BlueprintTemplate
	}
	var blueprintIsoTemplate string
	if imageBuilderImage.Spec.BlueprintIsoTemplate == "" {
		logger.Info("No defined spec.blueprintIsoTemplate, using default")
		blueprintIsoTemplate = defaultIsoBlueprintTemplate
	} else {
		blueprintIsoTemplate = imageBuilderImage.Spec.BlueprintIsoTemplate
	}

	// store blueprints in configmaps
	blueprintConfigMap := corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-blueprint", imageSpec.Name),
			Namespace: imageBuilderImage.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			imageSpec.Name:                        renderTemplateFromSpec(blueprintTemplate, imageSpec),
			fmt.Sprintf("%s-iso", imageSpec.Name): renderTemplateFromSpec(blueprintIsoTemplate, imageSpec),
		},
	}

	if err := CreateOrUpdateObject(ctx, r.Client, &blueprintConfigMap); err != nil {
		return ctrl.Result{}, err
	}

	//persistentVolume used for inter-task communication
	var pvcName string
	if imageBuilderImage.Spec.SharedVolume == "" {
		logger.Info("No PVC name specified, using default")
		pvcName = fmt.Sprintf("%s-data", req.Name)
	} else {
		pvcName = imageBuilderImage.Spec.SharedVolume
	}

	// common pipeline environment
	r.PipelineWorkspaces = []tektonv1.WorkspaceDeclaration{
		{
			Name: "shared-volume",
		},
		{
			Name: "blueprints",
		},
	}
	r.PipelineParams = tektonv1.ParamSpecs{
		tektonv1.ParamSpec{
			Name: "blueprintName",
		},
		{
			Name: "apiEndpoint",
		},
	}

	// generate and create pipeline tasks
	apiUrl := fmt.Sprintf("http://%s.%s:%v/api/v1",
		imageService.Name, imageService.Namespace, imageService.Spec.Ports[0].Port)

	prepareTask := r.PrepareSharedVolumeTask(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-prepare-volume", req.Name),
		Namespace: req.Namespace,
		Labels:    labels,
	})
	if err := r.Create(ctx, &prepareTask); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("prepare volume task already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create task preparevolume")
			return ctrl.Result{}, err
		}
	}

	commitTask := r.CommitTask(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-generate-commit", req.Name),
		Namespace: req.Namespace,
		Labels:    labels,
	})
	if err := r.Create(ctx, &commitTask); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Commit task already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create commit task")
			return ctrl.Result{}, err
		}
	}

	downloadTask := r.DownloadExtractCommitTask(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-download-extract-commit", req.Name),
		Namespace: req.Namespace,
		Labels:    labels,
	})
	if err := r.Create(ctx, &downloadTask); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Download and extract task already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create download task")
			return ctrl.Result{}, err
		}
	}

	isoComposeTask := r.IsoComposeTask(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-iso-compose", req.Name),
		Namespace: req.Namespace,
		Labels:    labels,
	})
	if err := r.Create(ctx, &isoComposeTask); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Iso compose task already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create isocompose task")
			return ctrl.Result{}, err
		}
	}
	isoDownloadTask := r.DownloadTask(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-iso-download", req.Name),
		Namespace: req.Namespace,
		Labels:    labels,
	}, "compose-iso.json", "installer.iso")
	if err := r.Create(ctx, &isoDownloadTask); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Iso download task already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create isodownload task")
			return ctrl.Result{}, err
		}
	}
	// create commit pipeline and pipelinerun
	imagePipeline := r.ImagePipeline(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-pipeline", req.Name),
		Namespace: req.Namespace,
		Labels:    labels,
	}, []tektonv1.Task{prepareTask, commitTask, downloadTask, isoComposeTask, isoDownloadTask})
	if err := r.Create(ctx, &imagePipeline); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Image generation pipeline already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create image pipeline")
			return ctrl.Result{}, err
		}
	}
	imagePipelineRun := tektonv1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pipeline-run", req.Name),
			Namespace: req.Namespace,
			Labels:    labels,
		},
		Spec: tektonv1.PipelineRunSpec{
			PipelineRef: &tektonv1.PipelineRef{
				Name: imagePipeline.Name,
			},
			Workspaces: []tektonv1.WorkspaceBinding{
				{
					Name: "blueprints",
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: blueprintConfigMap.Name,
						},
					},
				},
				{
					Name: "shared-volume",
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				},
			},
			Params: tektonv1.Params{
				{
					Name: "blueprintName",
					Value: tektonv1.ParamValue{
						Type:      "string",
						StringVal: req.Name,
					},
				},
				{
					Name: "apiEndpoint",
					Value: tektonv1.ParamValue{
						Type:      "string",
						StringVal: apiUrl,
					},
				},
			},
			Status: tektonv1.PipelineRunSpecStatus("PipelineRunPending"),
		},
	}

	if err := r.Create(ctx, &imagePipelineRun); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Image generation pipeline run already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create commit pipelinerun")
			return ctrl.Result{}, err
		}
	}

	// webserver deployment
	webDeployment := r.WebDeployment(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-web", req.Name),
		Namespace: req.Namespace,
		Labels:    labels,
	}, pvcName, req.Name)
	webService := r.WebService(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-service", req.Name),
		Namespace: req.Namespace,
		Labels:    labels,
	}, webDeployment.Name)
	webRoute := r.WebRoute(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-route", req.Name),
		Namespace: req.Namespace,
		Labels:    labels,
	}, webService.Name)

	if err := r.Create(ctx, &webDeployment); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Deployment already exists")
		} else {
			logger.Error(err, "Could not create deplyoment")
			return ctrl.Result{}, err
		}
	}
	if err := r.Create(ctx, &webService); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Service already exists")
		} else {
			logger.Error(err, "Service could not be created")
			return ctrl.Result{}, err
		}
	}
	if err := r.Create(ctx, &webRoute); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Route already exists")
		} else {
			logger.Error(err, "Could not create route")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *ImageBuilderImageReconciler) WebRoute(objectMeta metav1.ObjectMeta, serviceName string) routev1.Route {
	route := routev1.Route{
		ObjectMeta: objectMeta,
		Spec: routev1.RouteSpec{
			To: routev1.RouteTargetReference{
				Kind: "Service",
				Name: serviceName,
			},
			Port: &routev1.RoutePort{
				TargetPort: intstr.FromInt(8080),
			},
		},
	}
	return route
}

func (r *ImageBuilderImageReconciler) WebService(objectMeta metav1.ObjectMeta, appName string) corev1.Service {
	service := corev1.Service{
		ObjectMeta: objectMeta,
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:       8089,
					TargetPort: intstr.FromInt(8080),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Selector: map[string]string{
				"app": appName,
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}
	return service
}

func (r *ImageBuilderImageReconciler) WebDeployment(objectMeta metav1.ObjectMeta, pvcName string, imageName string) appsv1.Deployment {
	appName := objectMeta.Name
	var replicas int32 = 1
	webDeployment := appsv1.Deployment{
		ObjectMeta: objectMeta,
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": appName,
				},
			},
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": appName,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "httpd",
							Image: "registry.redhat.io/rhel9/httpd-24:latest",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 8080,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data-pv",
									MountPath: "/var/www/html/",
									SubPath:   imageName,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "data-pv",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvcName,
								},
							},
						},
					},
				},
			},
		},
	}
	return webDeployment
}

func renderTemplateFromSpec(blueprint string, values osbuildv1alpha1.ImageBuilderImageSpec) string {
	var render bytes.Buffer
	templ, err := template.New("template").Parse(blueprint)
	if err != nil {
		panic(err)
	}
	templ.Execute(&render, values)
	return render.String()
}

// SetupWithManager sets up the controller with the Manager.
func (r *ImageBuilderImageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&osbuildv1alpha1.ImageBuilderImage{}).
		Complete(r)
}
