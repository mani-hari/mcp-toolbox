---
title: cloud-sql-mssql-connect-gke
type: docs
weight: 13
description: Connect a Cloud SQL for SQL Server instance to a Google Kubernetes Engine cluster — validates Workload Identity and network configuration, recommends a connection method, and emits Kubernetes manifests plus an optional code snippet.
---

## About

The `cloud-sql-mssql-connect-gke` tool helps an agent (or user) wire up a
Cloud SQL for SQL Server instance to a Google Kubernetes Engine cluster. It:

1. Reads the Cloud SQL instance via the `cloud-sql-admin` source and the
   target GKE cluster via the Container API (auto-discovering the cluster
   location with a `locations/-` wildcard list when `cluster_location` is
   omitted).
2. Validates Workload Identity and network configuration between the two
   (same-VPC / private-IP posture, VPC-native cluster, WI enablement).
3. Recommends a primary connection method — for SQL Server this is the
   Cloud SQL Auth Proxy sidecar. (The Cloud SQL Connector library supports
   Postgres and MySQL only.)
4. Returns ready-to-paste Kubernetes manifests, environment/DSN/JDBC
   snippets, and — when the caller passes `language` — a Python / Node.js /
   Java / Go code snippet using the appropriate driver.

**Identity boundary:** the Cloud SQL Admin call is made with the caller's
access token (via the configured `cloud-sql-admin` source), but the GKE
Container call is made with the toolbox's Application Default Credentials.
A caller without `container.clusters.get` will still see cluster info in
the result.

All identifiers that flow into shell commands, DSNs, or Kubernetes manifests
are validated against GCP naming rules before use.

## Compatible Sources

{{< compatible-sources >}}

## Requirements

- The service account executing Toolbox needs `roles/cloudsql.client` on the
  Cloud SQL instance and read access on the GKE cluster.
- The Cloud SQL Admin API (`sqladmin.googleapis.com`), Kubernetes Engine API
  (`container.googleapis.com`), and IAM Credentials API
  (`iamcredentials.googleapis.com`) must be enabled.
- For the Auth Proxy sidecar recommendation, the target namespace should be
  configured for Workload Identity with `roles/iam.workloadIdentityUser`
  bound to the appropriate Kubernetes service account.

## Parameters

| **parameter**            | **type** | **required** | **description**                                                                                                        |
| ------------------------ | :------: | :----------: | ---------------------------------------------------------------------------------------------------------------------- |
| instance_connection_name |  string  |     true     | Cloud SQL instance connection name in the format `project:region:instance`.                                            |
| cluster_name             |  string  |     true     | Name of the GKE cluster to connect from.                                                                               |
| cluster_location         |  string  |    false     | Location (zone or region) of the GKE cluster. If omitted, the tool searches all locations in the project via `locations/-`. |
| namespace                |  string  |    false     | Kubernetes namespace for deployment. Defaults to `default`.                                                            |
| database_name            |  string  |    false     | Database name to connect to. Defaults to `master`.                                                                     |
| language                 |  string  |    false     | Programming language for a code snippet: `python`, `nodejs`, `java`, or `go`. Omit to skip snippet output.             |

## Example

```yaml
sources:
  my-cloud-sql-admin-source:
    kind: cloud-sql-admin

tools:
  connect_to_gke:
    kind: tool
    type: cloud-sql-mssql-connect-gke
    source: my-cloud-sql-admin-source
    description: Help me connect a Cloud SQL SQL Server instance to a GKE cluster.
```

## Output Format

The tool returns a JSON object containing:

- `instanceConnectionName`, `project`, `region`, `databaseType`,
  `databaseVersion`
- `computeType` (`gke`), `computeResource` (the cluster name),
  `computeLocation` (the resolved cluster location)
- `validation` — checks performed and their status
- `recommendedMethod` and `alternativeMethods` — typically `auth_proxy`,
  with rationale and requirements
- `connectionStrings` — host/port/DSN/JDBC templates (credential
  placeholders are `USER` / `PASS`)
- `environmentConfig` — env vars, Auth Proxy launch command, and
  sidecar/secret Kubernetes YAML where applicable
- `setupSteps` — ordered actions the user needs to take (`gcloud`, `kubectl`)
- `codeSnippet` — populated only when `language` was set
- `requiredIamRoles`, `requiredApis`

## Reference

### Tool Configuration

| **field**   | **type** | **required** | **description**                                  |
| ----------- | :------: | :----------: | ------------------------------------------------ |
| type        |  string  |     true     | Must be `cloud-sql-mssql-connect-gke`.           |
| source      |  string  |     true     | The name of the `cloud-sql-admin` source to use. |
| description |  string  |    false     | Overrides the default tool description.          |
