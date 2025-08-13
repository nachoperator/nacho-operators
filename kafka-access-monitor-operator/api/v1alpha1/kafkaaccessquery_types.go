package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KafkaAccessQuerySpec defines the desired state of KafkaAccessQuery
// This is where users will specify what the operator should monitor or query.
type KafkaAccessQuerySpec struct {
	// INSERT YOUR SPEC FIELDS HERE
	// Important: Run "make" to regenerate code after modifying this file

	// NamespaceSelector specifies the namespaces to analyze for Kafka access.
	// If empty or nil, all namespaces where the operator has RBAC permissions might be considered,
	// or a default set depending on controller logic.
	// +kubebuilder:validation:Optional
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// PodSelector specifies labels to filter Pods within the selected namespaces.
	// If empty or nil, all pods in the selected namespaces might be considered.
	// +kubebuilder:validation:Optional
	// +optional
	PodSelector *metav1.LabelSelector `json:"podSelector,omitempty"`

	// KafkaBrokerPattern is a string pattern (e.g., part of a hostname or a regex if your controller supports it)
	// to search for in configurations to identify Kafka broker endpoints.
	// +kubebuilder:validation:Optional
	// +optional
	KafkaBrokerPattern string `json:"kafkaBrokerPattern,omitempty"`

	// ConfigMapKeys specifies ConfigMaps and keys within their data to scan for broker endpoints.
	// The map key is the ConfigMap name, and the value is a list of data keys within that ConfigMap.
	// The operator will look for these ConfigMaps in the same namespace as the Pods being inspected,
	// or you might decide on a central namespace for common ConfigMaps.
	// +kubebuilder:validation:Optional
	// +optional
	ConfigMapKeys map[string][]string `json:"configMapKeys,omitempty"`

	// EnvironmentVariableNames lists common environment variable names that might contain Kafka bootstrap servers.
	// Example: ["KAFKA_BOOTSTRAP_SERVERS", "BOOTSTRAP_SERVERS", "KAFKA_URL"]
	// +kubebuilder:validation:Optional
	// +optional
	EnvironmentVariableNames []string `json:"environmentVariableNames,omitempty"`

	// RequeueInterval specifies the desired interval between periodic reconciliations/queries.
	// Use Go duration format string (e.g., "1h", "30m", "5m").
	// If empty or invalid, the controller will use a default internal value.
	// +kubebuilder:validation:Optional
	// +optional
	RequeueInterval string `json:"requeueInterval,omitempty"`

	// ErrorLogScanInterval defines how far back in time to scan for errors in pod logs.
	// Use Go duration format string (e.g., "1h", "10m"). Defaults to a controller-defined value if empty.
	// +optional
	ErrorLogScanInterval string `json:"errorLogScanInterval,omitempty"`

	// AIAnalysisEnabled controls whether the AI analysis feature is active.
	// If not specified, the controller will default to true.
	// +optional
	AIAnalysisEnabled *bool `json:"aiAnalysisEnabled,omitempty"`
}

// TopicInfo holds structured data about a Kafka topic.
type TopicInfo struct {
	Cluster string `json:"cluster,omitempty"`
	Domain  string `json:"domain,omitempty"`
	Subject string `json:"subject,omitempty"`
	Version string `json:"version,omitempty"`
}

// ProblemDetail holds structured information about a single unique problem.
type ProblemDetail struct {
	// Type is the severity of the problem (e.g., Warning, Error).
	// +kubebuilder:validation:Enum=Warning;Error;Panic
	Type string `json:"type"`

	// Message is the unique, deduplicated error or warning message.
	Message string `json:"message"`

	// Count is how many times this exact problem has occurred.
	Count int32 `json:"count"`

	// LastSeenTimestamp is the timestamp of the last time this problem was observed.
	LastSeenTimestamp metav1.Time `json:"lastSeenTimestamp"`

	// BrokerCPUUtilization stores the CPU usage of the associated broker as a string.
	// +optional
	BrokerCPUUtilization string `json:"brokerCpuUtilization,omitempty"`

	// BrokerNetworkBytesIn stores the network traffic received by the broker as a string.
	// +optional
	BrokerNetworkBytesIn string `json:"brokerNetworkBytesIn,omitempty"`

	// BrokerUnderReplicated stores the number of under-replicated partitions of the broker.
	// +optional
	BrokerUnderReplicated int64 `json:"brokerUnderReplicated,omitempty"`
	
	// AIAnalysis stores the analysis received from the AI service.
	// +optional
	AIAnalysis string `json:"aiAnalysis,omitempty"`

	// BrokerLogSnippets contains relevant log entries found on the Kafka broker
	// that might be related to this problem.
	// +optional
	BrokerLogSnippets []string `json:"brokerLogSnippets,omitempty"`
}

// PodAccessInfo holds the details of a detected Kafka access from a specific pod/container.
// PodAccessInfo holds the details of a detected Kafka access from a specific pod/container.
// The order of fields here determines the desired display order in the CR's status YAML.
type PodAccessInfo struct {
	// Name of the pod.
	// +required
	PodName string `json:"podName"`

	// Namespace of the pod where Kafka access was detected.
	// +required
	Namespace string `json:"namespace"`

	// ClientType indicates if the client is inferred to be a "Consumer" or "Producer".
	// +optional
	ClientType string `json:"clientType,omitempty"`

	// DetectedBrokerEndpoints is a list of Kafka broker addresses found.
	// +optional
	DetectedBrokerEndpoints []string `json:"detectedBrokerEndpoints,omitempty"`

	// SchemaRegistryURL is the URL for the Kafka Schema Registry, if found.
	// +optional
	SchemaRegistryURL string `json:"schemaRegistryUrl,omitempty"`

	// Name of the container. Can be a regular or init container.
	// +optional
	ContainerName string `json:"containerName,omitempty"`

	// SourceOfEndpoint describes how the broker endpoint was found (e.g., env, configmap-key).
	// +optional
	SourceOfEndpoint string `json:"sourceOfEndpoint,omitempty"`

	// ClientID is the Kafka producer client ID, if found.
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// GroupID is the Kafka consumer group ID, if found.
	// +optional
	GroupID string `json:"groupId,omitempty"`

	// AutoOffsetReset policy for consumers (e.g., "earliest", "latest"), if found.
	// +optional
	AutoOffsetReset string `json:"autoOffsetReset,omitempty"`

	// AutoCommit indicates if the consumer is configured to auto-commit offsets.
	// +optional
	AutoCommit *bool `json:"autoCommit,omitempty"`

	// AdditionalConfig holds other relevant Kafka properties found in the configuration.
	// +optional
	AdditionalConfig map[string]string `json:"additionalConfig,omitempty"`

	// Topics is a list of structured topic information associated with this client.
	// +optional
	Topics []TopicInfo `json:"topics,omitempty"`

	// +optional
	MaxPollRecords int `json:"maxPollRecords,omitempty"`
	// +optional
	MaxPollIntervalMs int `json:"maxPollIntervalMs,omitempty"`

	// ServiceAccountEmail is the client_email extracted from the related authn secret.
	// +optional
	ServiceAccountEmail string `json:"serviceAccountEmail,omitempty"`

	// PreferredUserName  is a transformed version of the ServiceAccountEmail,
	// +optional
	PreferredUserName string `json:"preferredUserName,omitempty"`

	// AuthorizedTenants is the list of tenant organizations this service account has access to.
	// +optional
	AuthorizedTenants []string `json:"authorizedTenants,omitempty"`

	// AuthorizedRoles is the list of roles associated with this service account.
	// (Placeholder for now, as it's not in the current CM example)
	// +optional
	AuthorizedRoles []string `json:"authorizedRoles,omitempty"`

	// RecentProblems is a list of recent Warning events or ERROR/WARN log entries found for this pod.
	// +optional
	RecentProblems []ProblemDetail `json:"recentProblems,omitempty"`
}

// KafkaAccessQueryStatus defines the observed state of KafkaAccessQuery
// This is where your operator will write the results of its findings.
type KafkaAccessQueryStatus struct {
	// INSERT YOUR STATUS FIELD HERE
	// Important: Run "make" to regenerate code after modifying this file

	// LastUpdateTime is the last time the access query was performed and status updated.
	// +kubebuilder:validation:Optional
	// +optional
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`
	// ObservedGeneration reflects the .metadata.generation that the status was based on.
	// +kubebuilder:validation:Optional
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// AccessingPods lists the pods found to be configured to access Kafka brokers based on the spec.
	// +kubebuilder:validation:Optional
	// +optional
	AccessingPods []PodAccessInfo `json:"accessingPods,omitempty"`

	// TotalPodsScanned is the total number of pods scanned in the last reconciliation.
	// +kubebuilder:validation:Optional
	// +optional
	TotalPodsScanned int `json:"totalPodsScanned,omitempty"`

	// TotalPodsWithKafkaAccess is the total number of pods found with Kafka access configurations.
	// +kubebuilder:validation:Optional
	// +optional
	TotalPodsWithKafkaAccess int `json:"totalPodsWithKafkaAccess,omitempty"`

	// Conditions represent the latest available observations of the KafkaAccessQuery's state.
	// +kubebuilder:validation:Optional
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
// You can add more print columns if desired for `kubectl get kafkaaccessquery`
//+kubebuilder:printcolumn:name="Pods Scanned",type="integer",JSONPath=".status.totalPodsScanned"
//+kubebuilder:printcolumn:name="Found Access",type="integer",JSONPath=".status.totalPodsWithKafkaAccess"
//+kubebuilder:printcolumn:name="Last Update",type="date",JSONPath=".status.lastUpdateTime"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// KafkaAccessQuery is the Schema for the kafkaaccessqueries API
type KafkaAccessQuery struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KafkaAccessQuerySpec   `json:"spec,omitempty"`
	Status KafkaAccessQueryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// KafkaAccessQueryList contains a list of KafkaAccessQuery
type KafkaAccessQueryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KafkaAccessQuery `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KafkaAccessQuery{}, &KafkaAccessQueryList{})
}
