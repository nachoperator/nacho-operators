package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeploymentPlan define un solo componente a ser gestionado por el orquestador.
type DeploymentPlan struct {
	// Name of the Deployment or StatefulSet to manage.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// List of other deployments/statefulsets that must be completed before this one.
	DependsOn []string `json:"dependsOn,omitempty"`
}

// UpgradeOrchestratorSpec defines the desired state of UpgradeOrchestrator.
type UpgradeOrchestratorSpec struct {
	// Deployment plan with dependencies between components.
	// +kubebuilder:validation:MinItems=1
	Deployments []DeploymentPlan `json:"deployments"`
}

// WorkloadStatus representa el estado de un único workload durante el upgrade.
type WorkloadStatus struct {
	// Name of the Deployment or StatefulSet.
	Name string `json:"name"`

	// Status representa el estado actual del workload en el proceso de upgrade.
	// Posibles valores: PendingDependencies, ReadyToMigrate, Migrating, Completed.
	// +kubebuilder:validation:Enum=PendingDependencies;ReadyToMigrate;Migrating;Completed
	Status string `json:"status"`

	// Message proporciona información adicional sobre el estado, como errores.
	Message string `json:"message,omitempty"`
}

// UpgradeOrchestratorStatus defines the observed state of UpgradeOrchestrator.
type UpgradeOrchestratorStatus struct {
	// State indica el estado global del orquestador.
	// "NoUpgrade": Estado normal y estable.
	// "AwaitingUpgradeConfirmation": Se ha detectado un cordon, esperando confirmación de upgrade.
	// "UpgradeInProgress": Upgrade confirmado y en progreso.
	// +kubebuilder:validation:Enum=NoUpgrade;AwaitingUpgradeConfirmation;UpgradeInProgress
	State string `json:"state,omitempty"`

	// TargetVersion es la versión de Kubernetes a la que se está actualizando el clúster.
	// Solo se rellena cuando el estado es "UpgradeInProgress".
	TargetVersion string `json:"targetVersion,omitempty"`

	// LastTransitionTime registra cuándo ocurrió la última transición de estado. Útil para timeouts.
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`

	// WorkloadStatuses contiene el estado detallado de cada workload gestionado.
	WorkloadStatuses []WorkloadStatus `json:"workloadStatuses,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state",description="The current state of the upgrade orchestrator."
//+kubebuilder:printcolumn:name="Target Version",type="string",JSONPath=".status.targetVersion",description="The target Kubernetes version for the upgrade."

// UpgradeOrchestrator is the Schema for the UpgradeOrchestrator API
type UpgradeOrchestrator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UpgradeOrchestratorSpec   `json:"spec,omitempty"`
	Status UpgradeOrchestratorStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// UpgradeOrchestratorList contains a list of UpgradeOrchestrator
type UpgradeOrchestratorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UpgradeOrchestrator `json:"items"`
}

func init() {
	SchemeBuilder.Register(&UpgradeOrchestrator{}, &UpgradeOrchestratorList{})
}
