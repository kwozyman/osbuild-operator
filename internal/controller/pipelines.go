package controller

import (
	"fmt"

	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *ImageBuilderImageReconciler) DownloadTask(objectMeta metav1.ObjectMeta, compose_file string, destination string) tektonv1.Task {
	task := tektonv1.Task{
		ObjectMeta: objectMeta,
		Spec: tektonv1.TaskSpec{
			Workspaces: r.PipelineWorkspaces,
			Params:     r.PipelineParams,
			Steps: []tektonv1.Step{
				{
					Name:  "download",
					Image: utilsImage,
					Command: []string{
						"/usr/bin/bash", "-c",
						fmt.Sprintf("/usr/bin/curl $(params.apiEndpoint)/compose/image/$(/usr/bin/jq -r '.build_id' \"/workspace/shared-volume/$(params.blueprintName)/%s\") --output \"/workspace/shared-volume/$(params.blueprintName)/%s\" --verbose", compose_file, destination),
					},
				},
			},
		},
	}
	return task
}

func (r *ImageBuilderImageReconciler) DownloadExtractCommitTask(objectMeta metav1.ObjectMeta, apiEndpoint string) tektonv1.Task {
	task := tektonv1.Task{
		ObjectMeta: objectMeta,
		Spec: tektonv1.TaskSpec{
			Workspaces: r.PipelineWorkspaces,
			Params:     r.PipelineParams,
			Steps: []tektonv1.Step{
				{
					Name:  "download-commit",
					Image: utilsImage,
					Command: []string{
						"/usr/bin/bash", "-c",
						"/usr/bin/curl $(params.apiEndpoint)/compose/image/$(/usr/bin/jq -r '.build_id' /workspace/shared-volume/$(params.blueprintName)/compose.json) --output /workspace/shared-volume/$(params.blueprintName)/edge-commit.tar --verbose",
					},
				},
				{
					Name:  "extract-commit",
					Image: ubiImage,
					Command: []string{
						"/usr/bin/bash", "-c",
						"tar xf /workspace/shared-volume/$(params.blueprintName)/edge-commit.tar -C /workspace/shared-volume/$(params.blueprintName)/",
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
		TypeMeta: metav1.TypeMeta{
			Kind:       "Task",
			APIVersion: "tekton.dev/v1",
		},
		Spec: tektonv1.TaskSpec{
			Workspaces: r.PipelineWorkspaces,
			Params:     r.PipelineParams,
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
			Workspaces: r.PipelineWorkspaces,
			Params:     r.PipelineParams,
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
						{
							Name:  "compose_file",
							Value: "compose.json",
						},
					},
				},
			},
		},
	}
	return task
}

func (r *ImageBuilderImageReconciler) IsoComposeTask(objectMeta metav1.ObjectMeta) tektonv1.Task {
	task := tektonv1.Task{
		ObjectMeta: objectMeta,
		Spec: tektonv1.TaskSpec{
			Workspaces: r.PipelineWorkspaces,
			Params:     r.PipelineParams,
			Steps: []tektonv1.Step{
				{
					Name:  "push-blueprint",
					Image: ubiImage,
					Command: []string{
						"/usr/bin/curl", "-H", "Content-Type: text/x-toml", "--data-binary", "@/workspace/blueprints/$(params.blueprintName)-iso", "$(params.apiEndpoint)/blueprints/new", "--silent",
					},
				},
				{
					Name:  "compose-json",
					Image: ubiImage,
					Command: []string{
						"/usr/bin/bash", "-c",
						`echo "{\"blueprint_name\":\"image-iso\",\"compose_type\":\"edge-simplified-installer\",\"ostree\":{\"ref\":\"rhel/9/x86_64/edge\",\"url\":\"http://$(getent hosts | grep pipeline | awk '{print $1}'):8000/repo\"}}" > /workspace/shared-volume/$(params.blueprintName)/ostree-compose.json`,
					},
				},
				{
					Name:  "start-compose",
					Image: ubiImage,
					Command: []string{
						"/usr/bin/curl", "-H", "Content-Type: application/json", "--data-binary", "@workspace/shared-volume/$(params.blueprintName)/ostree-compose.json", "$(params.apiEndpoint)/compose", "--verbose", "--output", "/workspace/shared-volume/$(params.blueprintName)/compose-iso.json",
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
						{
							Name:  "compose_file",
							Value: "compose-iso.json",
						},
					},
				},
			},
			Sidecars: []tektonv1.Sidecar{
				{
					Name:  "ostree-webserver",
					Image: ubiImage,
					Command: []string{
						"/usr/bin/bash", "-c",
						"/usr/bin/python3 -m http.server --directory /workspace/shared-volume/$(params.blueprintName) 8000 > /dev/null",
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							Exec: &corev1.ExecAction{
								Command: []string{
									"/usr/bin/curl", "http://127.0.0.1:8000/repo",
								},
							},
						},
					},
				},
			},
		},
	}
	return task
}

func (r *ImageBuilderImageReconciler) ImagePipeline(objectMeta metav1.ObjectMeta, tasks []tektonv1.Task) tektonv1.Pipeline {
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
			Tasks:  pipelinetasks,
			Params: r.PipelineParams,
		},
	}
	return pipeline
}
