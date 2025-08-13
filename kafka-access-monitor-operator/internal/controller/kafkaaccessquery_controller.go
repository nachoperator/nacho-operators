package controller

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	accessmonitorv1alpha1 "github.com/masmovil/mm-monorepo/pkg/runtime/operators/kafka-access-monitor-operator/api/v1alpha1"
	"github.com/masmovil/mm-monorepo/pkg/runtime/operators/kafka-access-monitor-operator/internal/ai"
	"github.com/masmovil/mm-monorepo/pkg/runtime/operators/kafka-access-monitor-operator/internal/discovery"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

//+kubebuilder:rbac:groups=accessmonitor.masorange.com,resources=kafkaaccessqueries,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=accessmonitor.masorange.com,resources=kafkaaccessqueries/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=accessmonitor.masorange.com,resources=kafkaaccessqueries/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

type KafkaAccessQueryReconciler struct {
	client.Client
	Log               logr.Logger
	Scheme            *runtime.Scheme
	PodFinder         *discovery.PodFinder
	ConfigMapFinder   *discovery.ConfigMapFinder
	SecretFinder      *discovery.SecretFinder
	AuthnEnricher     *discovery.AuthnEnricher
	DiagnosticsFinder *discovery.DiagnosticsFinder
	AIAnalyzer        ai.Analyzer
	KafkaParser       *discovery.DynamicParser
	SlackReporter     *discovery.SlackReporter
	BrokerLogFinders  map[string]*discovery.BrokerLogFinder
	MetricsFinder     discovery.BrokerMetricsFinder
}

// AIResult encapsulates the result of a single AI analysis call.
type AIResult struct {
	PodName           string
	ProblemMessage    string
	Analysis          string
	BrokerLogSnippets []string
	Err               error
}

// Reconcile is the main reconciliation loop.
func (r *KafkaAccessQueryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("kafkaaccessquery", req.NamespacedName)
	log.Info("Starting reconciliation")

	// 1. Fetch the KafkaAccessQuery instance
	var kafkaAccessQuery accessmonitorv1alpha1.KafkaAccessQuery
	if err := r.Get(ctx, req.NamespacedName, &kafkaAccessQuery); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("KafkaAccessQuery resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get KafkaAccessQuery")
		return ctrl.Result{}, err
	}

	// AI activation control logic
	shouldTriggerAI := true
	if kafkaAccessQuery.Spec.AIAnalysisEnabled != nil && !*kafkaAccessQuery.Spec.AIAnalysisEnabled {
		log.Info("AI analysis is disabled by spec, skipping.")
		shouldTriggerAI = false
	}

	originalStatus := kafkaAccessQuery.Status.DeepCopy()
	currentStatus := kafkaAccessQuery.Status.DeepCopy()
	currentStatus.AccessingPods = []accessmonitorv1alpha1.PodAccessInfo{}

	// Load the central authn ConfigMap once per reconciliation cycle.
	env := os.Getenv("EXECUTION_ENVIRONMENT")
	if env == "" {
		log.Info("EXECUTION_ENVIRONMENT not set, defaulting to 'sta' for authn lookup")
		env = "sta"
	}
	tenantMap, err := r.AuthnEnricher.LoadAndGetTenantMap(ctx, env)
	if err != nil {
		log.Error(err, "Cannot perform enrichment, tenants and roles will be empty for this cycle")
	}

	// Parse the error scan interval from the spec
	scanDuration, err := time.ParseDuration(kafkaAccessQuery.Spec.ErrorLogScanInterval)
	if err != nil || kafkaAccessQuery.Spec.ErrorLogScanInterval == "" {
		log.Info("Invalid or missing ErrorLogScanInterval, defaulting to 10m", "value", kafkaAccessQuery.Spec.ErrorLogScanInterval)
		scanDuration = 10 * time.Minute
	}

	// 2. Use PodFinder to get the pods
	log.Info("Listing pods using discovery PodFinder...")
	podList, err := r.PodFinder.ListPods(ctx, kafkaAccessQuery.Spec.NamespaceSelector, kafkaAccessQuery.Spec.PodSelector)
	if err != nil {
		log.Error(err, "Failed to list pods via PodFinder")
		return ctrl.Result{}, err
	}
	totalPodsScanned := len(podList.Items)
	log.Info("Total pods retrieved for inspection", "count", totalPodsScanned)

	var foundPodsAccessInfo []accessmonitorv1alpha1.PodAccessInfo
	log.Info("--- CHECKPOINT 2: Starting pod inspection loop ---")

	// 3. Inspect each Pod
	for _, pod := range podList.Items {
		if !pod.ObjectMeta.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning {
			continue
		}

		serviceAccountEmail, _ := r.SecretFinder.FindAuthnServiceAccountEmail(ctx, &pod)
		preferredUserName := generatePreferredUserName(serviceAccountEmail)
		authorizedTenantsForThisPod := tenantMap[preferredUserName]

		problemDetails, _ := r.DiagnosticsFinder.FetchRecentProblems(ctx, &pod, scanDuration)
		const maxProblemsToStore = 1 // Limit the number of problems to store per pod to avoid excessive data
		if len(problemDetails) > maxProblemsToStore {
			problemDetails = problemDetails[:maxProblemsToStore]
		}

		// 3a. Search in Environment Variables for all containers
		allContainers := append(pod.Spec.Containers, pod.Spec.InitContainers...)
		for _, container := range allContainers {
			endpoints, source := r.checkContainerEnvVars(ctx, log, &pod, &container, &kafkaAccessQuery.Spec)
			if len(endpoints) > 0 {
				podInfo := accessmonitorv1alpha1.PodAccessInfo{
					PodName:                 pod.Name,
					Namespace:               pod.Namespace,
					ServiceAccountEmail:     serviceAccountEmail,
					PreferredUserName:       preferredUserName,
					AuthorizedTenants:       authorizedTenantsForThisPod,
					ContainerName:           container.Name + getContainerType(&pod, container.Name),
					DetectedBrokerEndpoints: endpoints,
					SourceOfEndpoint:        source,
					ClientType:              "Unknown",
					RecentProblems:          problemDetails,
				}
				foundPodsAccessInfo = append(foundPodsAccessInfo, podInfo)
			}
		}

		// 3b. Search in ALL Mounted ConfigMaps using the Dynamic Parser
		mountedCMs, err := r.ConfigMapFinder.GetMountedConfigMaps(ctx, &pod)
		if err != nil {
			log.Error(err, "Could not inspect mounted ConfigMaps for pod", "pod", pod.Name)
			continue
		}

		for _, cm := range mountedCMs {
			if cm.Name == "cadence" {
				log.Info("Skipping parsing of hardcoded excluded ConfigMap", "configMapName", cm.Name, "pod", pod.Name)
				continue
			}
			for key, content := range cm.Data {
				if !strings.HasSuffix(key, ".yaml") && !strings.HasSuffix(key, ".yml") {
					continue
				}

				if details, found := r.KafkaParser.Parse(content); found {
					finalGroupID := details.GroupID
					if finalGroupID == "" && details.ConsumerGroupSuffix != "" {
						finalGroupID = preferredUserName + details.ConsumerGroupSuffix
					}
					podInfo := accessmonitorv1alpha1.PodAccessInfo{
						PodName:                 pod.Name,
						Namespace:               pod.Namespace,
						ServiceAccountEmail:     serviceAccountEmail,
						PreferredUserName:       preferredUserName,
						AuthorizedTenants:       authorizedTenantsForThisPod,
						ClientType:              details.ClientType,
						RecentProblems:          problemDetails,
						DetectedBrokerEndpoints: details.Brokers,
						SchemaRegistryURL:       details.SchemaRegistryURL,
						ContainerName:           fmt.Sprintf("(mounted-cm:%s)", cm.Name),
						SourceOfEndpoint:        fmt.Sprintf("configmap:%s, key:%s", cm.Name, key),
						ClientID:                details.ClientID,
						GroupID:                 finalGroupID,
						AutoOffsetReset:         details.AutoOffsetReset,
						AutoCommit:              details.AutoCommit,
						MaxPollRecords:          details.MaxPollRecords,
						MaxPollIntervalMs:       details.MaxPollIntervalMs,
						Topics:                  details.Topics,
					}
					foundPodsAccessInfo = append(foundPodsAccessInfo, podInfo)
				}
			}
		}
	}
	log.Info("--- CHECKPOINT 3: FINISHED pod inspection loop ---")

	currentStatus.AccessingPods = foundPodsAccessInfo
	currentStatus.TotalPodsScanned = totalPodsScanned
	currentStatus.TotalPodsWithKafkaAccess = countUniquePods(foundPodsAccessInfo)
	currentStatus.ObservedGeneration = kafkaAccessQuery.Generation
	currentStatus.LastUpdateTime = &metav1.Time{Time: time.Now()}

	var podsToReport []accessmonitorv1alpha1.PodAccessInfo
	for _, podInfo := range currentStatus.AccessingPods {
		if len(podInfo.RecentProblems) > 0 {
			podsToReport = append(podsToReport, podInfo)
		}
	}
	log.Info("Filtered pods with problems for reporting", "totalFound", len(currentStatus.AccessingPods), "reportingCount", len(podsToReport))

	// This loop enriches the problems with broker metrics before AI analysis.
	for i, podInfo := range currentStatus.AccessingPods {
		if len(podInfo.RecentProblems) == 0 {
			continue // jump pods that do not have recent problems
		}
		if r.MetricsFinder == nil {
			log.Info("MetricsFinder not configured, skipping metrics fetch.")
			continue
		}

		clusterKey := getClusterKeyFromEndpoint(podInfo.DetectedBrokerEndpoints)
		if clusterKey == "unknown" {
			continue
		}

		brokerPods, err := r.findBrokerPodsForCluster(ctx, clusterKey, env)
		if err != nil {
			log.Error(err, "Could not find broker pods for cluster, skipping metrics fetch", "clusterKey", clusterKey)
			continue
		}

		// First, iterate over each pod with problems to get the timestamp.
		for j, problem := range podInfo.RecentProblems {
			var allMetricsSummaries []string
			problemTimestamp := time.Now()

			if !problem.LastSeenTimestamp.IsZero() {
				problemTimestamp = problem.LastSeenTimestamp.Time
			}

			// Then, for this problem, get the metrics of all brokers at that time.
			for _, brokerPod := range brokerPods {
				log.Info("Fetching broker metrics for problem correlation.", "pod", podInfo.PodName, "brokerPod", brokerPod.Name)

				brokerMetrics, err := r.MetricsFinder.FetchBrokerMetrics(ctx, brokerPod.Name, problemTimestamp)
				if err != nil {
					log.Error(err, "Failed to fetch broker metrics", "brokerPod", brokerPod.Name)
					continue // continue to the next broker if this one fails
				}

				if brokerMetrics != nil {
					log.Info("Fetched broker metrics successfully", "brokerPod", brokerPod.Name, "cpu", brokerMetrics.CPUUtilization)
					currentStatus.AccessingPods[i].RecentProblems[j].BrokerCPUUtilization = fmt.Sprintf("%f", brokerMetrics.CPUUtilization)
					currentStatus.AccessingPods[i].RecentProblems[j].BrokerNetworkBytesIn = fmt.Sprintf("%f", brokerMetrics.NetworkBytesIn)
					currentStatus.AccessingPods[i].RecentProblems[j].BrokerUnderReplicated = brokerMetrics.UnderReplicated

					metricsSummary := fmt.Sprintf(
						" - Broker Health (pod: %s): CPU: %.2f%%, URPartitions: %d",
						brokerPod.Name,
						brokerMetrics.CPUUtilization*100,
						brokerMetrics.UnderReplicated,
					)
					allMetricsSummaries = append(allMetricsSummaries, metricsSummary)
				}
			}
			// Join all the metrics summaries into a single string and add it to the problem message.
			if len(allMetricsSummaries) > 0 {
				finalSummary := strings.Join(allMetricsSummaries, "; ")
				currentStatus.AccessingPods[i].RecentProblems[j].Message += finalSummary
			}
		}
	}

	// Refactored AI Analysis Logic (conditional)
	if shouldTriggerAI {
		log.Info("Checking for new problems that require AI analysis...")
		type problemToAnalyze struct {
			podInfo accessmonitorv1alpha1.PodAccessInfo
			problem accessmonitorv1alpha1.ProblemDetail
		}
		var problemsToAnalyze []problemToAnalyze
		for _, podInfo := range currentStatus.AccessingPods {
			for _, problem := range podInfo.RecentProblems {
				if problem.AIAnalysis == "" {
					problemsToAnalyze = append(problemsToAnalyze, problemToAnalyze{podInfo, problem})
				}
			}
		}

		if len(problemsToAnalyze) > 0 {
			log.Info("Found new problems requiring AI analysis", "count", len(problemsToAnalyze))

			log.Info("Pre-fetching broker logs for all relevant clients...")
			brokerLogsCache := make(map[string][]string) // Cache key: "clusterKey-clientIdentifier"
			for _, pta := range problemsToAnalyze {
				clientIdentifier := coalesce(pta.podInfo.GroupID, pta.podInfo.ClientID)
				if clientIdentifier == "" {
					continue
				}

				clusterKey := getClusterKeyFromEndpoint(pta.podInfo.DetectedBrokerEndpoints)
				if clusterKey == "unknown" {
					log.Info("Cannot determine cluster for log fetching", "pod", pta.podInfo.PodName)
					continue
				}

				cacheKey := fmt.Sprintf("%s-%s", clusterKey, clientIdentifier)
				if _, exists := brokerLogsCache[cacheKey]; exists {
					continue
				}

				if finder, ok := r.BrokerLogFinders[clusterKey]; ok {
					logs, err := finder.FindRelevantBrokerLogs(ctx, clientIdentifier, 15*time.Minute)
					if err != nil {
						log.Error(err, "Failed to pre-fetch broker logs", "client", clientIdentifier, "cluster", clusterKey)
						brokerLogsCache[cacheKey] = []string{fmt.Sprintf("Error fetching broker logs: %v", err)}
					} else {
						log.Info("Successfully pre-fetched broker logs", "client", clientIdentifier, "cluster", clusterKey, "logCount", len(logs))
						brokerLogsCache[cacheKey] = logs
					}
				} else {
					log.Info("No BrokerLogFinder configured for cluster key, cannot fetch logs", "key", clusterKey)
					brokerLogsCache[cacheKey] = []string{fmt.Sprintf("Log fetching skipped: No log finder configured for cluster '%s'", clusterKey)}
				}
			}

			resultsChan := make(chan AIResult, len(problemsToAnalyze))
			var wg sync.WaitGroup
			wg.Add(len(problemsToAnalyze))

			for _, pta := range problemsToAnalyze {
				go func(podInfo accessmonitorv1alpha1.PodAccessInfo, problem accessmonitorv1alpha1.ProblemDetail) {
					defer wg.Done()
					routineCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
					defer cancel()

					clientIdentifier := coalesce(podInfo.GroupID, podInfo.ClientID)
					var brokerLogs []string
					if clientIdentifier != "" {
						clusterKey := getClusterKeyFromEndpoint(podInfo.DetectedBrokerEndpoints)
						cacheKey := fmt.Sprintf("%s-%s", clusterKey, clientIdentifier)
						brokerLogs = brokerLogsCache[cacheKey]
					}

					problemWithLogs := problem
					problemWithLogs.BrokerLogSnippets = brokerLogs

					analysis, err := r.AIAnalyzer.Analyze(routineCtx, podInfo, problemWithLogs)
					resultsChan <- AIResult{
						PodName:           podInfo.PodName,
						ProblemMessage:    problem.Message,
						Analysis:          analysis,
						BrokerLogSnippets: brokerLogs,
						Err:               err,
					}
				}(pta.podInfo, pta.problem)
			}

			go func() {
				wg.Wait()
				close(resultsChan)
			}()

			allAIResults := make(map[string]string)
			allBrokerLogs := make(map[string][]string)

			for result := range resultsChan {
				key := result.PodName + result.ProblemMessage
				if result.Err != nil {
					log.Error(result.Err, "AI analysis failed for pod", "pod", result.PodName)
					allAIResults[key] = fmt.Sprintf("AI analysis failed: %v", result.Err)
				} else {
					log.Info("Successfully received AI analysis", "pod", result.PodName)
					allAIResults[key] = result.Analysis
				}
				allBrokerLogs[key] = result.BrokerLogSnippets
			}

			for i, pInfo := range currentStatus.AccessingPods {
				for j, pDetail := range pInfo.RecentProblems {
					key := pInfo.PodName + pDetail.Message
					if analysis, found := allAIResults[key]; found {
						currentStatus.AccessingPods[i].RecentProblems[j].AIAnalysis = analysis
						currentStatus.AccessingPods[i].RecentProblems[j].BrokerLogSnippets = allBrokerLogs[key]
					}
				}
			}
		}
	} else {
		log.Info("AI analysis is disabled by spec, skipping analysis.")
	}

	// Final Status Update
	if !reflect.DeepEqual(originalStatus, currentStatus) {
		log.Info("Status has changed, attempting to update.")
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var latestQuery accessmonitorv1alpha1.KafkaAccessQuery
			if getErr := r.Get(ctx, req.NamespacedName, &latestQuery); getErr != nil {
				return getErr
			}
			latestQuery.Status = *currentStatus
			return r.Status().Update(ctx, &latestQuery)
		}); err != nil {
			log.Error(err, "Failed to update KafkaAccessQuery status after all processing")
			return ctrl.Result{}, err
		}
		log.Info("Successfully updated KafkaAccessQuery status with all results.")

		if len(podsToReport) > 0 {
			log.Info("Status updated, sending report to Slack for pods with problems...")
			if err := r.SlackReporter.SendReport(ctx, podsToReport); err != nil {
				log.Error(err, "Failed to send report to Slack")
			}
		}

	} else {
		log.Info("KafkaAccessQuery status has not changed.")
	}
	// BQ Logging Logic
	log.Info("Logging discovered events for BigQuery sink")
	for _, podInfo := range currentStatus.AccessingPods {
		for _, problem := range podInfo.RecentProblems {
			log.Info("KafkaAccessEvent",
				"pod_namespace", podInfo.Namespace,
				"pod_name", podInfo.PodName,
				"container_name", podInfo.ContainerName,
				"service_account_email", podInfo.ServiceAccountEmail,
				"preferred_user_name", podInfo.PreferredUserName,
				"authorized_tenants", strings.Join(podInfo.AuthorizedTenants, ","),
				"client_type", podInfo.ClientType,
				"client_id", podInfo.ClientID,
				"group_id", podInfo.GroupID,
				"detected_broker_endpoints", strings.Join(podInfo.DetectedBrokerEndpoints, ","),
				"schema_registry_url", podInfo.SchemaRegistryURL,
				"source_of_endpoint", podInfo.SourceOfEndpoint,
				"topics", getTopicsAsString(podInfo.Topics),
				"problem_type", problem.Type,
				"problem_message", problem.Message,
				"problem_count", problem.Count,
				"ai_analysis", problem.AIAnalysis,
				"broker_log_snippets", strings.Join(problem.BrokerLogSnippets, "\n---\n"),
				"auto_offset_reset", podInfo.AutoOffsetReset,
				"auto_commit", podInfo.AutoCommit,
				"max_poll_records", podInfo.MaxPollRecords,
				"max_poll_interval_ms", podInfo.MaxPollIntervalMs,
				"broker_cpu_utilization", problem.BrokerCPUUtilization,
				"broker_network_bytes_in", problem.BrokerNetworkBytesIn,
				"broker_under_replicated", problem.BrokerUnderReplicated,
			)
		}
	}

	// Requeue Logic
	requeueDuration, _ := time.ParseDuration(kafkaAccessQuery.Spec.RequeueInterval)
	if requeueDuration == 0 {
		requeueDuration = time.Hour * 1
	}
	log.Info("Reconciliation finished, will requeue after duration.", "requeueAfter", requeueDuration.String())
	return ctrl.Result{RequeueAfter: requeueDuration}, nil
}

// checkContainerEnvVars resolves and checks environment variables for Kafka broker endpoints.
func (r *KafkaAccessQueryReconciler) checkContainerEnvVars(ctx context.Context, log logr.Logger, pod *corev1.Pod, container *corev1.Container, spec *accessmonitorv1alpha1.KafkaAccessQuerySpec) ([]string, string) {
	var detectedEndpoints []string
	var sourceDetails []string
	searchVars := make(map[string]bool)
	for _, v := range spec.EnvironmentVariableNames {
		searchVars[v] = true
	}
	if len(searchVars) == 0 {
		return nil, ""
	}
	for _, envVar := range container.Env {
		if !searchVars[envVar.Name] {
			continue
		}
		var value string
		var found bool
		if envVar.Value != "" {
			value = envVar.Value
			found = true
			sourceDetails = append(sourceDetails, fmt.Sprintf("env:%s", envVar.Name))
		} else if envVar.ValueFrom != nil && envVar.ValueFrom.ConfigMapKeyRef != nil {
			cmRef := envVar.ValueFrom.ConfigMapKeyRef
			log.V(1).Info("Resolving EnvVar from ConfigMapKeyRef", "pod", pod.Name, "container", container.Name, "configMap", cmRef.Name, "key", cmRef.Key)
			cm := &corev1.ConfigMap{}
			err := r.Client.Get(ctx, types.NamespacedName{Name: cmRef.Name, Namespace: pod.Namespace}, cm)
			if err != nil {
				log.Error(err, "Failed to get ConfigMap specified in envVar", "configMap", cmRef.Name)
				continue
			}
			if val, ok := cm.Data[cmRef.Key]; ok {
				value = val
				found = true
				sourceDetails = append(sourceDetails, fmt.Sprintf("env-from-cm:%s:%s", cmRef.Name, cmRef.Key))
			}
		}
		if found && value != "" {
			endpoints := strings.Split(value, ",")
			for _, ep := range endpoints {
				detectedEndpoints = append(detectedEndpoints, strings.TrimSpace(ep))
			}
		}
	}
	return detectedEndpoints, strings.Join(sourceDetails, "; ")
}

// countUniquePods counts the number of unique pods in the list of PodAccessInfo.
func countUniquePods(podAccessInfos []accessmonitorv1alpha1.PodAccessInfo) int {
	if len(podAccessInfos) == 0 {
		return 0
	}
	uniquePods := make(map[string]bool)
	for _, info := range podAccessInfos {
		podIdentifier := fmt.Sprintf("%s/%s", info.Namespace, info.PodName)
		uniquePods[podIdentifier] = true
	}
	return len(uniquePods)
}

// generatePreferredUserName transforms an email address into a simplified username.
func generatePreferredUserName(email string) string {
	if email == "" {
		return ""
	}
	lower := strings.ToLower(email)
	return strings.ReplaceAll(lower, "@", "-")
}

// SetupWithManager sets up the controller with the Manager.
func (r *KafkaAccessQueryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		return err
	}

	r.PodFinder = discovery.NewPodFinder(mgr.GetClient())
	r.ConfigMapFinder = discovery.NewConfigMapFinder(mgr.GetClient())
	r.SecretFinder = discovery.NewSecretFinder(mgr.GetClient())
	r.AuthnEnricher = discovery.NewAuthnEnricher(mgr.GetClient())
	r.DiagnosticsFinder = discovery.NewDiagnosticsFinder(mgr.GetClient(), clientset)
	r.Log = mgr.GetLogger().WithName("reconciler")

	return ctrl.NewControllerManagedBy(mgr).
		For(&accessmonitorv1alpha1.KafkaAccessQuery{}).
		Complete(r)
}

func getContainerType(pod *corev1.Pod, containerName string) string {
	for _, initContainer := range pod.Spec.InitContainers {
		if initContainer.Name == containerName {
			return " (init)"
		}
	}
	return ""
}

func getTopicsAsString(topics []accessmonitorv1alpha1.TopicInfo) string {
	if len(topics) == 0 {
		return ""
	}
	var topicSubjects []string
	for _, t := range topics {
		topicSubjects = append(topicSubjects, t.Subject)
	}
	return strings.Join(topicSubjects, ",")
}

// coalesce returns the first non-empty string.
func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// getClusterKeyFromEndpoint extracts the cluster identifier (bss, cdr, corp) from broker URLs.
func getClusterKeyFromEndpoint(endpoints []string) string {
	if len(endpoints) == 0 {
		return "unknown"
	}
	endpoint := strings.ToLower(endpoints[0])

	if strings.Contains(endpoint, ".bss.kafka") {
		return "bss"
	}
	if strings.Contains(endpoint, ".cdr.kafka") {
		return "cdr"
	}
	if strings.Contains(endpoint, ".corp.kafka") {
		return "corp"
	}
	return "unknown"
}

// findBrokerPodsForCluster busca todos los pods de broker para un clúster y entorno específicos.
func (r *KafkaAccessQueryReconciler) findBrokerPodsForCluster(ctx context.Context, clusterKey, env string) ([]corev1.Pod, error) {
	brokerNamespace := fmt.Sprintf("kafka-%s-%s", clusterKey, env)

	labelSelector := labels.SelectorFromSet(map[string]string{
		"strimzi.io/cluster":     fmt.Sprintf("kafka-%s-%s", clusterKey, env),
		"strimzi.io/broker-role": "true",
	})

	podList := &corev1.PodList{}
	listOpts := &client.ListOptions{
		Namespace:     brokerNamespace,
		LabelSelector: labelSelector,
	}

	if err := r.List(ctx, podList, listOpts); err != nil {
		return nil, fmt.Errorf("failed to list broker pods with labels %s in namespace %s: %w", labelSelector.String(), brokerNamespace, err)
	}

	return podList.Items, nil
}
