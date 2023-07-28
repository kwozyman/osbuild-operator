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

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"bytes"
	"fmt"
	"text/template"

	"github.com/go-logr/logr"
	osbuildv1alpha1 "github.com/kwozyman/osbuild-operator/api/v1alpha1"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
)

const ubiImage = "registry.access.redhat.com/ubi9:latest"
const utilsImage = "quay.io/cgament/composer-cli"
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

[customizations]
installation_device = "{{ .InstallationDevice }}"

[customizations.fdo]
manufacturing_server_url = "{{ .FdoManufacturingServerUrl }}"
diun_pub_key_insecure = "true"
`

const waitScriptTemplate = `#!/bin/bash
compose_id=$(jq '.build_id' -r /workspace/shared-volume/$(params.blueprintName)/compose.json)
while /usr/bin/curl "${api}/compose/queue" --silent | jq -r '.run[].id' | grep ${compose_id} || usr/bin/curl "${api}/compose/queue" --silent | jq -r '.new[].id' | grep ${compose_id}; do sleep 30; done
/usr/bin/curl "${api}/compose/failed" --silent | jq -r '.failed[].id' | grep "${compose_id}" && echo "Compose ${compose_id} failed!" && exit 1
/usr/bin/curl "${api}/compose/finished" --silent | jq -r --arg id "${composer_id}" '.finished[] | select (.id==$id)'
`

// ImageBuilderImageReconciler reconciles a ImageBuilderImage object
type ImageBuilderImageReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=osbuild.rh-ecosystem-edge.io,resources=imagebuilderimages,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=osbuild.rh-ecosystem-edge.io,resources=imagebuilderimages/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=osbuild.rh-ecosystem-edge.io,resources=imagebuilderimages/finalizers,verbs=update

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

	// get new ImageBuilderImage object
	var imageBuilderImage osbuildv1alpha1.ImageBuilderImage
	if err := r.Get(ctx, req.NamespacedName, &imageBuilderImage); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Resource not found, must have been deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Unable to fetch ImageBuilderImage")
		return ctrl.Result{}, client.IgnoreNotFound(err)
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageSpec.Name,
			Namespace: imageBuilderImage.Namespace,
		},
		Data: map[string]string{
			imageSpec.Name: renderTemplateFromSpec(blueprintTemplate, imageSpec),
		},
	}
	blueprintIsoConfigMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-iso", imageSpec.Name),
			Namespace: imageBuilderImage.Namespace,
		},
		Data: map[string]string{
			fmt.Sprintf("%s-iso", imageSpec.Name): renderTemplateFromSpec(blueprintIsoTemplate, imageSpec),
		},
	}
	if err := r.Create(ctx, &blueprintConfigMap); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Main blueprint configmap already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create main blueprint")
			return ctrl.Result{}, err
		}
	}
	if err := r.Create(ctx, &blueprintIsoConfigMap); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Iso blueprint configmap already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create iso blueprint")
			return ctrl.Result{}, err
		}
	}

	//persistentVolume used for inter-task communication
	var pvcName string
	if imageBuilderImage.Spec.SharedVolume == "" {
		logger.Info("No PVC name specified, using default")
		pvcName = fmt.Sprintf("%s-data", req.Name)
	} else {
		pvcName = imageBuilderImage.Spec.SharedVolume
	}
	/*
		sharedVolume := corev1.PersistentVolumeClaim{}
		if err := r.Get(ctx, client.ObjectKey{
			Name: pvcName,
		}, &sharedVolume); err != nil {
			logger.Info("Could not get PVC. Creating")
			//TODO: create volume
			}
	*/

	// generate and create pipeline tasks
	apiUrl := fmt.Sprintf("http://%s.%s:%v/api/v1",
		imageService.Name, imageService.Namespace, imageService.Spec.Ports[0].Port)

	prepareTask := r.PrepareSharedVolumeTask(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-prepare-volume", req.Name),
		Namespace: req.Namespace,
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
	}, apiUrl)
	if err := r.Create(ctx, &downloadTask); err != nil {
		if errors.IsAlreadyExists(err) {
			logger.Info("Download and extract task already exists, skipping creation")
		} else {
			logger.Error(err, "Could not create download task")
			return ctrl.Result{}, err
		}
	}

	// create commit pipeline and pipelinerun
	imagePipeline := r.ImagePipeline(metav1.ObjectMeta{
		Name:      fmt.Sprintf("%s-pipeline", req.Name),
		Namespace: req.Namespace,
	}, []tektonv1.Task{prepareTask, commitTask, downloadTask}, logger)
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

	return ctrl.Result{}, nil
}

func (r *ImageBuilderImageReconciler) DownloadExtractCommitTask(objectMeta metav1.ObjectMeta, apiEndpoint string) tektonv1.Task {
	task := tektonv1.Task{
		ObjectMeta: objectMeta,
		Spec: tektonv1.TaskSpec{
			Workspaces: []tektonv1.WorkspaceDeclaration{
				{
					Name: "shared-volume",
				},
				{
					Name: "blueprints",
				},
			},
			Params: tektonv1.ParamSpecs{
				tektonv1.ParamSpec{
					Name: "blueprintName",
				},
				{
					Name: "apiEndpoint",
				},
			},
			Steps: []tektonv1.Step{
				{
					Name:  "download-commit",
					Image: utilsImage,
					Command: []string{
						"/usr/bin/bash", "-c",
						"/usr/bin/curl $(params.apiEndpoint)/compose/image/$(/usr/bin/jq -r '.build_id' /workspace/shared-volume/$(params.blueprintName)/compose.json) --output /workspace/shared-volume/$(params.blueprintName)/edge-commit.tar --verbose",
					},
				},
			},
		},
	}
	return task
}

func (r *ImageBuilderImageReconciler) PrepareSharedVolumeTask(objectMeta metav1.ObjectMeta) tektonv1.Task {
	task := tektonv1.Task{
		ObjectMeta: objectMeta,
		Spec: tektonv1.TaskSpec{
			Workspaces: []tektonv1.WorkspaceDeclaration{
				{
					Name: "shared-volume",
				},
				{
					Name: "blueprints",
				},
			},
			Params: tektonv1.ParamSpecs{
				{
					Name: "blueprintName",
				},
				{
					Name: "apiEndpoint",
				},
			},
			Steps: []tektonv1.Step{
				{
					Name:  "create-directory",
					Image: ubiImage,
					Command: []string{
						"/bin/bash", "-c",
						"mkdir -p \"/workspace/shared-volume/$(params.blueprintName)\" && echo Using blueprint $(params.blueprintName)",
					},
				},
				{
					Name:  "remove-compose-file",
					Image: ubiImage,
					Command: []string{
						"/bin/bash", "-c",
						"rm -fv \"workspace/shared-volume/$(params.blueprintName)\"/compose.json",
					},
				},
			},
		},
	}
	return task
}

func (r *ImageBuilderImageReconciler) CommitTask(objectMeta metav1.ObjectMeta) tektonv1.Task {
	task := tektonv1.Task{
		ObjectMeta: objectMeta,
		Spec: tektonv1.TaskSpec{
			Workspaces: []tektonv1.WorkspaceDeclaration{
				{
					Name: "blueprints",
				},
				{
					Name: "shared-volume",
				},
			},
			Params: tektonv1.ParamSpecs{
				{
					Name: "blueprintName",
				},
				{
					Name: "apiEndpoint",
				},
			},
			Steps: []tektonv1.Step{
				{
					Name:  "push-blueprint",
					Image: ubiImage,
					Command: []string{
						"/usr/bin/curl", "-H", "Content-Type: text/x-toml", "--data-binary", "@/workspace/blueprints/$(params.blueprintName)", "$(params.apiEndpoint)/blueprints/new",
						"--silent",
					},
				},
				{
					Name:  "start-compose",
					Image: ubiImage,
					Command: []string{
						"/usr/bin/curl", "-H", "Content-Type: application/json",
						"--data", "{\"blueprint_name\":\"$(params.blueprintName)\",\"compose_type\":\"edge-commit\"}",
						"$(params.apiEndpoint)/compose",
						"--output", "/workspace/shared-volume/$(params.blueprintName)/compose.json",
						"--silent",
					},
				},
				{
					Name:   "wait-for-finish",
					Image:  utilsImage,
					Script: waitScriptTemplate,
					Env: []corev1.EnvVar{
						{
							Name:  "api",
							Value: "$(params.apiEndpoint)",
						},
					},
				},
			},
		},
	}
	return task
}

func (r *ImageBuilderImageReconciler) ImagePipeline(objectMeta metav1.ObjectMeta, tasks []tektonv1.Task, logger logr.Logger) tektonv1.Pipeline {
	pipelinetasks := []tektonv1.PipelineTask{}
	previousTask := tektonv1.Task{}
	for counter, task := range tasks {
		currentTask := tektonv1.PipelineTask{
			TaskRef: &tektonv1.TaskRef{
				Name: task.Name,
			},
			Name: task.Name,
			Workspaces: []tektonv1.WorkspacePipelineTaskBinding{
				{
					Name: "blueprints",
				},
				{
					Name: "shared-volume",
				},
			},
			Params: tektonv1.Params{
				tektonv1.Param{
					Name: "blueprintName",
					Value: tektonv1.ParamValue{
						Type:      "string",
						StringVal: "$(params.blueprintName)",
					},
				},
				{
					Name: "apiEndpoint",
					Value: tektonv1.ParamValue{
						Type:      "string",
						StringVal: "$(params.apiEndpoint)",
					},
				},
			},
		}
		if counter == 0 {
			previousTask = task
		} else {
			currentTask.RunAfter = []string{previousTask.Name}
			previousTask = task
		}
		pipelinetasks = append(pipelinetasks, currentTask)
	}
	pipeline := tektonv1.Pipeline{
		ObjectMeta: objectMeta,
		Spec: tektonv1.PipelineSpec{
			Workspaces: []tektonv1.PipelineWorkspaceDeclaration{
				{
					Name: "blueprints",
				},
				{
					Name: "shared-volume",
				},
			},
			Tasks: pipelinetasks,
			Params: tektonv1.ParamSpecs{
				{
					Name: "blueprintName",
				},
				{
					Name: "apiEndpoint",
				},
			},
		},
	}
	return pipeline
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
