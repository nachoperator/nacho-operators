package controller

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	certmonitorv1alpha1 "github.com/masmovil/mm-monorepo/pkg/runtime/operators/cert-monitor-operator/api/v1alpha1"
	"github.com/masmovil/mm-monorepo/pkg/runtime/operators/cert-monitor-operator/pkg/notifications"
	"github.com/masmovil/mm-monorepo/pkg/runtime/operators/cert-monitor-operator/pkg/utils"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// CertificateMonitorReconciler reconciles a CertificateMonitor object
type CertificateMonitorReconciler struct {
	client.Client
	Log logr.Logger
}

// RBAC permissions... (copied from your input)
//+kubebuilder:rbac:groups=certmonitor.masorange.com,resources=certificatemonitors,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=certmonitor.masorange.com,resources=certificatemonitors/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=certmonitor.masorange.com,resources=certificatemonitors/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *CertificateMonitorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("certificatemonitor", req.NamespacedName)
	log.Info("Starting reconciliation")

	// 1. Fetch the CertificateMonitor instance
	var certMonitor certmonitorv1alpha1.CertificateMonitor
	if err := r.Get(ctx, req.NamespacedName, &certMonitor); err != nil {
		// Ignore if not found, might have been deleted
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Keep a pristine copy of the status read from the API server
	originalStatus := certMonitor.Status.DeepCopy()
	log.Info("Processing CertificateMonitor", "name", certMonitor.Name, "resourceVersion", certMonitor.ResourceVersion)

	// 2. List all Secret keys in the cluster (ensure utils.ListAllSecrets exists and works)
	secretKeys, err := utils.ListAllSecrets(ctx, r.Client, log)
	if err != nil {
		log.Error(err, "Failed to list secrets")
		return ctrl.Result{}, err  
	}
	log.Info("Total secrets found in cluster", "count", len(secretKeys))

	// --- Prepare Selectors ---
	nsSelector, secretSelector, err := r.prepareSelectors(ctx, &certMonitor)
	if err != nil {
		log.Error(err, "Failed to prepare selectors")
		return ctrl.Result{}, err  
	}

	// --- 3. Iterate, fetch, filter, and process ---
	monitoredCertsInfo := []certmonitorv1alpha1.MonitoredCertificateInfo{}
	certsToNotify := []certmonitorv1alpha1.MonitoredCertificateInfo{}
	secretsProcessedCount := 0
	now := time.Now() // Get current time once for consistency
	nowMetaTime := metav1.NewTime(now) // Convert to metav1.Time once

	for _, key := range secretKeys {
		var secret corev1.Secret
		if err := r.Get(ctx, key, &secret); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "Failed to get Secret object", "key", key)
			}
			continue // Skip this secret on error
		}

		// Apply Filters
		matches, err := r.matchesSelectors(ctx, &secret, nsSelector, secretSelector)
		if err != nil {
			log.Error(err, "Failed to check selectors", "key", key)
			continue // Skip this secret on error
		}
		if !matches {
			continue // Skip this secret if no match
		}

		// Process Filtered Secret Data
		secretsProcessedCount++

		for keyName, dataValue := range secret.Data {
			// Only process .crt files
			if strings.HasSuffix(keyName, ".crt") {
				block, _ := pem.Decode(dataValue)
				if block == nil {
					log.Info("Failed PEM decode, skipping data key", "key", key, "dataKey", keyName)
					continue
				}
				cert, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					log.Info("Failed cert parse, skipping data key", "key", key, "dataKey", keyName, "error", err.Error())
					continue
				}

				// Certificate Parsed Successfully
				expirationDate := cert.NotAfter
				daysUntilExpiration := int(expirationDate.Sub(now).Hours() / 24)
				isExpired := expirationDate.Before(now)
				isNearExpiration := !isExpired && certMonitor.Spec.RenewalThresholdDays > 0 && daysUntilExpiration <= int(certMonitor.Spec.RenewalThresholdDays)

				certInfo := certmonitorv1alpha1.MonitoredCertificateInfo{
					Namespace:           secret.Namespace,
					SecretName:          secret.Name,
					SecretKey:           keyName,
					SubjectCN:           cert.Subject.CommonName,
					IssuerCN:            cert.Issuer.CommonName,
					ExpiresAt:           metav1.NewTime(expirationDate),
					DaysUntilExpiration: daysUntilExpiration,
					IsExpired:           isExpired,
					IsNearExpiration:    isNearExpiration,
				}
				monitoredCertsInfo = append(monitoredCertsInfo, certInfo)

				if isNearExpiration || isExpired {
					certsToNotify = append(certsToNotify, certInfo)
				}
			} // End if .crt
		} // End inner loop (secret.Data)
	} // End outer loop (secretKeys)

	log.Info("Finished processing secrets", "processedCount", secretsProcessedCount, "certsFound", len(monitoredCertsInfo), "certsToNotify", len(certsToNotify))

	// --- Notification Sending Logic ---
	notificationSent := false
	if len(certsToNotify) > 0 {
		// Calculate cooldown 
		notificationCooldown := time.Hour * 24
		sendNotification := true
		// Use the status read at the START of reconcile for cooldown check
		if originalStatus.LastNotificationTime != nil && time.Since(originalStatus.LastNotificationTime.Time) < notificationCooldown {
			log.Info("Skipping notification due to cooldown", "lastSent", originalStatus.LastNotificationTime.Time, "cooldown", notificationCooldown)
			sendNotification = false
		}

		if sendNotification {
			log.Info("Sending aggregated notification...", "count", len(certsToNotify))
			// Ensure this function only READS certMonitor, doesn't modify it
			r.sendAggregatedNotifications(ctx, log, &certMonitor, certsToNotify)
			notificationSent = true
		}
	}
	// --- End Notification Sending Logic ---

	// --- Prepare desired Status based on THIS reconcile run ---
	// Start from the status read at the beginning to preserve unknown fields
	// or fields not calculated here (like Conditions, if you add them later).
	desiredStatus := originalStatus.DeepCopy()
	desiredStatus.MonitoredCertificates = monitoredCertsInfo
	desiredStatus.LastChecked = nowMetaTime // Use the consistent 'now' timestamp
	if notificationSent {
		// Update LastNotificationTime ONLY if a notification was sent in THIS run
		desiredStatus.LastNotificationTime = &nowMetaTime
	}
	// If notificationSent is false, desiredStatus.LastNotificationTime retains
	// the value from originalStatus (read at the start).

	// --- Update Status ONLY IF Meaningful Changes Detected ---
	// Define "meaningful" change: certificate list changed OR notification time updated.
	// We explicitly ignore changes *only* to LastChecked to prevent loops.
	meaningfulChangeDetected := !reflect.DeepEqual(originalStatus.MonitoredCertificates, desiredStatus.MonitoredCertificates) ||
		(notificationSent && (originalStatus.LastNotificationTime == nil || !originalStatus.LastNotificationTime.Equal(desiredStatus.LastNotificationTime)))
	// Add || !reflect.DeepEqual(originalStatus.Conditions, desiredStatus.Conditions) if you manage conditions

	// Check if anything at all changed in status
	statusActuallyChanged := !reflect.DeepEqual(originalStatus, desiredStatus)

	if statusActuallyChanged && meaningfulChangeDetected {
		log.Info("Meaningful status change detected, attempting to update status with retry on conflict")

		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Fetch the LATEST version of the CR inside the retry loop
			currentCertMonitor := &certmonitorv1alpha1.CertificateMonitor{}
			if getErr := r.Get(ctx, req.NamespacedName, currentCertMonitor); getErr != nil {
				log.Error(getErr, "Failed to re-fetch CertificateMonitor before status update retry")
				return getErr
			}
			// Apply the DESIRED status (calculated before the retry block) onto the LATEST fetched object
			currentCertMonitor.Status = *desiredStatus

			// Attempt the update
			updateErr := r.Status().Update(ctx, currentCertMonitor)
			if updateErr != nil && apierrors.IsConflict(updateErr) {
				log.V(1).Info("Status update conflict detected, retrying...") // Requires -v=1 or higher
			}
			return updateErr
		})

		// Handle final error after retries
		if err != nil {
			log.Error(err, "Failed to update CertificateMonitor status after multiple retries")
			return ctrl.Result{Requeue: true}, err // Requeue on persistent failure
		}
		log.Info("CertificateMonitor status updated successfully after retry logic")

	} else if statusActuallyChanged {
		log.Info("Skipping status update because only non-meaningful fields changed (e.g., LastChecked).")
	} else {
		log.Info("CertificateMonitor status has not changed, skipping update.")
	}

	// --- Requeue Logic ---
	fixedRequeueInterval := time.Hour * 1  

	log.Info("Reconciliation finished, requeuing after fixed interval (due to GenerationChangedPredicate)", "after", fixedRequeueInterval)
	return ctrl.Result{RequeueAfter: fixedRequeueInterval}, nil
}

// -----------------------------------------------------------------------------
// Helper Functions
// -----------------------------------------------------------------------------

// prepareSelectors prepares label selectors from the CertificateMonitor spec.
func (r *CertificateMonitorReconciler) prepareSelectors(ctx context.Context, certMonitor *certmonitorv1alpha1.CertificateMonitor) (labels.Selector, labels.Selector, error) {
	log := log.FromContext(ctx)
	var nsSelector, secretSelector labels.Selector
	var err error

	// Prepare Namespace Selector
	if certMonitor.Spec.NamespaceSelector != nil {
		nsSelector, err = metav1.LabelSelectorAsSelector(certMonitor.Spec.NamespaceSelector)
		if err != nil {
			log.Error(err, "Invalid NamespaceSelector")
			return nil, nil, err
		}
	} else {
		nsSelector = labels.Everything()
	}
	log.Info("Using NamespaceSelector", "selector", nsSelector.String())  

	// Prepare Secret Selector
	if certMonitor.Spec.SecretSelector != nil {
		secretSelector, err = metav1.LabelSelectorAsSelector(certMonitor.Spec.SecretSelector)
		if err != nil {
			log.Error(err, "Invalid SecretSelector")
			return nil, nil, err
		}
	} else {
		secretSelector = labels.Everything()
	}
	log.Info("Using SecretSelector", "selector", secretSelector.String())  

	return nsSelector, secretSelector, nil
}

// matchesSelectors checks if a secret matches the provided namespace and secret selectors.
func (r *CertificateMonitorReconciler) matchesSelectors(ctx context.Context, secret *corev1.Secret, nsSelector, secretSelector labels.Selector) (bool, error) {
	log := log.FromContext(ctx)

	// Check Namespace Selector
	if !nsSelector.Empty() {
		var ns corev1.Namespace
		if err := r.Get(ctx, types.NamespacedName{Name: secret.Namespace}, &ns); err != nil {
			if apierrors.IsNotFound(err) {
				log.V(1).Info("Namespace not found for selector check, treating as non-match", "namespace", secret.Namespace) 
				return false, nil
			}
			log.Error(err, "Failed to get Namespace object for selector check", "namespace", secret.Namespace)
			return false, err // Return error if we couldn't get the namespace
		}
		if !nsSelector.Matches(labels.Set(ns.Labels)) {
			return false, nil
		}
	}

	// Check Secret Selector
	if !secretSelector.Empty() {
		if !secretSelector.Matches(labels.Set(secret.Labels)) {
			return false, nil
		}
	}

	return true, nil
}

// sendAggregatedNotifications sends notifications based on the spec.
// 'allCertsToNotify' contains all certificates requiring notification from all inspected namespaces.
func (r *CertificateMonitorReconciler) sendAggregatedNotifications(ctx context.Context, log logr.Logger, certMonitor *certmonitorv1alpha1.CertificateMonitor, allCertsToNotify []certmonitorv1alpha1.MonitoredCertificateInfo) {
    
    var scopedCertsForSlackAndOpsgenie []certmonitorv1alpha1.MonitoredCertificateInfo
    var notificationNsSelector labels.Selector
    var err error
    // This flag indicates if the user intends to scope notifications using the selector.
    // It's true if NotificationNamespaceSelector is provided in the spec, even if the selector itself is empty (which means select all).
    userWantsScopedNotifications := certMonitor.Spec.NotificationNamespaceSelector != nil

    if userWantsScopedNotifications {
        // Convert metav1.LabelSelector to labels.Selector
        // An empty selector {} from the user means "match all namespaces" (that have any labels or no labels, depending on behavior).
        // A nil spec.NotificationNamespaceSelector means user did not configure this feature.
        notificationNsSelector, err = metav1.LabelSelectorAsSelector(certMonitor.Spec.NotificationNamespaceSelector)
        if err != nil {
            log.Error(err, "Invalid NotificationNamespaceSelector in spec. Scoped notifications for Webhook/Opsgenie will be skipped.")
            // Do not proceed with scoped notifications if the selector is invalid
            userWantsScopedNotifications = false // Effectively disable scoped notifications due to error
        } else {
            log.Info("NotificationNamespaceSelector is active", "selector", notificationNsSelector.String())
        }
    }

    if userWantsScopedNotifications { // Proceed only if selector was provided and is valid
        // Cache for namespace checks to avoid repeated Get calls for the same namespace during this reconcile loop
        namespaceMatchesCache := make(map[string]bool)

        for _, cert := range allCertsToNotify {
            matches, foundInCache := namespaceMatchesCache[cert.Namespace]
            if !foundInCache {
                var ns corev1.Namespace
                if getErr := r.Get(ctx, types.NamespacedName{Name: cert.Namespace}, &ns); getErr != nil {
                    if apierrors.IsNotFound(getErr) {
                        log.V(1).Info("Namespace not found for notification scope check, cert will not be included in scoped notifications", 
                                     "certificateNamespace", cert.Namespace, "certificateSecret", cert.SecretName)
                        matches = false
                    } else {
                        log.Error(getErr, "Failed to get Namespace for notification scope check, cert will not be included in scoped notifications", 
                                  "certificateNamespace", cert.Namespace, "certificateSecret", cert.SecretName)
                        matches = false 
                    }
                } else {
                    matches = notificationNsSelector.Matches(labels.Set(ns.Labels))
                }
                namespaceMatchesCache[cert.Namespace] = matches
            }

            if matches {
                scopedCertsForSlackAndOpsgenie = append(scopedCertsForSlackAndOpsgenie, cert)
            }
        }
        log.Info("Filtered certificates for Webhook/Opsgenie based on NotificationNamespaceSelector",
            "selector", notificationNsSelector.String(),
            "certsInScopeCount", len(scopedCertsForSlackAndOpsgenie))
    }
    // If userWantsScopedNotifications is false (either because spec.NotificationNamespaceSelector was nil or selector was invalid),
    // scopedCertsForSlackAndOpsgenie will be empty.

    // --- Send Webhook Notification (e.g., Slack) ---
    if certMonitor.Spec.NotificationEndpoint != "" {
        if userWantsScopedNotifications { // Only act if scoping was intended and selector was valid
            if len(scopedCertsForSlackAndOpsgenie) > 0 {
                var messageBody bytes.Buffer
                messageBody.WriteString(fmt.Sprintf("🚨 Certificate Expiry Alert (CR: *%s/%s*, Scoped by Selector: *%s*):\n",
                    certMonitor.Namespace, certMonitor.Name, notificationNsSelector.String()))
                messageBody.WriteString(fmt.Sprintf("Found *%d* certificates matching scope, expiring within *%d days* or already expired:\n",
                    len(scopedCertsForSlackAndOpsgenie), certMonitor.Spec.RenewalThresholdDays))
                for _, cert := range scopedCertsForSlackAndOpsgenie { // Use the filtered list
                    status := ""
                    if cert.IsExpired {
                        status = fmt.Sprintf("*EXPIRED* (%d days ago)", cert.DaysUntilExpiration*-1)
                    } else {
                        status = fmt.Sprintf("%d days left", cert.DaysUntilExpiration)
                    }
                    messageBody.WriteString(fmt.Sprintf("  • *%s/%s* (Key: *%s*, Subject: *%s*) - %s\n",
                        cert.Namespace, cert.SecretName, cert.SecretKey, cert.SubjectCN, status))
                }
                err := notifications.SendAggregatedWebhookNotification(log, certMonitor.Spec.NotificationEndpoint, messageBody.String())
                if err != nil {
                    log.Error(err, "Failed to send scoped webhook notification", "endpoint", certMonitor.Spec.NotificationEndpoint)
                } else {
                    log.Info("Scoped webhook notification sent successfully", "endpoint", certMonitor.Spec.NotificationEndpoint)
                }
            } else {
                log.Info("Webhook notification: No certificates require notification in the namespaces matching the selector.", "selector", notificationNsSelector.String())
            }
        } else if certMonitor.Spec.NotificationNamespaceSelector != nil && err != nil { // Selector was provided but invalid
             log.Error(err, "Webhook notification skipped due to invalid NotificationNamespaceSelector in spec.")
        } else { // No selector was provided in spec by user
            log.Info("Webhook notification skipped: NotificationNamespaceSelector is not set in CertificateMonitor spec.")
        }
    }

    // --- Send Email Notification (still uses allCertsToNotify for a global overview) ---
    if len(certMonitor.Spec.EmailRecipients) > 0 && len(allCertsToNotify) > 0 {
        var emailMessageBody bytes.Buffer
        emailMessageBody.WriteString(fmt.Sprintf("🚨 Certificate Expiry Alert (CR: *%s/%s*) - All Monitored Scopes:\n",
            certMonitor.Namespace, certMonitor.Name))
        emailMessageBody.WriteString(fmt.Sprintf("Found *%d* certificates expiring within *%d days* or already expired:\n",
            len(allCertsToNotify), certMonitor.Spec.RenewalThresholdDays))
        for _, cert := range allCertsToNotify { // Use the complete list
            status := ""
            if cert.IsExpired {
                status = fmt.Sprintf("*EXPIRED* (%d days ago)", cert.DaysUntilExpiration*-1)
            } else {
                status = fmt.Sprintf("%d days left", cert.DaysUntilExpiration)
            }
            emailMessageBody.WriteString(fmt.Sprintf("  • *%s/%s* (Key: *%s*, Subject: *%s*) - %s\n",
                cert.Namespace, cert.SecretName, cert.SecretKey, cert.SubjectCN, status))
        }
        subject := fmt.Sprintf("Certificate Expiry Alert (%s/%s) - %d Certificates Overall",
            certMonitor.Namespace, certMonitor.Name, len(allCertsToNotify))
        err := notifications.SendAggregatedEmailNotification(log, certMonitor.Spec.EmailRecipients, subject, emailMessageBody.String())
        if err != nil {
            log.Error(err, "Failed to send aggregated email notification", "recipients", certMonitor.Spec.EmailRecipients)
        } else {
            log.Info("Aggregated email notification sent successfully", "recipients", certMonitor.Spec.EmailRecipients)
        }
    }
    
    // --- Send Opsgenie Notification ---
    if certMonitor.Spec.Opsgenie != nil && certMonitor.Spec.Opsgenie.Enabled {
        if userWantsScopedNotifications { // Only act if scoping was intended and selector was valid
            if len(scopedCertsForSlackAndOpsgenie) > 0 {
                apiKey, getApiKeyErr := r.getOpsgenieAPIKey(ctx, certMonitor.Namespace, certMonitor.Spec.Opsgenie.APIKeySecretRef)
                if getApiKeyErr != nil {
                    log.Error(getApiKeyErr, "Failed to get Opsgenie API key, skipping Opsgenie notification")
                    return
                }
                if apiKey == "" {
                    return
                }

                var opsgenieDetailedMessage bytes.Buffer
                opsgenieDetailedMessage.WriteString(fmt.Sprintf("Certificate Expiry Alert (CR: %s/%s, Scoped by Selector: %s):\n",
                    certMonitor.Namespace, certMonitor.Name, notificationNsSelector.String()))
                opsgenieDetailedMessage.WriteString(fmt.Sprintf("Found %d certificates matching scope, expiring within %d days or already expired:\n",
                    len(scopedCertsForSlackAndOpsgenie), certMonitor.Spec.RenewalThresholdDays))
                for _, cert := range scopedCertsForSlackAndOpsgenie { // Use the filtered list
                     status := ""
                    if cert.IsExpired {
                        status = fmt.Sprintf("EXPIRED (%d days ago)", cert.DaysUntilExpiration*-1)
                    } else {
                        status = fmt.Sprintf("%d days left", cert.DaysUntilExpiration)
                    }
                    opsgenieDetailedMessage.WriteString(fmt.Sprintf("  - %s/%s (Key: %s, Subject: %s) - %s\n",
                        cert.Namespace, cert.SecretName, cert.SecretKey, cert.SubjectCN, status))
                }
                
                opsgenieMessageTitle := fmt.Sprintf("Cert Expiry (Selector: %s): %s/%s - %d certs",
                    notificationNsSelector.String(), certMonitor.Namespace, certMonitor.Name, len(scopedCertsForSlackAndOpsgenie))
                
                alertAlias := fmt.Sprintf("cert-monitor-%s-%s-selectorbased",
                    certMonitor.Namespace, certMonitor.Name) // Consider making alias more dynamic if selector changes often

                sendAlertErr := notifications.SendOpsgenieAlert(
                    log, apiKey, certMonitor.Spec.Opsgenie.Region, certMonitor.Spec.Opsgenie.Priority,
                    opsgenieMessageTitle, opsgenieDetailedMessage.String(), alertAlias,
                    scopedCertsForSlackAndOpsgenie, // Use the filtered list
                    certMonitor.Namespace, certMonitor.Name,
                )
                if sendAlertErr != nil {
                    log.Error(sendAlertErr, "Failed to send scoped Opsgenie alert")
                } else {
                    log.Info("Scoped Opsgenie alert sent successfully")
                }
            } else {
                log.Info("Opsgenie notification: No certificates require notification in the namespaces matching the selector.", "selector", notificationNsSelector.String())
            }
        } else if certMonitor.Spec.NotificationNamespaceSelector != nil && err != nil { // Selector was provided but invalid
            log.Error(err, "Opsgenie notification skipped due to invalid NotificationNamespaceSelector in spec.")
        } else { // No selector was provided by user
            log.Info("Opsgenie notification skipped: NotificationNamespaceSelector is not set in CertificateMonitor spec.")
        }
    }
}

// getOpsgenieAPIKey fetches the Opsgenie API key from the specified Kubernetes Secret.
func (r *CertificateMonitorReconciler) getOpsgenieAPIKey(ctx context.Context, crNamespace string, secretRef *certmonitorv1alpha1.OpsgenieAPIKeySecretRef) (string, error) {
    log := log.FromContext(ctx)

    if secretRef == nil {
        return "", fmt.Errorf("Opsgenie APIKeySecretRef is nil")
    }
    if secretRef.Name == "" {
        return "", fmt.Errorf("Opsgenie APIKeySecretRef.Name is empty")
    }
    if secretRef.Key == "" {
        return "", fmt.Errorf("Opsgenie APIKeySecretRef.Key is empty")
    }

    secretNamespace := secretRef.Namespace
    if secretNamespace == "" {
        // Default to the CertificateMonitor's namespace if not specified in the secretRef
        secretNamespace = crNamespace
        log.Info("Opsgenie APIKeySecretRef.Namespace is empty, defaulting to CertificateMonitor's namespace", "namespace", secretNamespace)
    }

    secret := &corev1.Secret{}
    secretNamespacedName := types.NamespacedName{
        Name:      secretRef.Name,
        Namespace: secretNamespace,
    }

    if err := r.Client.Get(ctx, secretNamespacedName, secret); err != nil {
        log.Error(err, "Failed to get Opsgenie API key secret", "secretName", secretRef.Name, "secretNamespace", secretNamespace)
        return "", fmt.Errorf("failed to get Opsgenie API key secret %s/%s: %w", secretNamespace, secretRef.Name, err)
    }

    apiKeyBytes, ok := secret.Data[secretRef.Key]
    if !ok {
        log.Error(fmt.Errorf("key %s not found in Opsgenie API key secret %s/%s", secretRef.Key, secretNamespace, secretRef.Name), "API key retrieval error")
        return "", fmt.Errorf("key %s not found in Opsgenie API key secret %s/%s", secretRef.Key, secretNamespace, secretRef.Name)
    }

    apiKey := strings.TrimSpace(string(apiKeyBytes))
    if apiKey == "" {
        log.Error(fmt.Errorf("API key found but is empty in Opsgenie API key secret %s/%s, key %s", secretNamespace, secretRef.Name, secretRef.Key), "API key retrieval error")
        return "", fmt.Errorf("API key is empty in secret %s/%s, key %s", secretNamespace, secretRef.Name, secretRef.Key)
    }
    
    log.Info("Successfully retrieved Opsgenie API key from secret", "secretName", secretRef.Name, "secretNamespace", secretNamespace)
    return apiKey, nil
}

func (r *CertificateMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&certmonitorv1alpha1.CertificateMonitor{},
            // ***** only detect .spec changes, to avoid continuous loop *****
            builder.WithPredicates(predicate.GenerationChangedPredicate{}),
        ).
        Complete(r)
}
