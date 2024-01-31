/*
Copyright (c) 2023 Red Hat, Inc.

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

package kubeletconfig

import (
	"fmt"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/spf13/cobra"

	"github.com/openshift/rosa/pkg/interactive"
	"github.com/openshift/rosa/pkg/interactive/confirm"
	. "github.com/openshift/rosa/pkg/kubeletconfig"
	"github.com/openshift/rosa/pkg/ocm"
	"github.com/openshift/rosa/pkg/rosa"
)

type KubletConfigOptions struct {
	runtime *rosa.Runtime
}

func NewCreateKubeletConfigOptions() *KubletConfigOptions {
	r := rosa.NewRuntime().WithOCM()
	defer r.Cleanup()

	return &KubletConfigOptions{
		runtime: r,
	}
}

func NewCreateKubeletConfig() *cobra.Command {
	options := NewCreateKubeletConfigOptions()
	cmd := &cobra.Command{
		Use:     "kubeletconfig",
		Aliases: []string{"kubelet-config"},
		Short:   "Create a custom kubeletconfig for a cluster",
		Long:    "Create a custom kubeletconfig for a cluster",
		Example: `  # Create a custom kubeletconfig with a pod-pids-limit of 5000
  rosa create kubeletconfig --cluster=mycluster --pod-pids-limit=5000
  `,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := options.Create(); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().SortFlags = false
	cmd.Flags().IntVar(
		&args.podPidsLimit,
		PodPidsLimitOption,
		PodPidsLimitOptionDefaultValue,
		PodPidsLimitOptionUsage)

	ocm.AddClusterFlag(cmd)
	interactive.AddFlag(cmd.Flags())
	return cmd
}

var args struct {
	podPidsLimit int
}

func (o *KubletConfigOptions) Create() error {
	clusterKey := o.runtime.GetClusterKey()
	cluster := o.runtime.FetchCluster()

	if cluster.Hypershift().Enabled() {
		return fmt.Errorf("Hosted Control Plane clusters do not support custom KubeletConfig configuration.")
	}

	if cluster.State() != cmv1.ClusterStateReady {
		return fmt.Errorf("Cluster '%s' is not yet ready. Current state is '%s'", clusterKey, cluster.State())
	}

	kubeletConfig, err := o.runtime.OCMClient.GetClusterKubeletConfig(cluster.ID())
	if err != nil {
		return fmt.Errorf("Failed getting KubeletConfig for cluster '%s': %s",
			cluster.ID(), err)
	}

	if kubeletConfig != nil {
		return fmt.Errorf("A custom KubeletConfig for cluster '%s' already exists. "+
			"You should edit it via 'rosa edit kubeletconfig'", clusterKey)
	}

	requestedPids, err := ValidateOrPromptForRequestedPidsLimit(args.podPidsLimit, clusterKey, nil, o.runtime)
	if err != nil {
		return err
	}

	prompt := fmt.Sprintf("Creating the custom KubeletConfig for cluster '%s' will cause all non-Control Plane "+
		"nodes to reboot. This may cause outages to your applications. Do you wish to continue?", clusterKey)

	if confirm.ConfirmRaw(prompt) {

		o.runtime.Reporter.Debugf("Creating KubeletConfig for cluster '%s'", clusterKey)
		kubeletConfigArgs := ocm.KubeletConfigArgs{PodPidsLimit: requestedPids}

		_, err = o.runtime.OCMClient.CreateKubeletConfig(cluster.ID(), kubeletConfigArgs)
		if err != nil {
			return fmt.Errorf("Failed creating custom KubeletConfig for cluster '%s': '%s'",
				clusterKey, err)
		}

		o.runtime.Reporter.Infof("Successfully created custom KubeletConfig for cluster '%s'", clusterKey)
		return nil
	}

	o.runtime.Reporter.Infof("Creation of custom KubeletConfig for cluster '%s' aborted.", clusterKey)
	return nil
}
