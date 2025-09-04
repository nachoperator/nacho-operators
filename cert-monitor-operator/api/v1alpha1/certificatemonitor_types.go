/*
Copyright 2025.

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
	// "time" // time.Time ya no se usa directamente aquí, metav1.Time sí
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// CertificateMonitorSpec defines the desired state of CertificateMonitor
type CertificateMonitorSpec struct {
	// NamespaceSelector specifies the namespaces to analyze. If empty, all namespaces are analyzed.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// SecretSelector specifies the labels to filter Secrets containing mTLS certificates.
	// +optional
	SecretSelector *metav1.LabelSelector `json:"secretSelector,omitempty"`

	// RenewalThresholdDays is the number of days before expiration to consider a certificate as "near renewal".
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=365
	RenewalThresholdDays int32 `json:"renewalThresholdDays"`

	// NotificationEndpoint is the endpoint to send alerts (e.g., Opsgenie webhook).
	// +optional
	NotificationEndpoint string `json:"notificationEndpoint,omitempty"`

	// EmailRecipients is a list of email addresses to send notifications.
	// +optional
	EmailRecipients []string `json:"emailRecipients,omitempty"`

	// RequeueInterval specifies the desired interval between periodic reconciliations.
	// Use Go duration format string (e.g., "12h", "30m", "24h").
	// If empty or invalid, a default value (12h) will be used by the controller.
	// +optional
	RequeueInterval string `json:"requeueInterval,omitempty"`

	// Opsgenie contains Opsgenie notification configuration.
	// +kubebuilder:validation:Optional
	Opsgenie *OpsgenieConfig `json:"opsgenie,omitempty"`

	// NotificationNamespaceSelector specifies a label selector for namespaces.
	// Notifications for Webhook (Slack) and Opsgenie will be restricted to
	// certificates found within namespaces matching this selector.
	// If this field is nil or the selector is empty, it may affect how these notifications are scoped
	// (e.g., potentially all matching namespaces if the selector is empty like {}, or skipped if nil).
	// The controller logic will determine precise behavior for nil/empty cases.
	// Email notifications remain governed by the global NamespaceSelector.
	// +kubebuilder:validation:Optional
	// +optional
	NotificationNamespaceSelector *metav1.LabelSelector `json:"notificationNamespaceSelector,omitempty"`
}

// MonitoredCertificateInfo contains details about a specific monitored certificate.
// +k8s:openapi-gen=true
type MonitoredCertificateInfo struct {
	// Namespace where the Secret containing the certificate is located.
	Namespace string `json:"namespace"`

	// Name of the Secret containing the certificate.
	SecretName string `json:"secretName"`

	// Key within Secret.Data containing the certificate (e.g., tls.crt, ca.crt).
	SecretKey string `json:"secretKey"`

	// Common Name (CN) from the certificate's Subject.
	// +optional
	SubjectCN string `json:"subjectCN,omitempty"`

	// Common Name (CN) from the certificate's Issuer.
	// +optional
	IssuerCN string `json:"issuerCN,omitempty"`

	// Expiration date and time of the certificate.
	ExpiresAt metav1.Time `json:"expiresAt"` // Use metav1.Time for k8s compatibility

	// Approximate remaining days until expiration.
	DaysUntilExpiration int `json:"daysUntilExpiration"`

	// Indicates if the certificate is already expired.
	// +optional
	IsExpired bool `json:"isExpired,omitempty"`

	// Indicates if the certificate is within the renewal threshold (and not already expired).
	// +optional
	IsNearExpiration bool `json:"isNearExpiration,omitempty"`
}

// CertificateMonitorStatus defines the observed state of CertificateMonitor
type CertificateMonitorStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Conditions represent the latest available observations of an object's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`

	// MonitoredCertificates contains the list of certificates found and their status.
	// +optional
	MonitoredCertificates []MonitoredCertificateInfo `json:"monitoredCertificates,omitempty"`

	// LastChecked marks the last time the reconciler ran its check.
	// +optional
	LastChecked metav1.Time `json:"lastChecked,omitempty"`

	// LastNotificationTime records when the last aggregated notification was sent.
	// Used for cooldown logic.
	// +optional
	LastNotificationTime *metav1.Time `json:"lastNotificationTime,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Threshold",type="integer",JSONPath=".spec.renewalThresholdDays"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// CertificateMonitor is the Schema for the certificatemonitors API
type CertificateMonitor struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CertificateMonitorSpec   `json:"spec,omitempty"`
	Status CertificateMonitorStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// CertificateMonitorList contains a list of CertificateMonitor
type CertificateMonitorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CertificateMonitor `json:"items"`
}

// OpsgenieAPIKeySecretRef defines a reference to a Kubernetes Secret
// containing the Opsgenie API key.
type OpsgenieAPIKeySecretRef struct {
	// Name is the name of the Kubernetes Secret.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace is the namespace of the Kubernetes Secret.
	// Defaults to the CertificateMonitor's namespace if not specified.
	// +kubebuilder:validation:Optional
	Namespace string `json:"namespace,omitempty"`

	// Key is the key within the Secret's Data map that contains the API key.
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

// OpsgenieConfig defines the configuration for Opsgenie notifications.
type OpsgenieConfig struct {
	// Enabled specifies whether Opsgenie notifications are enabled.
	// +kubebuilder:validation:Optional
	Enabled bool `json:"enabled,omitempty"`

	// APIKeySecretRef is a reference to the Kubernetes Secret
	// containing the Opsgenie API key.
	// Required if Opsgenie is enabled.
	// +kubebuilder:validation:Optional
	APIKeySecretRef *OpsgenieAPIKeySecretRef `json:"apiKeySecretRef,omitempty"`

	// Region specifies the Opsgenie API region (e.g., "EU", "US").
	// If empty, defaults to the US region API endpoint.
	// +kubebuilder:validation:Optional
	Region string `json:"region,omitempty"`

	// Priority for the Opsgenie alert (e.g., P1, P2, P3, P4, P5). Defaults to P3.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:="P3"
	Priority string `json:"priority,omitempty"`
}

func init() {
	SchemeBuilder.Register(&CertificateMonitor{}, &CertificateMonitorList{})
}
