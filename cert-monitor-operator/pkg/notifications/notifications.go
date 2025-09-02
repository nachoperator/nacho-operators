package notifications

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/go-logr/logr"
	certmonitorv1alpha1 "github.com/nachoperator/nacho-operators/cert-monitor-operator/api/v1alpha1"
)

// SMTP server configuration (ensure these are correctly set or loaded from config/env)
var (
	smtpHost     = "smtp.example.com"   // TODO:
	smtpPort     = "587"                // TODO:
	smtpUsername = "alert@example.com"  // TODO:
	smtpPassword = "your-smtp-password" // TODO:
	fromEmail    = "alert@example.com"  // TODO:
)

// --- Opsgenie Constants ---
const (
	opsgenieDefaultAPIURL = "https://api.opsgenie.com/v2/alerts"
	opsgenieEUAPIURL      = "https://api.eu.opsgenie.com/v2/alerts"
)

// --- Opsgenie Structs ---
type OpsgenieAlertPayload struct {
	Message     string            `json:"message"`
	Alias       string            `json:"alias,omitempty"`
	Description string            `json:"description,omitempty"`
	Priority    string            `json:"priority,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Details     map[string]string `json:"details,omitempty"`
}

// SendAggregatedWebhookNotification sends a single webhook notification with an aggregated message.
func SendAggregatedWebhookNotification(log logr.Logger, endpoint string, aggregatedMessage string) error {
	log = log.WithValues("webhook", endpoint)
	log.Info("Sending aggregated webhook notification")

	// Create HTTP client
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	slackPayload := map[string]string{
		"text": aggregatedMessage,
	}
	requestBody, err := json.Marshal(slackPayload)
	if err != nil {
		log.Error(err, "Failed to marshal aggregated webhook request body")
		return err
	}

	// Create the POST request
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(requestBody))
	if err != nil {
		log.Error(err, "Failed to create aggregated HTTP request")
		return err
	}

	// Set Content-Type header
	req.Header.Set("Content-Type", "application/json")

	// Send the request
	resp, err := client.Do(req)
	if err != nil {
		log.Error(err, "Failed to send aggregated HTTP request")
		return err
	}
	defer resp.Body.Close()

	// Check the response status code
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Info("Aggregated webhook notification sent successfully", "status", resp.Status)
		return nil
	} else {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Error(fmt.Errorf("webhook request failed with status: %s, body: %s", resp.Status, string(bodyBytes)), "Aggregated webhook notification failed")
		return fmt.Errorf("webhook request failed with status: %s, body: %s", resp.Status, string(bodyBytes))
	}
}

// SendAggregatedEmailNotification sends a single email notification with an aggregated message.
func SendAggregatedEmailNotification(log logr.Logger, recipients []string, subject string, aggregatedMessage string) error {
	log = log.WithValues("recipients", recipients)
	log.Info("Sending aggregated email notification")

	if len(recipients) == 0 {
		log.Info("No recipients specified for aggregated email notification, skipping.")
		return nil
	}

	// Basic validation for SMTP config (optional but recommended)
	if smtpHost == "" || smtpPort == "" || smtpUsername == "" || smtpPassword == "" || fromEmail == "" {
		err := fmt.Errorf("SMTP configuration is incomplete. Please set smtpHost, smtpPort, smtpUsername, smtpPassword, and fromEmail variables")
		log.Error(err, "Cannot send email due to missing SMTP settings")
		return err
	}

	// Construct the email message
	var msg bytes.Buffer
	msg.WriteString("From: " + fromEmail + "\r\n")
	msg.WriteString("To: " + strings.Join(recipients, ",") + "\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("MIME-version: 1.0;\nContent-Type: text/plain; charset=\"UTF-8\";\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(aggregatedMessage)

	// Authenticate with the SMTP server
	auth := smtp.PlainAuth("", smtpUsername, smtpPassword, smtpHost)

	// Send the email
	err := smtp.SendMail(
		smtpHost+":"+smtpPort,
		auth,
		fromEmail,
		recipients,
		msg.Bytes(),
	)

	if err != nil {
		log.Error(err, "Failed to send aggregated email")
		return err
	}

	log.Info("Aggregated email notification sent successfully")
	return nil
}

// --- Opsgenie Notification Function ---

func SendOpsgenieAlert(
	log logr.Logger,
	apiKey string,
	region string,
	priority string,
	message string,
	description string,
	alias string,
	certs []certmonitorv1alpha1.MonitoredCertificateInfo,
	crNamespace string,
	crName string,
) error {
	apiURL := opsgenieDefaultAPIURL
	if strings.ToUpper(region) == "EU" {
		apiURL = opsgenieEUAPIURL
		log.Info("Using EU Opsgenie API endpoint", "url", apiURL)
	} else {
		log.Info("Using default (US) Opsgenie API endpoint", "url", apiURL)
	}

	if priority == "" {
		log.Info("Opsgenie alert priority not specified, defaulting to P3")
		priority = "P3"
	}

	// Constructing details for Opsgenie payload
	opsgenieDetails := map[string]string{
		"customResourceNamespace":   crNamespace,
		"customResourceName":        crName,
		"expiringCertificatesCount": fmt.Sprintf("%d", len(certs)),
	}

	payload := OpsgenieAlertPayload{
		Message:     message,
		Alias:       alias,
		Description: description,
		Priority:    priority,
		Tags:        []string{"kubernetes", "certificate-monitor", "operator", crNamespace, crName},
		Details:     opsgenieDetails,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Error(err, "Failed to marshal Opsgenie alert payload")
		return fmt.Errorf("failed to marshal Opsgenie payload: %w", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		log.Error(err, "Failed to create Opsgenie HTTP request")
		return fmt.Errorf("failed to create Opsgenie request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "GenieKey "+apiKey)

	log.Info("Sending Opsgenie alert", "alias", alias, "priority", priority, "message", message)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Error(err, "Failed to send Opsgenie alert request")
		return fmt.Errorf("failed to send Opsgenie alert request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			log.Error(readErr, "Failed to read Opsgenie error response body")
		}

		log.Error(fmt.Errorf(
			"Opsgenie API request failed with status code %d. Response: %s",
			resp.StatusCode, string(bodyBytes),
		), "Opsgenie API error")

		return fmt.Errorf(
			"Opsgenie API request failed with status code %d. Response: %s",
			resp.StatusCode, string(bodyBytes))
	}

	log.Info("Successfully sent alert to Opsgenie", "alias", alias, "statusCode", resp.StatusCode)
	return nil
}
