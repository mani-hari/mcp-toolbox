// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package cloudsqlpgconnectgke provides a tool that connects a Cloud SQL for
// PostgreSQL instance to a Google Kubernetes Engine cluster: it validates
// Workload Identity and network configuration, recommends an Auth Proxy
// sidecar (or Connector library) deployment, and emits ready-to-paste
// Kubernetes manifests and an optional language-specific code snippet.
//
// Identity boundary: the Cloud SQL Admin call is made with the caller's
// access token (via the configured cloud-sql-admin source), but the GKE
// Container call is made with the toolbox's Application Default Credentials
// against CloudPlatformReadOnly scope. A caller without
// container.clusters.get will still see cluster info reflected in the
// result. This matches the behavior of the GCE-target sibling tool; a
// future change can thread the caller token through option.WithTokenSource
// for both calls.
package cloudsqlpgconnectgke

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	yaml "github.com/goccy/go-yaml"
	"github.com/googleapis/mcp-toolbox/internal/sources"
	"github.com/googleapis/mcp-toolbox/internal/tools"
	"github.com/googleapis/mcp-toolbox/internal/util"
	"github.com/googleapis/mcp-toolbox/internal/util/cloudsqlconnect"
	"github.com/googleapis/mcp-toolbox/internal/util/parameters"
	container "google.golang.org/api/container/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

const resourceType string = "cloud-sql-postgres-connect-gke"

func init() {
	if !tools.Register(resourceType, newConfig) {
		panic(fmt.Sprintf("tool type %q already registered", resourceType))
	}
}

func newConfig(ctx context.Context, name string, decoder *yaml.Decoder) (tools.ToolConfig, error) {
	actual := Config{ConfigBase: tools.ConfigBase{Name: name}}
	if err := decoder.DecodeContext(ctx, &actual); err != nil {
		return nil, err
	}
	return actual, nil
}

type compatibleSource interface {
	GetService(context.Context, string) (*sqladmin.Service, error)
	UseClientAuthorization() bool
}

// Config defines the configuration for the cloud-sql-postgres-connect-gke tool.
type Config struct {
	tools.ConfigBase `yaml:",inline"`
	Type             string                 `yaml:"type" validate:"required"`
	Source           string                 `yaml:"source" validate:"required"`
	Annotations      *tools.ToolAnnotations `yaml:"annotations,omitempty"`
}

var _ tools.ToolConfig = Config{}

// ToolConfigType returns the type of the tool.
func (cfg Config) ToolConfigType() string { return resourceType }

// Initialize initializes the tool from the configuration.
func (cfg Config) Initialize(context.Context) (tools.Tool, error) {
	if cfg.Description == "" {
		cfg.Description = "Helps connect a Cloud SQL PostgreSQL instance to a GKE cluster. " +
			"Validates Workload Identity and network configuration, recommends Auth Proxy " +
			"sidecar setup, and provides Kubernetes manifests and optional code snippets."
	}
	allParameters := buildParams()
	return Tool{
		BaseTool: tools.NewBaseTool(
			cfg,
			tools.GetAnnotationsOrDefault(cfg.Annotations, tools.NewReadOnlyAnnotations),
			tools.Manifest{Description: cfg.Description, Parameters: allParameters.Manifest(), AuthRequired: cfg.AuthRequired},
			allParameters,
		),
	}, nil
}

func buildParams() parameters.Parameters {
	return parameters.Parameters{
		parameters.NewStringParameter(
			"instance_connection_name",
			"Cloud SQL instance connection name in the format: project:region:instance",
		),
		parameters.NewStringParameter(
			"cluster_name",
			"Name of the GKE cluster to connect from",
		),
		parameters.NewStringParameter(
			"cluster_location",
			"Location (zone or region) of the GKE cluster (optional - will auto-discover if not provided)",
			parameters.WithStringDefault(""),
		),
		parameters.NewStringParameter(
			"namespace",
			"Kubernetes namespace for deployment (defaults to 'default')",
			parameters.WithStringDefault("default"),
		),
		parameters.NewStringParameter(
			"database_name",
			"Database name to connect to (defaults to 'postgres')",
			parameters.WithStringDefault("postgres"),
		),
		parameters.NewStringParameter(
			"language",
			"Programming language for code snippet generation: python, nodejs, java, go (optional)",
			parameters.WithStringDefault(""),
		),
	}
}

// Tool represents the cloud-sql-postgres-connect-gke tool.
type Tool struct {
	tools.BaseTool[Config]
}

func (t Tool) GetSourceName() string { return t.Cfg.Source }

func (t Tool) ToConfig() tools.ToolConfig { return t.Cfg }

func (t Tool) ValidateSource(source sources.Source) error {
	_, ok := source.(compatibleSource)
	if !ok {
		return fmt.Errorf("invalid source for %q tool: source %q is not a compatible type", t.Cfg.Type, t.Cfg.Source)
	}
	return nil
}

// Invoke validates connectivity between a Cloud SQL Postgres instance and a
// GKE cluster, then returns connection recommendations.
func (t Tool) Invoke(ctx context.Context, s sources.Source, params parameters.ParamValues, accessToken tools.AccessToken) (any, util.ToolboxError) {
	source, ok := s.(compatibleSource)
	if !ok {
		return nil, util.NewClientServerError("source used is not compatible with the tool", http.StatusInternalServerError, nil)
	}

	sqlService, err := source.GetService(ctx, string(accessToken))
	if err != nil {
		return nil, util.ProcessGcpError(err)
	}

	containerService, err := cloudsqlconnect.GetContainerService()
	if err != nil {
		return nil, util.NewClientServerError("failed to initialize Container service", http.StatusInternalServerError, err)
	}

	paramsMap := params.AsMap()

	connName, ok := paramsMap["instance_connection_name"].(string)
	if !ok || connName == "" {
		return nil, util.NewAgentError("missing or empty 'instance_connection_name' parameter", nil)
	}

	clusterName, ok := paramsMap["cluster_name"].(string)
	if !ok || clusterName == "" {
		return nil, util.NewAgentError("missing or empty 'cluster_name' parameter", nil)
	}
	if err := cloudsqlconnect.ValidateGCEResourceName(clusterName, "cluster_name"); err != nil {
		return nil, util.NewAgentError(err.Error(), err)
	}

	clusterLocation, _ := paramsMap["cluster_location"].(string)
	if clusterLocation != "" {
		if err := cloudsqlconnect.ValidateGKELocation(clusterLocation); err != nil {
			return nil, util.NewAgentError(err.Error(), err)
		}
	}

	namespace, _ := paramsMap["namespace"].(string)
	if namespace == "" {
		namespace = "default"
	}
	if err := cloudsqlconnect.ValidateKubernetesNamespace(namespace); err != nil {
		return nil, util.NewAgentError(err.Error(), err)
	}

	dbName, _ := paramsMap["database_name"].(string)
	if dbName == "" {
		dbName = cloudsqlconnect.DefaultDatabaseName(cloudsqlconnect.PostgreSQL)
	}
	if err := cloudsqlconnect.ValidateDatabaseName(dbName); err != nil {
		return nil, util.NewAgentError(err.Error(), err)
	}

	language, _ := paramsMap["language"].(string)
	if err := cloudsqlconnect.ValidateLanguage(language); err != nil {
		return nil, util.NewAgentError(err.Error(), err)
	}

	project, region, instanceName, err := cloudsqlconnect.ValidateInstanceConnectionName(connName)
	if err != nil {
		return nil, util.NewAgentError(err.Error(), err)
	}

	sqlInstance, err := sqlService.Instances.Get(project, instanceName).Context(ctx).Do()
	if err != nil {
		return nil, util.ProcessGcpError(err)
	}
	sqlInfo := cloudsqlconnect.ExtractSQLInfo(sqlInstance)

	if err := cloudsqlconnect.AssertEngine(cloudsqlconnect.PostgreSQL, instanceName, sqlInfo.DatabaseVersion); err != nil {
		return nil, util.NewAgentError(err.Error(), err)
	}

	var cluster *container.Cluster
	if clusterLocation == "" {
		cluster, clusterLocation, err = cloudsqlconnect.FindCluster(ctx, containerService, project, clusterName)
		if err != nil {
			return nil, util.ProcessGcpError(err)
		}
	} else {
		clusterPath := fmt.Sprintf("projects/%s/locations/%s/clusters/%s", project, clusterLocation, clusterName)
		cluster, err = containerService.Projects.Locations.Clusters.Get(clusterPath).Context(ctx).Do()
		if err != nil {
			return nil, util.ProcessGcpError(err)
		}
	}
	clusterInfo := cloudsqlconnect.ExtractClusterInfo(cluster, clusterLocation)

	validation := cloudsqlconnect.ValidateGKEConnection(sqlInfo, clusterInfo)
	sameVPC := cloudsqlconnect.IsSameVPC(sqlInfo.VPCNetwork, clusterInfo.VPCNetwork)
	primary, alternatives := cloudsqlconnect.GetGKERecommendations(sqlInfo, clusterInfo, sameVPC)

	port := cloudsqlconnect.GetDatabasePort(cloudsqlconnect.PostgreSQL)
	setupSteps := cloudsqlconnect.GenerateGKESetupSteps(connName, port, project, namespace)
	envConfig := cloudsqlconnect.GenerateEnvironmentConfig(
		primary.Method, cloudsqlconnect.ComputeGKE, connName, port,
		sqlInfo.PrivateIPAddress, dbName, project,
	)
	connStrings := cloudsqlconnect.BuildConnectionStrings(primary.Method, cloudsqlconnect.PostgreSQL, sqlInfo, dbName, connName)

	result := &cloudsqlconnect.ConnectResult{
		InstanceConnectionName: connName,
		Project:                project,
		Region:                 region,
		DatabaseType:           cloudsqlconnect.PostgreSQL,
		DatabaseVersion:        sqlInfo.DatabaseVersion,
		ComputeType:            cloudsqlconnect.ComputeGKE,
		ComputeResource:        clusterName,
		ComputeLocation:        clusterLocation,
		Validation:             *validation,
		RecommendedMethod:      primary,
		AlternativeMethods:     alternatives,
		ConnectionStrings:      connStrings,
		EnvironmentConfig:      envConfig,
		SetupSteps:             setupSteps,
		AvailableLanguages:     cloudsqlconnect.AvailableLanguages,
		RequiredIAMRoles: []string{
			"roles/cloudsql.client",
			"roles/iam.workloadIdentityUser",
		},
		RequiredAPIs: []string{
			"sqladmin.googleapis.com",
			"container.googleapis.com",
			"iamcredentials.googleapis.com",
		},
	}

	if language != "" {
		lang := cloudsqlconnect.Language(strings.ToLower(language))
		result.CodeSnippet = cloudsqlconnect.GenerateCodeSnippet(
			lang, primary.Method, cloudsqlconnect.PostgreSQL,
			connName, dbName, port, sqlInfo.PrivateIPAddress,
		)
	}

	return result, nil
}

func (t Tool) RequiresClientAuthorization(source sources.Source) (bool, error) {
	s, ok := source.(compatibleSource)
	if !ok {
		return false, fmt.Errorf("invalid source for %q tool: source %q is not a compatible type", t.Cfg.Type, t.Cfg.Source)
	}
	return s.UseClientAuthorization(), nil
}
