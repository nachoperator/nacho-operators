### What's the focus of this PR?
This PR introduces the kafka-access-monitor-operator, a new Kubernetes operator designed to proactively monitor Kafka clients (producers and consumers), detect issues, and provide AI-driven root cause analysis.

The key features implemented are:

- Automatic Discovery: The operator discovers Kafka clients and parses their configuration from ConfigMaps and environment variables.
- Problem Detection: It actively scans pod logs and Kubernetes events to identify and diagnose common issues (DiagnosticsFinder).
- 360° Data Enrichment: When a problem is detected, the operator enriches the incident with correlated data:
- Broker Logs: Fetches relevant logs from Kafka brokers at the time of the incident (BrokerLogFinder).
- Broker Metrics: Queries Google Cloud Monitoring for broker performance metrics like CPU and network usage (MetricsFinder).
- Authorization Context: Gathers information on authorized tenants for the client's Service Account.
- AI Root Cause Analysis: It sends the consolidated incident data to Google's Generative Language API (Gemini) to get an expert-level analysis and actionable recommendations.
- Data Persistence & Reporting: All findings are logged as a structured KafkaAccessEvent to be ingested by a BigQuery sink for historical analysis. It also includes integration for Slack and Opsgenie reporting.


### How to review this PR?

- Verify that the operator logs show the successful fetching of broker logs and metrics.
- Confirm that the final KafkaAccessEvent log is generated with the enriched data, including the AI analysis.
- Check that the corresponding data is correctly loaded into the final BigQuery table.

### Related Issues
https://jiranext.masorange.es/browse/CLE-4691
  
### Type of changes
- [x] New feature

### Checklist
- [x] The PR relates to *only* one subject with a clear title.
- [x] I have modified the version
