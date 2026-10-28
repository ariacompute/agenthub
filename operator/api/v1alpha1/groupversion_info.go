// Package v1alpha1 contains the AgentHub custom resource definition for the
// agenthub kubernetes operator. It mirrors every AGENTHUB_* knob the
// agenthub-server binary accepts (see internal/config/config.go) so the
// controller can drive a fully configured deployment from a declarative CR.
//
// +kubebuilder:object:generate=true
// +groupName=agenthub.ai
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group/version the AgentHub CR belongs to.
	GroupVersion = schema.GroupVersion{Group: "agenthub.ai", Version: "v1alpha1"}

	// SchemeBuilder registers the AgentHub types with a runtime.Scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the AgentHub types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(&AgentHub{}, &AgentHubList{})
}

// GroupName is the API group string, exposed for RBAC/label helpers.
const GroupName = "agenthub.ai"
