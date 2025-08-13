# Kubernetes Certificate Expiry Monitor Operator

## Overview

The Kubernetes Certificate Expiry Monitor Operator is a proactive tool designed to manage the lifecycle of TLS certificates within your Kubernetes cluster. It automatically discovers certificates, extracts detailed information, calculates expiry timelines, and integrates with alerting platforms like Slack and Opsgenie to notify you of certificates requiring attention. This helps prevent service disruptions caused by expired certificates.

## Features

* **Comprehensive Certificate Data Extraction:**
    * Retrieves full X.509 certificate details from TLS Secrets (type `kubernetes.io/tls`).
    * Extracts key information including Subject, Issuer, serial number, `NotBefore` (validity start date, often indicating renewal date), and `NotAfter` (expiry date).
* **Expiry & Renewal Status Calculation:**
    * Identifies certificates nearing their expiration date based on a configurable `renewalThresholdDays`.
    * Can provide insights into recently renewed/issued certificates by analyzing their `NotBefore` date.
* **Flexible Scope Control & Filtering:**
    * Monitor certificates across all namespaces or limit scanning to a specified list of namespaces.
    * Target specific Secrets by their names.
    * Utilize label selectors on Secrets to precisely include or exclude certificates from monitoring.
* **Integrated Alerting & Notification:**
    * Sends notifications to **Slack** using a pre-configured Incoming Webhook URL for certificates approaching their renewal deadline.
    * Integrates with **Opsgenie** (if enabled) using API keys to create alerts for critical certificate expiry events, ensuring visibility and prompt action.

## How it Works

The operator implements a control loop that performs the following actions:

1.  **Discovery:** Watches Kubernetes `Secret` resources of type `kubernetes.io/tls`. This discovery can be scoped by namespaces, label selectors, or specific Secret names as configured in its Custom Resource (if applicable, and managed via Helm values).
2.  **Data Extraction:** For each discovered Secret, the operator extracts the `tls.crt` data.
3.  **Certificate Parsing:** The X.509 certificate data is parsed to retrieve its full details, including subject, issuer, validity period (`NotBefore`, `NotAfter`), and other attributes.
4.  **Date Calculation:**
    * Compares the certificate's `NotAfter` date with the current date and the configured `renewalThresholdDays` to determine if it's nearing expiration.
    * The `NotBefore` date is available to identify recently issued/renewed certificates.
5.  **Alerting:**
    * If a certificate is identified as nearing expiration, the operator constructs and sends notifications to the configured Slack Incoming Webhook URL and/or to Opsgenie (if enabled, using its API key).

## Prerequisites

* Kubernetes cluster (v1.19+ recommended).
* `kubectl` configured to interact with your cluster.
* **`helmfile`** (ensure your version supports OCI, e.g., v0.139.0+).
* A **Slack Incoming Webhook URL** if Slack notifications are enabled.
* For Opsgenie (if enabled): A valid Opsgenie API key and the name of the Kubernetes Secret storing it.

## Deployment

Deployment of the Certificate Monitor Operator is managed using `helmfile`, from `cd-core` repository.

1.  **Locate/Create `helmfile.yaml`:**
    Within your deployment management structure (e.g., `cd-core/path/to/your/env/`), you should have a `helmfile.yaml` file. (TODO: User should specify the exact path or structure if needed for clarity, e.g., `cd-core/environments/development/main/helmfile.yaml`).

    A typical `helmfile.yaml` configuration for this operator would look like this:

    ```yaml
    # helmfile.yaml
    helmDefaults:
      atomic: true
      cleanupOnFail: true
      kubeContext: gke_mm-k8s-dev-01_europe-southwest1_mm-k8s-dev-eusw1-01 # Adjust per environment
      wait: true

    repositories:
      - name: mm-cloud-runtime # Repository for the operator chart
        url: europe-docker.pkg.dev/mm-cloud-runtime-prod/helm-charts
        oci: true
      - name: mm-platform-sre-prod # Example of another SRE platform repository
        url: europe-docker.pkg.dev/mm-platform-sre-prod/helm-charts
        oci: true
      # Add other repositories as needed

    releases:
      - name: cert-monitor # Name of this Helm release
        namespace: operator # Target namespace for the operator
        chart: mm-cloud-runtime/cert-monitor-operator # Chart name from the OCI repository
        version: v0.0.4 # Specify the desired chart version
        values:
          - ./values.yaml # Path to your values file for this release (relative to helmfile.yaml)
    ```

2.  **Prepare your `values.yaml`:**
    In the same directory as your `helmfile.yaml` (or as referenced by the `values:` path), create/edit your `values.yaml` file with the specific configuration:

    ```yaml
    # values.yaml
    cr:
      renewalThresholdDays: 30
      notifications:
        slack:
          enabled: true
          # The direct Slack Incoming Webhook URL.
          # This URL itself contains the necessary token/secret.
          notificationEndpoint: [https://hooks.slack.com/services/T06FTLLKNGK/B08RUEdHMXT3/FNgpFzRBeJZyRddxp810u9E6mG](https://hooks.slack.com/services/T06FTLLKNGK/B08RUEdHMXT3/FNgpFzRBeJZyRddxp810u9E6mG) # Replace with your actual Slack Webhook URL
        opsgenie:
          enabled: false
          # If enabled, configure Opsgenie details here:
          # apiKeySecretName: "opsgenie-cert-monitor-apikey" # Name of the K8s Secret
          # apiKeySecretKey: "apiKey"                         # Key within the Secret
          # region: "EU"                                    # Your Opsgenie region (e.g., EU, US)
    ```

3.  **Deploy using `helmfile`:**
    Navigate to the directory containing your `helmfile.yaml` and run:
    ```bash
    helmfile sync
    ```
    Or, to review changes before applying:
    ```bash
    helmfile diff
    helmfile apply
    ```
    This command will deploy or update the `cert-monitor-operator` chart with your specified configuration.

## Configuration

The operator's behavior is configured via the `values.yaml` file used by `helmfile`. The Helm chart uses these values to set up the operator's deployment and any associated Custom Resources (CRs) or configurations.

Key configurable parameters in your `values.yaml` (typically under a `cr:` block or similar, depending on the chart's structure) are:

| `values.yaml` Path                            | Description                                                                 | Example from provided `values.yaml`   |
| :-------------------------------------------- | :-------------------------------------------------------------------------- | :------------------------------------ |
| `cr.renewalThresholdDays`                       | Days before a certificate's expiration to trigger a notification.           | `30`                                  |
| `cr.notifications.slack.enabled`              | Enable (`true`) or disable (`false`) Slack notifications.                   | `true`                                |
| `cr.notifications.slack.notificationEndpoint` | The full Slack Incoming Webhook URL used for sending notifications.         | `https://hooks.slack.com/...`         |
| `cr.notifications.opsgenie.enabled`           | Enable (`true`) or disable (`false`) Opsgenie alerts.                       | `false`                               |

### Notification System Details

* **Slack:**
    When `cr.notifications.slack.enabled: true`, the operator will send notifications to the URL specified in `cr.notifications.slack.notificationEndpoint`. This should be a complete Slack Incoming Webhook URL.

* **Opsgenie (if enabled):**
    If you set `cr.notifications.opsgenie.enabled: true`, you also need to configure:
    * `cr.notifications.opsgenie.apiKeySecretName`: The name of the Kubernetes Secret that stores your Opsgenie API key (e.g., `opsgenie-cert-monitor-apikey`).
    * `cr.notifications.opsgenie.apiKeySecretKey`: The key within that Kubernetes Secret which holds the API key value (e.g., `apiKey`).
    * `cr.notifications.opsgenie.region` (optional): Your Opsgenie region (e.g., "EU", "US") if applicable for the API.

    **Example Kubernetes Secret for Opsgenie API Key:**
    You would need to create a secret like this in your Kubernetes cluster:
    ```yaml
    # opsgenie-credentials.yaml
    apiVersion: v1
    kind: Secret
    metadata:
      name: opsgenie-cert-monitor-apikey # This name should match apiKeySecretName in values.yaml
      namespace: operator # Or the namespace where the operator runs/can access secrets
    type: Opaque
    stringData:
      apiKey: "your-actual-opsgenie-api-key" # This key should match apiKeySecretKey
    ```
    Apply it with `kubectl apply -f opsgenie-credentials.yaml -n operator`.

**Note on Filtering (`namespaceSelector`, `secretSelector`):**
The operator might support filtering which certificates to monitor based on namespace selectors or secret label selectors. If so, these would be configured in your `values.yaml`, likely under the `cr:` block or a similar section that defines the spec for the operator's Custom Resource or main configuration. Refer to the Helm chart's specific `values.yaml` documentation or comments for how to configure these. Example (structure may vary):
```yaml
# values.yaml (example snippet for filtering if supported by the chart)
# cr:
#   namespaceSelector:
#     matchLabels:
#       environment: "production"
#       monitoring: "enabled"
#   secretSelector:
#     matchLabels:
#       certificateGroup: "external-services"
``````

## Visualizing Certificate Expiry Information
The CertificateMonitor operator populates the .status.monitoredCertificates array in its Custom Resource (CR) instance with detailed information about all certificates it discovers and tracks. You can use kubectl combined with tools like jq (a command-line JSON processor) and column to query, filter, sort, and display this information in various ways.

The following examples assume:

Your CertificateMonitor CR is named cluster-wide-cert-check.
It is deployed in the operator namespace.
You have jq installed. If not, you can usually install it via your system's package manager (e.g., sudo apt install jq, brew install jq).
Adjust the CR name and namespace in the commands as necessary.

Common Fields in Output:
For clarity, most examples will output the following fields:

- NAMESPACE
- SECRET_NAME
- SECRET_KEY (the key within the Secret, e.g., tls.crt)
- SUBJECT_CN (Common Name of the certificate subject)
- DAYS_LEFT (Days until expiration; negative if expired)
- NEAR_EXPIRY (true if within renewalThresholdDays and not expired)
- EXPIRED (true if the certificate has passed its NotAfter date)
- EXPIRES_AT (The full expiration timestamp)

### List All Monitored Certificates, Sorted by Urgency (Most Urgent First)

This command displays all certificates, with expired ones appearing first, followed by those closest to expiration.
``````bash
( \
echo -e "NAMESPACE\tSECRET_NAME\tSECRET_KEY\tSUBJECT_CN\tDAYS_LEFT\tNEAR_EXPIRY\tEXPIRED\tEXPIRES_AT"; \
kubectl get certificatemonitor cluster-wide-cert-check -n operator -o json | \
jq -r '.status.monitoredCertificates | sort_by(.daysUntilExpiration) | .[] | [.namespace, .secretName, .secretKey, (.subjectCN // "<N/A>"), .daysUntilExpiration, .isNearExpiration, .isExpired, .expiresAt] | @tsv' \
) | column -t -s $'\t'
``````

### List Only Certificates Nearing Expiration or Already Expired, Sorted by Urgency

This filters the list to show only actionable certificates

``````bash
( \
echo -e "NAMESPACE\tSECRET_NAME\tSECRET_KEY\tSUBJECT_CN\tDAYS_LEFT\tNEAR_EXPIRY\tEXPIRED\tEXPIRES_AT"; \
kubectl get certificatemonitor cluster-wide-cert-check -n operator -o json | \
jq -r '.status.monitoredCertificates | map(select(.isNearExpiration == true or .isExpired == true)) | sort_by(.daysUntilExpiration) | .[] | [.namespace, .secretName, .secretKey, (.subjectCN // "<N/A>"), .daysUntilExpiration, .isNearExpiration, .isExpired, .expiresAt] | @tsv' \
) | column -t -s $'\t'
``````

### List Non-Expired Certificates, Sorted by Days Until Expiration (Closest to Expiry First)

This focuses on certificates that are still valid but approaching their renewal threshold.
``````bash
( \
echo -e "NAMESPACE\tSECRET_NAME\tSECRET_KEY\tSUBJECT_CN\tDAYS_LEFT\tNEAR_EXPIRY\tEXPIRES_AT"; \
kubectl get certificatemonitor cluster-wide-cert-check -n operator -o json | \
jq -r '.status.monitoredCertificates | map(select(.daysUntilExpiration >= 0)) | sort_by(.daysUntilExpiration) | .[] | [.namespace, .secretName, .secretKey, (.subjectCN // "<N/A>"), .daysUntilExpiration, (.isNearExpiration // false), .expiresAt] | @tsv' \
) | column -t -s $'\t'
``````

### List All Certificates, Grouped by Namespace, then Sorted by Urgency
``````bash
( \
echo -e "NAMESPACE\tSECRET_NAME\tSECRET_KEY\tSUBJECT_CN\tDAYS_LEFT\tNEAR_EXPIRY\tEXPIRED\tEXPIRES_AT"; \
kubectl get certificatemonitor cluster-wide-cert-check -n operator -o json | \
jq -r '.status.monitoredCertificates | sort_by([.namespace, .daysUntilExpiration]) | .[] | [.namespace, .secretName, .secretKey, (.subjectCN // "<N/A>"), .daysUntilExpiration, (.isNearExpiration // false), (.isExpired // false), .expiresAt] | @tsv' \
) | column -t -s $'\t'
``````

### List Certificates for a Specific Namespace, Sorted by Urgency
``````bash
TARGET_NAMESPACE="your-target-namespace"
( \
echo -e "NAMESPACE\tSECRET_NAME\tSECRET_KEY\tSUBJECT_CN\tDAYS_LEFT\tNEAR_EXPIRY\tEXPIRED\tEXPIRES_AT"; \
kubectl get certificatemonitor cluster-wide-cert-check -n operator -o json | \
jq -r --arg ns "$TARGET_NAMESPACE" '.status.monitoredCertificates | map(select(.namespace == $ns)) | sort_by(.daysUntilExpiration) | .[] | [.namespace, .secretName, .secretKey, (.subjectCN // "<N/A>"), .daysUntilExpiration, (.isNearExpiration // false), (.isExpired // false), .expiresAt] | @tsv' \
) | column -t -s $'\t'
``````

### List Certificates for a Specific Secret Name (Across All Monitored Namespaces), Sorted by Urgency
``````bash
TARGET_SECRET_NAME="your-target-secret-name"
( \
echo -e "NAMESPACE\tSECRET_NAME\tSECRET_KEY\tSUBJECT_CN\tDAYS_LEFT\tNEAR_EXPIRY\tEXPIRED\tEXPIRES_AT"; \
kubectl get certificatemonitor cluster-wide-cert-check -n operator -o json | \
jq -r --arg secret "$TARGET_SECRET_NAME" '.status.monitoredCertificates | map(select(.secretName == $secret)) | sort_by(.daysUntilExpiration) | .[] | [.namespace, .secretName, .secretKey, (.subjectCN // "<N/A>"), .daysUntilExpiration, (.isNearExpiration // false), (.isExpired // false), .expiresAt] | @tsv' \
) | column -t -s $'\t'
``````

### Count of Certificates Nearing Expiration or Already Expired
``````bash
RENEWAL_THRESHOLD_FOR_QUERY=30
kubectl get certificatemonitor cluster-wide-cert-check -n operator -o json | \
jq --argjson threshold "$RENEWAL_THRESHOLD_FOR_QUERY" \
   '.status.monitoredCertificates | map(select(.daysUntilExpiration < 0 or (.daysUntilExpiration >= 0 and .daysUntilExpiration <= $threshold))) | length'
``````
### List Certificates Expiring in the Next X Days (e.g., 7 days)
``````bash
DAYS_WINDOW=70
( \
echo -e "NAMESPACE\tSECRET_NAME\tSECRET_KEY\tSUBJECT_CN\tDAYS_LEFT\tNEAR_EXPIRY\tEXPIRED\tEXPIRES_AT"; \
kubectl get certificatemonitor cluster-wide-cert-check -n operator -o json | \
jq -r --argjson days "$DAYS_WINDOW" '.status.monitoredCertificates | map(select(.daysUntilExpiration >= 0 and .daysUntilExpiration <= $days)) | sort_by(.daysUntilExpiration) | .[] | [.namespace, .secretName, .secretKey, (.subjectCN // "<N/A>"), .daysUntilExpiration, (.isNearExpiration // false), (.isExpired // false), .expiresAt] | @tsv' \
) | column -t -s $'\t'
``````

