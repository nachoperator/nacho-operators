package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// --- Structs to parse the central authn ConfigMap ---

type TenantDetail struct {
	ID  int    `json:"id"`
	Org string `json:"org"`
}

type ServiceAccountDetail struct {
	ID                 string                  `json:"id"`
	ClientEmail        string                  `json:"client_email"`
	PreferredUsername  string                  `json:"preferred_username"`
	Tenants            map[string]TenantDetail `json:"tenants"`
}

type AuthnData struct {
	ServiceAccounts map[string]ServiceAccountDetail `json:"service_accounts"`
}

// AuthnEnricher is responsible for fetching and parsing the central authn ConfigMap.
type AuthnEnricher struct {
	Client client.Client
}

// NewAuthnEnricher creates a new instance of AuthnEnricher.
func NewAuthnEnricher(c client.Client) *AuthnEnricher {
	return &AuthnEnricher{Client: c}
}

// TenantLookupMap is a simplified map for easy lookups: Key -> client_email, Value -> list of tenant orgs.
type TenantLookupMap map[string][]string

// LoadAndGetTenantMap fetches the central authn ConfigMap and processes it into an easily searchable map.
func (f *AuthnEnricher) LoadAndGetTenantMap(ctx context.Context, env string) (TenantLookupMap, error) {
	log := log.FromContext(ctx)

	// 1. Construct the namespace and ConfigMap name based on the environment
	namespace := fmt.Sprintf("authn-%s", env)
	cmName := "authn-configmap-service-accounts-v2"
	
	log.Info("Loading central authn ConfigMap", "namespace", namespace, "name", cmName)

	// 2. Get the ConfigMap from the Kubernetes API
	cm := &corev1.ConfigMap{}
	err := f.Client.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, cm)
	if err != nil {
		// This might be a fatal error for the reconciliation, as no enrichment is possible.
		return nil, fmt.Errorf("failed to get central authn configmap %s in namespace %s: %w", cmName, namespace, err)
	}

	// 3. Extract the JSON content from the 'data' key
	jsonData, ok := cm.Data["data"]
	if !ok {
		return nil, fmt.Errorf("central authn configmap %s found, but key 'data' is missing", cmName)
	}

	// 4. Parse the JSON
	var authnData AuthnData
	if err := json.Unmarshal([]byte(jsonData), &authnData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON from authn configmap: %w", err)
	}

	// 5. Transform the parsed data into a simple lookup map for efficiency
	lookupMap := make(TenantLookupMap)
	for _, saDetails := range authnData.ServiceAccounts {
		var tenantOrgs []string
		for _, tenantInfo := range saDetails.Tenants {
			tenantOrgs = append(tenantOrgs, tenantInfo.Org)
		}
		// We can index by both email and preferred_username for flexible lookups
		lookupMap[strings.ToLower(saDetails.ClientEmail)] = tenantOrgs
		lookupMap[strings.ToLower(saDetails.PreferredUsername)] = tenantOrgs
	}

	log.Info("Successfully loaded and parsed authn configmap", "serviceAccountsLoaded", len(lookupMap)/2)
	return lookupMap, nil
}
