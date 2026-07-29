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

package cloudsqlconnect

import (
	"context"
	"fmt"
	"sync"

	container "google.golang.org/api/container/v1"
	"google.golang.org/api/option"
)

// containerService is lazily initialized on first use and shared across
// invocations: *container.Service is goroutine-safe and rebuilding it per
// call would re-pay token-source + service-discovery costs.
var (
	containerOnce    sync.Once
	containerService *container.Service
	containerErr     error
)

// GetContainerService returns a process-wide GKE Container client, built
// once on first call. The initializer uses context.Background() on
// purpose: a request-scoped ctx cached inside sync.Once would poison
// every subsequent invocation if the first caller cancelled or timed
// out. Callers still propagate their request ctx to individual API
// calls via Clusters.Get(...).Context(ctx).Do().
func GetContainerService() (*container.Service, error) {
	containerOnce.Do(func() {
		containerService, containerErr = container.NewService(context.Background(), option.WithScopes(container.CloudPlatformReadOnlyScope))
	})
	return containerService, containerErr
}

// ExtractClusterInfo lifts the fields the connect tools need out of a
// GKE Cluster.
func ExtractClusterInfo(cluster *container.Cluster, location string) *GKEClusterInfo {
	info := &GKEClusterInfo{
		Name:     cluster.Name,
		Location: location,
	}
	if cluster.WorkloadIdentityConfig != nil {
		info.WorkloadIdentity = cluster.WorkloadIdentityConfig.WorkloadPool != ""
	}
	if cluster.IpAllocationPolicy != nil {
		info.VPCNative = cluster.IpAllocationPolicy.UseIpAliases
	}
	if cluster.PrivateClusterConfig != nil {
		info.PrivateCluster = cluster.PrivateClusterConfig.EnablePrivateNodes
	}
	info.VPCNetwork = ExtractNetworkName(cluster.Network)
	return info
}

// FindCluster resolves a GKE cluster by name across all locations in a
// project using the wildcard `locations/-` form. Returns the cluster, its
// location, and an error if the cluster is missing or ambiguous. Clusters.List
// with `locations/-` returns every matching cluster in one response, so we
// surface every match in the ambiguity error rather than truncating.
func FindCluster(ctx context.Context, service *container.Service, project, clusterName string) (*container.Cluster, string, error) {
	parent := fmt.Sprintf("projects/%s/locations/-", project)
	resp, err := service.Projects.Locations.Clusters.List(parent).Context(ctx).Do()
	if err != nil {
		return nil, "", fmt.Errorf("failed to list clusters: %w", err)
	}

	var found []*container.Cluster
	var locations []string
	for _, cluster := range resp.Clusters {
		if cluster.Name == clusterName {
			found = append(found, cluster)
			locations = append(locations, cluster.Location)
		}
	}

	switch len(found) {
	case 0:
		return nil, "", fmt.Errorf("GKE cluster %q not found in project %q", clusterName, project)
	case 1:
		return found[0], locations[0], nil
	default:
		return nil, "", fmt.Errorf("multiple clusters named %q found in locations: %v - please specify cluster_location parameter", clusterName, locations)
	}
}
