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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ImageBuilderImageSpec defines the desired state of ImageBuilderImage
type ImageBuilderImageSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Name                      string `json:"name,omitempty"`
	UserName                  string `json:"userName,omitempty"`
	SshKey                    string `json:"sshKey,omitempty"`
	InstallationDevice        string `json:"installationDevice,omitempty"`
	FdoManufacturingServerUrl string `json:"fdoManufacturingServerUrl,omitempty"`
	BlueprintTemplate         string `json:"blueprintTemplate,omitempty"`
	BlueprintIsoTemplate      string `json:"blueprintIsoTemplate,omitempty"`
	ImageBuilder              string `json:"imageBuilder,omitempty"`
	SharedVolume              string `json:"persistentVolumeName,omitempty"`
	IsoTarget                 string `json:"isoTarget,omitempty"`
}

// ImageBuilderImageStatus defines the observed state of ImageBuilderImage
type ImageBuilderImageStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// ImageBuilderImage is the Schema for the imagebuilderimages API
type ImageBuilderImage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ImageBuilderImageSpec   `json:"spec,omitempty"`
	Status ImageBuilderImageStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ImageBuilderImageList contains a list of ImageBuilderImage
type ImageBuilderImageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImageBuilderImage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ImageBuilderImage{}, &ImageBuilderImageList{})
}
