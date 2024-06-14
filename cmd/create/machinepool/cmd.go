/*
Copyright (c) 2020 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package machinepool

import (
	"context"
	"fmt"

	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/spf13/cobra"

	"github.com/openshift/rosa/pkg/aws"
	mpHelpers "github.com/openshift/rosa/pkg/helper/machinepools"
	"github.com/openshift/rosa/pkg/machinepool"
	mpOpts "github.com/openshift/rosa/pkg/options/machinepool"
	"github.com/openshift/rosa/pkg/properties"
	"github.com/openshift/rosa/pkg/rosa"
)

func NewCreateMachinePoolCommand() *cobra.Command {
	cmd, options := mpOpts.BuildMachinePoolCreateCommandWithOptions()
	cmd.Run = rosa.DefaultRunner(rosa.RuntimeWithOCM(), CreateMachinepoolRunner(options))
	return cmd
}

func CreateMachinepoolRunner(userOptions *machinepool.CreateMachinepoolUserOptions) rosa.CommandRunner {
	return func(ctx context.Context, r *rosa.Runtime, cmd *cobra.Command, argv []string) error {
		var err error
		options := NewCreateMachinepoolOptions()

		clusterKey := r.GetClusterKey()

		options.args = userOptions

		cluster := r.FetchCluster()
		if cluster.State() != cmv1.ClusterStateReady {
			return fmt.Errorf("Cluster '%s' is not yet ready", clusterKey)
		}

		val, ok := cluster.Properties()[properties.UseLocalCredentials]
		useLocalCredentials := ok && val == "true"

		if cmd.Flags().Changed("labels") {
			_, err := mpHelpers.ParseLabels(options.args.Labels)
			if err != nil {
				return fmt.Errorf("%s", err)
			}
		}

		// Initiate the AWS client with the cluster's region
		r.AWSClient, err = aws.NewClient().
			Region(cluster.Region().ID()).
			Logger(r.Logger).
			UseLocalCredentials(useLocalCredentials).
			Build()
		if err != nil {
			return fmt.Errorf("Failed to create awsClient: %s", err)
		}

		service := machinepool.NewMachinePoolService()
		if cluster.Hypershift().Enabled() {
			return service.CreateNodePools(r, cmd, clusterKey, cluster, options.Machinepool())
		}
		return service.CreateMachinePool(r, cmd, clusterKey, cluster, options.Machinepool())
	}
}
