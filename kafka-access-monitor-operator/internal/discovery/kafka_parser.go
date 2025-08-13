package discovery

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v2"

	accessmonitorv1alpha1 "github.com/masmovil/mm-monorepo/pkg/runtime/operators/kafka-access-monitor-operator/api/v1alpha1"
)

type KafkaConfigDetails struct {
	Brokers              []string
	SchemaRegistryURL    string
	ClientType           string
	ClientID             string
	GroupID              string
	AutoOffsetReset      string
	AutoCommit           *bool
	Topics               []accessmonitorv1alpha1.TopicInfo
	MaxPollRecords       int
	MaxPollIntervalMs    int
	AutoCommitIntervalMs int
	ConsumerGroupSuffix  string
}

// ParsingRules define the structure of the configuration file for parsing rules.
type ParsingRules struct {
	Rules map[string][]string `yaml:"parsingRules"`
}

// DynamicParser contains the loaded parsing rules and performs the parsing.
type DynamicParser struct {
	rules ParsingRules
	log   logr.Logger
}

// NewDynamicParser reads the rules from a file and creates a new parser.
func NewDynamicParser(configPath string, log logr.Logger) (*DynamicParser, error) {
	log.Info("Loading dynamic parser rules", "path", configPath)
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read parser config file: %w", err)
	}

	var config struct {
		Rules map[string][]string `yaml:"parsingRules"`
	}
	if err := yaml.Unmarshal(yamlFile, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal parser rules: %w", err)
	}

	log.Info("Dynamic parser rules loaded successfully", "rulesCount", len(config.Rules))
	return &DynamicParser{rules: ParsingRules{Rules: config.Rules}, log: log}, nil
}

// Parse proces the input YAML content using the loaded rules.
func (p *DynamicParser) Parse(yamlContent string) (*KafkaConfigDetails, bool) {
	var data map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &data); err != nil {
		p.log.Error(err, "Failed to unmarshal input YAML into generic map")
		return nil, false
	}

	if kafkaData, found := findKafkaRoot(data); found {
		data = kafkaData
	}

	// We use the rules to extract each value
	brokers := p.getStringValue(data, "bootstrapServers")
	if brokers == "" {
		host := p.getStringValue(data, "host")
		port := p.getStringValue(data, "port")
		if host != "" && port != "" {
			brokers = fmt.Sprintf("%s:%s", host, port)
		}
	}

	if brokers == "" {
		return nil, false // Brokers is the only mandatory field
	}

	details := &KafkaConfigDetails{
		Brokers:              strings.Split(brokers, ","),
		SchemaRegistryURL:    p.getStringValue(data, "schemaRegistryUrl"),
		GroupID:              p.getStringValue(data, "groupId"),
		ClientID:             p.getStringValue(data, "clientId"),
		AutoOffsetReset:      strings.ToLower(p.getStringValue(data, "autoOffsetReset")),
		AutoCommit:           p.getBoolValue(data, "autoCommit"),
		MaxPollRecords:       p.getIntValue(data, "maxPollRecords"),
		MaxPollIntervalMs:    p.getIntValue(data, "maxPollIntervalMs"),
		AutoCommitIntervalMs: p.getIntValue(data, "autoCommitIntervalMs"),
		ConsumerGroupSuffix:  p.getStringValue(data, "consumerGroupSuffix"),
	}

	if details.GroupID != "" {
		details.ClientType = "Consumer"
	} else if details.ClientID != "" {
		details.ClientType = "Producer"
	} else {
		details.ClientType = "Unknown"
	}

	// Parse the topics, searching in the keys 'topics' or 'topic'.
	var topics []accessmonitorv1alpha1.TopicInfo
	// First, try to find in the plural key "topics".
	if topicsRaw, found := getValueFromPath(data, "topics"); found {
		if topicsMap, ok := topicsRaw.(map[interface{}]interface{}); ok {
			topics = parseTopics(p.log, convertKeysToStrings(topicsMap))
		}
		// If not, try to search in the singular key "topic".
	} else if singleTopicRaw, found := getValueFromPath(data, "topic"); found {
		if topicMap, ok := singleTopicRaw.(map[interface{}]interface{}); ok {
			topics = parseTopics(p.log, convertKeysToStrings(topicMap))
		}
	}
	details.Topics = topics

	return details, true
}

// --- Helper Functions for the Dynamic Parser ---
func findKafkaRoot(data map[string]interface{}) (map[string]interface{}, bool) {
	if kafka, ok := data["kafka"].(map[string]interface{}); ok {
		return kafka, true
	}
	if kafka, ok := data["kafka"].(map[interface{}]interface{}); ok {
		return convertKeysToStrings(kafka), true
	}
	if tp, ok := data["thirdParties"].(map[interface{}]interface{}); ok {
		if kafka, ok := tp["kafka"].(map[interface{}]interface{}); ok {
			return convertKeysToStrings(kafka), true
		}
	}
	return data, false // Returns the original map if no nesting is found
}

func (p *DynamicParser) getStringValue(data map[string]interface{}, fieldKey string) string {
	paths, ok := p.rules.Rules[fieldKey]
	if !ok {
		return ""
	}

	for _, path := range paths {
		if val, found := getValueFromPath(data, path); found {
			if strVal, ok := val.(string); ok {
				return strVal
			}
			if intVal, ok := val.(int); ok {
				return strconv.Itoa(intVal)
			}
		}
	}
	return ""
}

func (p *DynamicParser) getBoolValue(data map[string]interface{}, fieldKey string) *bool {
	paths, ok := p.rules.Rules[fieldKey]
	if !ok {
		return nil
	}

	for _, path := range paths {
		if val, found := getValueFromPath(data, path); found {
			if boolVal, ok := val.(bool); ok {
				return &boolVal
			}
		}
	}
	return nil
}

func (p *DynamicParser) getIntValue(data map[string]interface{}, fieldKey string) int {
	paths, ok := p.rules.Rules[fieldKey]
	if !ok {
		return 0
	}

	for _, path := range paths {
		if val, found := getValueFromPath(data, path); found {
			if intVal, ok := val.(int); ok {
				return intVal
			}
		}
	}
	return 0
}

// getValueFromPath search in a nested map using "key.subkey".
// Not case sensitive, and not underscore sensitive return the value if found, or nil and false if not found.
func getValueFromPath(data map[string]interface{}, path string) (interface{}, bool) {
	keys := strings.Split(path, ".")
	var current interface{} = data

	for _, searchKey := range keys {
		currentMap, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		foundInLevel := false
		for dataKey, val := range currentMap {
			if strings.EqualFold(dataKey, searchKey) {
				current = val
				foundInLevel = true
				break
			}
		}
		if !foundInLevel {
			return nil, false
		}
	}
	return current, true
}

// normalizeKey converts a key string to a canonical format for comparison
// by making it lowercase and removing hyphens and underscores.
func normalizeKey(key string) string {
	s := strings.ToLower(key)
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "_", "")
	return s
}

func convertKeysToStrings(original map[interface{}]interface{}) map[string]interface{} {
	converted := make(map[string]interface{})
	for key, value := range original {
		if keyStr, ok := key.(string); ok {
			if subMap, ok := value.(map[interface{}]interface{}); ok {
				converted[keyStr] = convertKeysToStrings(subMap)
			} else {
				converted[keyStr] = value
			}
		}
	}
	return converted
}

// parseTopics is the entry point to start the recursive parsing of topics.
func parseTopics(log logr.Logger, topicsData map[string]interface{}) []accessmonitorv1alpha1.TopicInfo {
	var results []accessmonitorv1alpha1.TopicInfo
	if topicsData == nil {
		return results
	}
	// Inicia el proceso recursivo sin un topic padre inicial.
	recursiveParse(log, topicsData, accessmonitorv1alpha1.TopicInfo{}, &results)
	return results
}

// recursiveParse traverses the nested map to find all complete topic definitions.
func recursiveParse(log logr.Logger, data map[string]interface{}, parentTopic accessmonitorv1alpha1.TopicInfo, results *[]accessmonitorv1alpha1.TopicInfo) {
	// Creates a copy of the parent topic to inherit its fields (such as cluster and domain).
	currentTopic := parentTopic

	// Extracts the known fields at the current level of the YAML.
	if cluster, ok := data["cluster"].(string); ok {
		currentTopic.Cluster = cluster
	}
	if domain, ok := data["domain"].(string); ok {
		currentTopic.Domain = domain
	}
	// Handles the alternative name 'productDomain'.
	if domain, ok := data["productDomain"].(string); ok {
		currentTopic.Domain = domain
	}

	// A map is considered a "final topic" if it contains "subject" (or "name") and "version".
	subject, subjectOK := data["subject"].(string)
	name, nameOK := data["name"].(string)
	version, versionOK := data["version"].(string)
	version5, version5OK := data["version5"].(string)

	isLeafNode := (subjectOK || nameOK) && (versionOK || version5OK)

	if isLeafNode {
		if subjectOK {
			currentTopic.Subject = subject
		} else {
			currentTopic.Subject = name
		}

		if versionOK {
			currentTopic.Version = version
		} else {
			currentTopic.Version = version5
		}

		log.V(1).Info("Found complete leaf topic definition", "topic", currentTopic)
		*results = append(*results, currentTopic)
	}

	// If it is not a "final topic", continue searching in the sub-levels.
	if !isLeafNode {
		for key, value := range data {
			// If a value is itself a map, it is a sub-level that needs to be explored.
			if nextMap, ok := value.(map[interface{}]interface{}); ok {
				log.V(1).Info("Recursing into child map", "key", key)
				// We convert the map and call the function recursively,
				// passing the 'currentTopic' so that the children inherit the context.
				recursiveParse(log, convertKeysToStrings(nextMap), currentTopic, results)
			}
		}
	}
}
