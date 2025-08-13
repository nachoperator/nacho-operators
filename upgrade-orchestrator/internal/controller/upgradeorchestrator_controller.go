package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/Masterminds/semver/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	upgraderv1alpha1 "github.com/nachoperator/nacho-operators/upgrade-orchestrator/api/v1alpha1"
)

// Constantes
const (
	StateNoUpgrade                   = "NoUpgrade"
	StateAwaitingUpgradeConfirmation = "AwaitingUpgradeConfirmation"
	StateUpgradeInProgress           = "UpgradeInProgress"
	UpgradeConfirmationTimeout       = 5 * time.Minute
	UpgradeTaintKey                  = "upgrade-orchestrator/status"
	UpgradeTaintValue                = "upgrading"
	UpgradeNodeLabelKey              = "upgrade-orchestrator/target-version"
	OriginalPDBSpecAnnotation        = "upgrade.nachoperator.io/original-pdb-spec"
	ManagedByAnnotation              = "upgrade.nachoperator.io/managed-by"
)

type UpgradeOrchestratorReconciler struct {
	client.Client
}

// Reconcile
func (r *UpgradeOrchestratorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("upgradeOrchestrator", req.NamespacedName)
	logger.Info("🚀 Starting reconciliation loop")

	var orchestrator upgraderv1alpha1.UpgradeOrchestrator
	if err := r.Get(ctx, req.NamespacedName, &orchestrator); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if orchestrator.Status.State == "" {
		orchestrator.Status.State = StateNoUpgrade
		now := metav1.Now()
		orchestrator.Status.LastTransitionTime = &now
		if err := r.Status().Update(ctx, &orchestrator); err != nil {
			logger.Error(err, "Failed to initialize status")
			return ctrl.Result{Requeue: true}, nil
		}
	}

	switch orchestrator.Status.State {
	case StateNoUpgrade:
		return r.handleNoUpgradeState(ctx, &orchestrator)
	case StateAwaitingUpgradeConfirmation:
		return r.handleAwaitingConfirmationState(ctx, &orchestrator)
	case StateUpgradeInProgress:
		return r.handleUpgradeInProgressState(ctx, &orchestrator)
	default:
		logger.Error(fmt.Errorf("unknown state: %s", orchestrator.Status.State), "Unknown orchestrator state")
		return ctrl.Result{}, nil
	}
}

// handleNoUpgradeState
func (r *UpgradeOrchestratorReconciler) handleNoUpgradeState(ctx context.Context, orchestrator *upgraderv1alpha1.UpgradeOrchestrator) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("state", StateNoUpgrade)

	if orchestrator.Annotations["last-known-state"] == StateUpgradeInProgress {
		nodeVersions, err := r.detectNodeVersions(ctx)
		if err != nil || len(nodeVersions) != 1 {
			logger.Info("Post-upgrade: Waiting for cluster to stabilize on a single node version.")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		allMigrated, err := r.areAllWorkloadsMigrated(ctx, orchestrator, nodeVersions[0])
		if err == nil && allMigrated {
			logger.Info("✅ All workloads successfully migrated. Performing final cleanup.")
			if err := r.cleanupGlobalUpgradeArtifacts(ctx, orchestrator); err != nil {
				logger.Error(err, "Failed to perform global cleanup")
				return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
			}

			delete(orchestrator.Annotations, "last-known-state")
			if err := r.Update(ctx, orchestrator); err != nil {
				return ctrl.Result{Requeue: true}, err
			}
			logger.Info("✨ Global cleanup complete.")
			return ctrl.Result{}, nil
		}
	}

	isCordoned, err := r.isAnyNodeCordoned(ctx)
	if err != nil {
		logger.Error(err, "Failed to check for cordoned nodes")
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	if isCordoned {
		logger.Info("🚨 Cordoned node detected! Moving to AwaitingUpgradeConfirmation state.")
		orchestrator.Status.State = StateAwaitingUpgradeConfirmation
		now := metav1.Now()
		orchestrator.Status.LastTransitionTime = &now
		if err := r.Status().Update(ctx, orchestrator); err != nil {
			return ctrl.Result{Requeue: true}, err
		}
	}

	return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
}

// handleAwaitingConfirmationState
func (r *UpgradeOrchestratorReconciler) handleAwaitingConfirmationState(ctx context.Context, orchestrator *upgraderv1alpha1.UpgradeOrchestrator) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("state", StateAwaitingUpgradeConfirmation)
	logger.Info("Awaiting upgrade confirmation...")

	if time.Since(orchestrator.Status.LastTransitionTime.Time) > UpgradeConfirmationTimeout {
		logger.Info("Timeout reached. No second version detected. Assuming manual maintenance, returning to NoUpgrade state.")
		orchestrator.Status.State = StateNoUpgrade
		now := metav1.Now()
		orchestrator.Status.LastTransitionTime = &now
		if err := r.Status().Update(ctx, orchestrator); err != nil {
			return ctrl.Result{Requeue: true}, err
		}
		return ctrl.Result{}, nil
	}

	nodeVersions, err := r.detectNodeVersions(ctx)
	if err != nil {
		logger.Error(err, "Failed to detect node versions")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if len(nodeVersions) == 2 {
		logger.Info("✅ Second version detected. Upgrade confirmed! Setting initial PDB states.")

		// Lógica de bloqueo/desbloqueo inicial inteligente
		if err := r.setInitialPDBState(ctx, orchestrator); err != nil {
			logger.Error(err, "Failed to set initial PDB states")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		orchestrator.Status.State = StateUpgradeInProgress
		now := metav1.Now()
		orchestrator.Status.LastTransitionTime = &now
		if orchestrator.Annotations == nil {
			orchestrator.Annotations = make(map[string]string)
		}
		orchestrator.Annotations["last-known-state"] = StateUpgradeInProgress
		if err := r.Update(ctx, orchestrator); err != nil {
			return ctrl.Result{Requeue: true}, err
		}
		if err := r.Status().Update(ctx, orchestrator); err != nil {
			return ctrl.Result{Requeue: true}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// handleUpgradeInProgressState
func (r *UpgradeOrchestratorReconciler) handleUpgradeInProgressState(ctx context.Context, orchestrator *upgraderv1alpha1.UpgradeOrchestrator) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("state", StateUpgradeInProgress)
	logger.Info("Reconciling in UpgradeInProgress state")

	nodeVersions, err := r.detectNodeVersions(ctx)
	if err != nil || len(nodeVersions) < 2 {
		logger.Info("Waiting for two distinct node versions to proceed...")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	targetVersion, err := determineTargetVersion(nodeVersions)
	if err != nil {
		logger.Error(err, "Could not determine target version")
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	if orchestrator.Status.TargetVersion != targetVersion {
		orchestrator.Status.TargetVersion = targetVersion
		if err := r.Status().Update(ctx, orchestrator); err != nil {
			return ctrl.Result{Requeue: true}, err
		}
	}

	// PRIMERA ACCIÓN: Taintea los nodos nuevos para ganar la condición de carrera
	if err := r.prepareNodes(ctx, targetVersion); err != nil {
		logger.Error(err, "Failed to prepare nodes")
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// SEGUNDA ACCIÓN: Itera y migra
	for _, depSpec := range orchestrator.Spec.Deployments {
		workload, err := r.getWorkload(ctx, orchestrator.Namespace, depSpec.Name)
		if err != nil {
			logger.Error(err, "Workload not found", "name", depSpec.Name)
			continue
		}

		areDependenciesMet, err := r.dependenciesMet(ctx, orchestrator, depSpec.DependsOn)
		if err != nil {
			logger.Error(err, "Could not verify dependencies for workload", "workload", workload.name)
			continue
		}

		if areDependenciesMet {
			logger.Info("Dependencies met. Unblocking and adding toleration.", "workload", workload.name)
			// Desbloquear PDB para este workload específico
			if err := r.reconcilePDB(ctx, workload, true); err != nil { // allowDisruption = true
				logger.Error(err, "Failed to unblock PDB", "workload", workload.name)
				continue
			}
			// Añadir toleration para que migre
			if err := r.addTolerationToWorkload(ctx, workload); err != nil {
				logger.Error(err, "Failed to add toleration", "workload", workload.name)
				continue
			}
		} else {
			logger.Info("Dependencies not met. Workload remains blocked.", "workload", workload.name)
		}
	}

	return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
}

// NUEVA FUNCIÓN setInitialPDBState
// setInitialPDBState es la nueva lógica de "lockdown" inteligente.
func (r *UpgradeOrchestratorReconciler) setInitialPDBState(ctx context.Context, orchestrator *upgraderv1alpha1.UpgradeOrchestrator) error {
	logger := log.FromContext(ctx)
	logger.Info("Setting initial PDB states for all managed workloads.")

	for _, depSpec := range orchestrator.Spec.Deployments {
		workload, err := r.getWorkload(ctx, orchestrator.Namespace, depSpec.Name)
		if err != nil {
			logger.Error(err, "Workload not found during initial PDB setup", "name", depSpec.Name)
			continue
		}

		// Si el workload NO tiene dependencias, debe poder migrar. Lo DESBLOQUEAMOS.
		if len(depSpec.DependsOn) == 0 {
			logger.Info("Unblocking root workload to start migration", "workload", workload.name)
			if err := r.reconcilePDB(ctx, workload, true); err != nil { // allowDisruption = true
				logger.Error(err, "Failed to unblock root PDB", "workload", workload.name)
			}
		} else {
			// Si el workload SÍ tiene dependencias, lo BLOQUEAMOS.
			logger.Info("Blocking dependent workload", "workload", workload.name)
			if err := r.reconcilePDB(ctx, workload, false); err != nil { // allowDisruption = false
				logger.Error(err, "Failed to block dependent PDB", "workload", workload.name)
			}
		}
	}
	return nil
}

// Lógica de PDBs (reconcilePDB y findUserManagedPDBForWorkload) sin cambios
func (r *UpgradeOrchestratorReconciler) reconcilePDB(ctx context.Context, w *genericWorkload, allowDisruption bool) error {
	logger := log.FromContext(ctx)

	userPDB, err := r.findUserManagedPDBForWorkload(ctx, w)
	if err != nil {
		return fmt.Errorf("failed to search for user-managed PDBs for workload %s: %w", w.name, err)
	}

	operatorPDBName := fmt.Sprintf("upgrade-blocker-%s", w.name)
	operatorPDB := &policyv1.PodDisruptionBudget{}
	err = r.Get(ctx, types.NamespacedName{Name: operatorPDBName, Namespace: w.namespace}, operatorPDB)
	operatorPDBExists := !apierrors.IsNotFound(err)
	if err != nil && operatorPDBExists {
		return fmt.Errorf("failed to get operator-managed PDB %s: %w", operatorPDBName, err)
	}

	if allowDisruption {
		if userPDB != nil {
			if originalSpecJSON, ok := userPDB.Annotations[OriginalPDBSpecAnnotation]; ok {
				logger.Info("Restoring original spec for user-managed PDB", "pdb", userPDB.Name)
				var originalSpec policyv1.PodDisruptionBudgetSpec
				if err := json.Unmarshal([]byte(originalSpecJSON), &originalSpec); err != nil {
					return fmt.Errorf("failed to unmarshal original PDB spec for %s: %w", userPDB.Name, err)
				}
				patch := client.MergeFrom(userPDB.DeepCopy())
				userPDB.Spec = originalSpec
				delete(userPDB.Annotations, OriginalPDBSpecAnnotation)
				return r.Patch(ctx, userPDB, patch)
			}
		}
		if operatorPDBExists {
			logger.Info("Removing operator-managed PDB", "pdb", operatorPDB.Name)
			return client.IgnoreNotFound(r.Delete(ctx, operatorPDB))
		}
		return nil
	} else {
		if userPDB != nil {
			if _, ok := userPDB.Annotations[OriginalPDBSpecAnnotation]; ok {
				logger.Info("User-managed PDB is already under upgrade management", "pdb", userPDB.Name)
				return nil
			}
			logger.Info("Found user-managed PDB. Modifying to block disruptions.", "pdb", userPDB.Name)
			originalSpecJSON, err := json.Marshal(userPDB.Spec)
			if err != nil {
				return err
			}
			patch := client.MergeFrom(userPDB.DeepCopy())
			if userPDB.Annotations == nil {
				userPDB.Annotations = make(map[string]string)
			}
			userPDB.Annotations[OriginalPDBSpecAnnotation] = string(originalSpecJSON)
			maxUnavailable := intstr.FromInt(0)
			userPDB.Spec.MaxUnavailable = &maxUnavailable
			userPDB.Spec.MinAvailable = nil
			return r.Patch(ctx, userPDB, patch)
		} else {
			if operatorPDBExists {
				logger.Info("Operator-managed PDB already exists and is blocking.", "pdb", operatorPDB.Name)
				return nil
			}
			logger.Info("No user-managed PDB found. Creating a new blocking PDB.", "workload", w.name)
			maxUnavailable := intstr.FromInt(0)
			newPDB := &policyv1.PodDisruptionBudget{
				ObjectMeta: metav1.ObjectMeta{
					Name:        operatorPDBName,
					Namespace:   w.namespace,
					Annotations: map[string]string{ManagedByAnnotation: "upgrade-orchestrator"},
				},
				Spec: policyv1.PodDisruptionBudgetSpec{
					Selector:       w.selector,
					MaxUnavailable: &maxUnavailable,
				},
			}
			return r.Create(ctx, newPDB)
		}
	}
}

func (r *UpgradeOrchestratorReconciler) findUserManagedPDBForWorkload(ctx context.Context, w *genericWorkload) (*policyv1.PodDisruptionBudget, error) {
	logger := log.FromContext(ctx)
	pdbList := &policyv1.PodDisruptionBudgetList{}
	if err := r.List(ctx, pdbList, client.InNamespace(w.namespace)); err != nil {
		return nil, err
	}
	podLabelsToMatch := labels.Set(w.template.Labels)
	if len(podLabelsToMatch) == 0 {
		return nil, nil
	}
	for i := range pdbList.Items {
		pdb := &pdbList.Items[i]
		if _, ok := pdb.Annotations[ManagedByAnnotation]; ok {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			logger.Error(err, "Skipping PDB with invalid selector", "pdb", pdb.Name)
			continue
		}
		if selector.Matches(podLabelsToMatch) {
			return pdb, nil
		}
	}
	return nil, nil
}

// --- Resto de funciones auxiliares sin cambios ---

func (r *UpgradeOrchestratorReconciler) isAnyNodeCordoned(ctx context.Context) (bool, error) {
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return false, err
	}
	for _, node := range nodes.Items {
		for _, taint := range node.Spec.Taints {
			if taint.Key == "node.kubernetes.io/unschedulable" && taint.Effect == corev1.TaintEffectNoSchedule {
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *UpgradeOrchestratorReconciler) detectNodeVersions(ctx context.Context) ([]string, error) {
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return nil, err
	}
	versionSet := make(map[string]struct{})
	for _, node := range nodes.Items {
		version := node.Status.NodeInfo.KubeletVersion
		versionSet[version] = struct{}{}
	}
	var versions []string
	for v := range versionSet {
		versions = append(versions, v)
	}
	return versions, nil
}

func determineTargetVersion(versions []string) (string, error) {
	if len(versions) != 2 {
		return "", fmt.Errorf("expected 2 versions, got %d", len(versions))
	}
	v1, err1 := semver.NewVersion(versions[0])
	v2, err2 := semver.NewVersion(versions[1])
	if err1 != nil || err2 != nil {
		return "", fmt.Errorf("failed to parse semantic versions: %v, %v", versions[0], versions[1])
	}
	if v1.GreaterThan(v2) {
		return v1.Original(), nil
	}
	return v2.Original(), nil
}

func (r *UpgradeOrchestratorReconciler) prepareNodes(ctx context.Context, targetVersion string) error {
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return err
	}
	upgradeTaint := corev1.Taint{
		Key:    UpgradeTaintKey,
		Value:  UpgradeTaintValue,
		Effect: corev1.TaintEffectNoSchedule,
	}
	for _, node := range nodes.Items {
		if node.Status.NodeInfo.KubeletVersion == targetVersion {
			patch := client.MergeFrom(node.DeepCopy())
			if node.Labels == nil {
				node.Labels = make(map[string]string)
			}
			node.Labels[UpgradeNodeLabelKey] = targetVersion

			hasTaint := false
			for _, t := range node.Spec.Taints {
				if t.MatchTaint(&upgradeTaint) {
					hasTaint = true
					break
				}
			}
			if !hasTaint {
				node.Spec.Taints = append(node.Spec.Taints, upgradeTaint)
			}
			if err := r.Patch(ctx, &node, patch); err != nil {
				return fmt.Errorf("failed to update node %s: %w", node.Name, err)
			}
		}
	}
	return nil
}

func (r *UpgradeOrchestratorReconciler) addTolerationToWorkload(ctx context.Context, w *genericWorkload) error {
	upgradeToleration := corev1.Toleration{
		Key:      UpgradeTaintKey,
		Operator: corev1.TolerationOpEqual,
		Value:    UpgradeTaintValue,
		Effect:   corev1.TaintEffectNoSchedule,
	}
	currentWorkload, err := r.getWorkload(ctx, w.namespace, w.name)
	if err != nil {
		return err
	}
	for _, t := range currentWorkload.template.Spec.Tolerations {
		if reflect.DeepEqual(t, upgradeToleration) {
			return nil
		}
	}
	patch := client.MergeFrom(currentWorkload.GetObject().DeepCopyObject().(client.Object))
	currentWorkload.template.Spec.Tolerations = append(currentWorkload.template.Spec.Tolerations, upgradeToleration)
	return r.Patch(ctx, currentWorkload.GetObject(), patch)
}

func (r *UpgradeOrchestratorReconciler) areAllWorkloadsMigrated(ctx context.Context, orchestrator *upgraderv1alpha1.UpgradeOrchestrator, targetVersion string) (bool, error) {
	for _, depSpec := range orchestrator.Spec.Deployments {
		migrated, err := r.isWorkloadMigrated(ctx, orchestrator.Namespace, depSpec.Name, targetVersion)
		if err != nil || !migrated {
			return false, err
		}
	}
	return true, nil
}

func (r *UpgradeOrchestratorReconciler) cleanupGlobalUpgradeArtifacts(ctx context.Context, orchestrator *upgraderv1alpha1.UpgradeOrchestrator) error {
	logger := log.FromContext(ctx)
	logger.Info("Starting global cleanup of upgrade artifacts")
	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return err
	}
	for _, node := range nodes.Items {
		patch := client.MergeFrom(node.DeepCopy())
		var newTaints []corev1.Taint
		taintFound := false
		for _, taint := range node.Spec.Taints {
			if taint.Key == UpgradeTaintKey {
				taintFound = true
			} else {
				newTaints = append(newTaints, taint)
			}
		}
		if taintFound {
			node.Spec.Taints = newTaints
			if err := r.Patch(ctx, &node, patch); err != nil {
				logger.Error(err, "Failed to remove taint from node", "node", node.Name)
			}
		}
	}
	for _, depSpec := range orchestrator.Spec.Deployments {
		workload, err := r.getWorkload(ctx, orchestrator.Namespace, depSpec.Name)
		if err != nil {
			continue
		}
		if err := r.reconcilePDB(ctx, workload, true); err != nil {
			logger.Error(err, "Failed to restore PDB during cleanup", "workload", workload.name)
		}

		patch := client.MergeFrom(workload.GetObject().DeepCopyObject().(client.Object))
		var newTolerations []corev1.Toleration
		tolerationFound := false
		for _, t := range workload.template.Spec.Tolerations {
			if t.Key == UpgradeTaintKey {
				tolerationFound = true
			} else {
				newTolerations = append(newTolerations, t)
			}
		}
		if tolerationFound {
			workload.template.Spec.Tolerations = newTolerations
			if err := r.Patch(ctx, workload.GetObject(), patch); err != nil {
				logger.Error(err, "Failed to remove toleration", "workload", workload.name)
			}
		}
	}
	return nil
}

func (r *UpgradeOrchestratorReconciler) dependenciesMet(ctx context.Context, orchestrator *upgraderv1alpha1.UpgradeOrchestrator, deps []string) (bool, error) {
	if len(deps) == 0 {
		return true, nil
	}
	targetVersion := orchestrator.Status.TargetVersion
	if targetVersion == "" {
		return false, fmt.Errorf("cannot check dependencies, targetVersion is not set in status")
	}
	for _, depName := range deps {
		migrated, err := r.isWorkloadMigrated(ctx, orchestrator.Namespace, depName, targetVersion)
		if err != nil || !migrated {
			return false, err
		}
	}
	return true, nil
}

func (r *UpgradeOrchestratorReconciler) isWorkloadMigrated(ctx context.Context, ns, name, targetVersion string) (bool, error) {
	logger := log.FromContext(ctx)
	workload, err := r.getWorkload(ctx, ns, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("could not get dependency workload '%s': %w", name, err)
	}
	podList := &corev1.PodList{}
	selector, err := metav1.LabelSelectorAsSelector(workload.selector)
	if err != nil {
		return false, fmt.Errorf("invalid label selector for dependency '%s': %w", name, err)
	}
	if err := r.List(ctx, podList, &client.ListOptions{Namespace: ns, LabelSelector: selector}); err != nil {
		return false, fmt.Errorf("failed to list pods for dependency '%s': %w", name, err)
	}
	if len(podList.Items) == 0 {
		return true, nil
	}
	for _, pod := range podList.Items {
		if pod.Spec.NodeName == "" {
			return false, nil
		}
		var node corev1.Node
		if err := r.Get(ctx, types.NamespacedName{Name: pod.Spec.NodeName}, &node); err != nil {
			return false, err
		}
		if node.Status.NodeInfo.KubeletVersion != targetVersion {
			logger.Info("Dependency not met: pod is on an old node", "dependency", name, "pod", pod.Name, "nodeVersion", node.Status.NodeInfo.KubeletVersion)
			return false, nil
		}
	}
	return true, nil
}

type genericWorkload struct {
	kind      string
	namespace string
	name      string
	selector  *metav1.LabelSelector
	template  *corev1.PodTemplateSpec
	obj       client.Object
}

func (w *genericWorkload) GetObject() client.Object {
	return w.obj
}

func (r *UpgradeOrchestratorReconciler) getWorkload(ctx context.Context, ns, name string) (*genericWorkload, error) {
	ss := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, ss); err == nil {
		return &genericWorkload{kind: "StatefulSet", namespace: ns, name: name, selector: ss.Spec.Selector, template: &ss.Spec.Template, obj: ss}, nil
	}
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, dep); err == nil {
		return &genericWorkload{kind: "Deployment", namespace: ns, name: name, selector: dep.Spec.Selector, template: &dep.Spec.Template, obj: dep}, nil
	}
	return nil, fmt.Errorf("workload %s not found", name)
}

func (r *UpgradeOrchestratorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&upgraderv1alpha1.UpgradeOrchestrator{}).
		Owns(&corev1.Pod{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(r.mapNodeToOrchestrator),
		).
		Complete(r)
}

func (r *UpgradeOrchestratorReconciler) mapNodeToOrchestrator(ctx context.Context, obj client.Object) []reconcile.Request {
	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{
			Name:      "deploy-dependency-map", // O el nombre que uses
			Namespace: "cadence-master",        // O el namespace que uses
		}},
	}
}
