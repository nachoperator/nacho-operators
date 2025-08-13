package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	accessmonitorv1alpha1 "github.com/masmovil/mm-monorepo/pkg/runtime/operators/kafka-access-monitor-operator/api/v1alpha1"
)

// --- Structs for the Gemini API Request and Response ---

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
			Role  string       `json:"role"`
		} `json:"content"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason   string `json:"blockReason"`
		SafetyRatings []struct {
			Category    string `json:"category"`
			Probability string `json:"probability"`
		} `json:"safetyRatings"`
	} `json:"promptFeedback"`
}

// --- Analyzer Interface and Struct ---

// Analyzer defines the interface for an AI-based problem analyzer.
type Analyzer interface {
	Analyze(ctx context.Context, podInfo accessmonitorv1alpha1.PodAccessInfo, problem accessmonitorv1alpha1.ProblemDetail) (string, error)
}

// GeminiAnalyzer implements the Analyzer interface using Google's Gemini API.
type GeminiAnalyzer struct {
	apiKey     string
	httpClient *http.Client
	model      string
	log        logr.Logger
}

// NewGeminiAnalyzer creates a new instance of GeminiAnalyzer.
// The API Key should be obtained from a Kubernetes secret, not hardcoded.
func NewGeminiAnalyzer(apiKey string, log logr.Logger) Analyzer {
	return &GeminiAnalyzer{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 45 * time.Second, // AI API calls can be slow
		},
		model: "gemini-1.5-flash", // A fast and efficient model
		log:   log,
	}
}

// Analyze performs the full process of building a prompt and calling the Gemini API.
func (g *GeminiAnalyzer) Analyze(ctx context.Context, podInfo accessmonitorv1alpha1.PodAccessInfo, problem accessmonitorv1alpha1.ProblemDetail) (string, error) {
	g.log.Info("Starting AI analysis for pod problem", "pod", podInfo.PodName, "problem", problem.Message)
	prompt := g.buildPrompt(podInfo, problem)
	return g.callAPI(ctx, prompt)
}

// buildPrompt creates the detailed text prompt to be sent to the AI.
func (g *GeminiAnalyzer) buildPrompt(podInfo accessmonitorv1alpha1.PodAccessInfo, problem accessmonitorv1alpha1.ProblemDetail) string {
	// We build a string representation of the topics for the prompt
	var topicsStr []string
	for _, t := range podInfo.Topics {
		topicsStr = append(topicsStr, fmt.Sprintf("- %s/%s/%s", t.Cluster, t.Domain, t.Subject))
	}
	topicsFormatted := strings.Join(topicsStr, "\n")
	if len(topicsFormatted) == 0 {
		topicsFormatted = "N/A"
	}

	brokerLogsSnippet := "N/A"
	if len(problem.BrokerLogSnippets) > 0 {
		brokerLogsSnippet = "\n- " + strings.Join(problem.BrokerLogSnippets, "\n- ")
	}

	return fmt.Sprintf(`
	You are a Kubernetes and Kafka expert. Analyze the following problem detected in a pod and provide a structured response.
	
	**Pod Context:**
	- **Name:** %s
	- **Namespace:** %s
	- **Kafka Client Type:** %s
	- **Group ID:** %s
	- **Detected Broker Endpoints:** %s
	- **Detected Topics:**
	%s
	
	**Logged Problem:**
	- **Severity:** %s
	- **Message:** %s
	- **Occurrences:** %d
	
	**Broker Log Snippets:**
	%s
	
	**Your Task:**
	Based on this information, provide an analysis in two parts:
	1.  **Probable Root Cause:** Describe in 1-2 sentences what the most likely cause of the error might be.
	2.  **Recommendations:** Offer 2-3 concrete tips or improvements (e.g., configuration, code best practices, resilience) for this type of Kafka producer/consumer.
	`,
		podInfo.PodName, podInfo.Namespace, podInfo.ClientType, podInfo.GroupID,
		strings.Join(podInfo.DetectedBrokerEndpoints, ", "), topicsFormatted,
		problem.Type, problem.Message, problem.Count,
		brokerLogsSnippet,
	)
}

// callAPI makes the actual HTTP POST request to the Gemini API.
func (g *GeminiAnalyzer) callAPI(ctx context.Context, prompt string) (string, error) {
	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", g.model, g.apiKey)

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		g.log.Error(err, "Failed to marshal request body")
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		g.log.Error(err, "Failed to create API request")
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	g.log.Info("Calling Gemini API", "model", g.model)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		g.log.Error(err, "API call failed")
		return "", fmt.Errorf("API call failed: %w", err)
	}
	defer resp.Body.Close()

	g.log.Info("Received response from Gemini API", "statusCode", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("API returned non-200 status: %s", resp.Status)
		g.log.Error(err, "Gemini API error")
		return "", err
	}

	var geminiResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		g.log.Error(err, "Failed to decode response body")
		return "", fmt.Errorf("failed to decode response body: %w", err)
	}

	if len(geminiResp.Candidates) > 0 && len(geminiResp.Candidates[0].Content.Parts) > 0 {
		g.log.Info("Successfully extracted analysis from API response")
		return geminiResp.Candidates[0].Content.Parts[0].Text, nil
	}

	if geminiResp.PromptFeedback.BlockReason != "" {
		err := fmt.Errorf("prompt was blocked by the API, reason: %s", geminiResp.PromptFeedback.BlockReason)
		g.log.Error(err, "Gemini API content moderation")
		return "", err
	}

	err = errors.New("no content received from AI API")
	g.log.Error(err, "Empty response from Gemini")
	return "", err
}
