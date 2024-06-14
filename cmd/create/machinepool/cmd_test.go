package machinepool

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"reflect"

	"go.uber.org/mock/gomock"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	sdk "github.com/openshift-online/ocm-sdk-go"
	amsv1 "github.com/openshift-online/ocm-sdk-go/accountsmgmt/v1"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	v1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/openshift-online/ocm-sdk-go/logging"
	. "github.com/openshift-online/ocm-sdk-go/testing"

	"github.com/openshift/rosa/pkg/aws"
	"github.com/openshift/rosa/pkg/ocm"
	. "github.com/openshift/rosa/pkg/output"
	"github.com/openshift/rosa/pkg/rosa"
	"github.com/openshift/rosa/pkg/test"
)

const (
	nodePoolName = "nodepool85"
	clusterId    = "24vf9iitg3p6tlml88iml6j6mu095mh8"
	instanceType = "m5.xlarge"
)

var _ = Describe("Create machine pool", func() {
	Context("Create machine pool command", func() {
		format.TruncatedDiff = false

		mockClassicClusterReady := test.MockCluster(func(c *cmv1.ClusterBuilder) {
			c.AWS(cmv1.NewAWS().SubnetIDs("subnet-0b761d44d3d9a4663", "subnet-0f87f640e56934cbc"))
			c.Region(cmv1.NewCloudRegion().ID("us-east-1"))
			c.State(cmv1.ClusterStateReady)
			c.Hypershift(cmv1.NewHypershift().Enabled(false))
		})
		classicClusterReady := test.FormatClusterList([]*cmv1.Cluster{mockClassicClusterReady})

		var t *test.TestingRuntime

		BeforeEach(func() {
			t = test.NewTestRuntime()
			// Create the servers:
			t.SsoServer = MakeTCPServer()
			t.ApiServer = MakeTCPServer()
			t.ApiServer.SetAllowUnhandledRequests(true)
			t.ApiServer.SetUnhandledRequestStatusCode(http.StatusInternalServerError)

			// Create the token:
			claims := MakeClaims()
			claims["username"] = "foo"
			accessTokenObj := MakeTokenObject(claims)
			accessToken := accessTokenObj.Raw

			// Prepare the server:
			t.SsoServer.AppendHandlers(
				RespondWithAccessToken(accessToken),
			)
			// Prepare the logger:
			logger, err := logging.NewGoLoggerBuilder().
				Debug(true).
				Build()
			Expect(err).To(BeNil())
			// Set up the connection with the fake config
			connection, err := sdk.NewConnectionBuilder().
				Logger(logger).
				Tokens(accessToken).
				URL(t.ApiServer.URL()).
				Build()
			// Initialize client object
			Expect(err).To(BeNil())
			ocmClient := ocm.NewClientWithConnection(connection)
			ocm.SetClusterKey("cluster1")
			t.RosaRuntime = rosa.NewRuntime()
			t.RosaRuntime.OCMClient = ocmClient
			t.RosaRuntime.Creator = &aws.Creator{
				ARN:       "fake",
				AccountID: "123",
				IsSTS:     false,
			}

			ctrl := gomock.NewController(GinkgoT())
			aws := aws.NewMockClient(ctrl)
			aws.EXPECT().GetLocalAWSAccessKeys().AnyTimes()
			t.RosaRuntime.AWSClient = aws
			SetOutput("")
		})
		Context("Classic", func() {
			It("Creates machine pool successfully with flags", func() {
				t.ApiServer.AppendHandlers(RespondWithJSON(http.StatusOK, classicClusterReady))
				t.ApiServer.AppendHandlers(RespondWithJSON(http.StatusOK, ""))
				args := NewCreateMachinepoolUserOptions()
				args.Name = nodePoolName
				args.Replicas = 3
				args.InstanceType = instanceType
				runner := CreateMachinepoolRunner(args)
				err := t.StdOutReader.Record()
				Expect(err).ToNot(HaveOccurred())
				cmd := NewCreateMachinePoolCommand()
				err = cmd.Flag("cluster").Value.Set(clusterId)
				Expect(err).ToNot(HaveOccurred())
				err = runner(context.Background(), t.RosaRuntime, cmd,
					[]string{"--name", nodePoolName, "--replicas", "3", "--instance-type", instanceType})
				Expect(err).To(BeNil())
				stdout, err := t.StdOutReader.Read()
				Expect(err).ToNot(HaveOccurred())
				Expect(stdout).To(Equal(fmt.Sprintf("Machine pool '%s' created successfully on cluster '%s'", nodePoolName, clusterId)))
			})
		})
	})
})

// formatNodePool simulates the output of APIs for a fake node pool list
func formatNodePool() string {
	version := cmv1.NewVersion().ID("4.12.24").RawID("openshift-4.12.24")
	awsNodePool := cmv1.NewAWSNodePool().InstanceType("m5.xlarge")
	nodeDrain := cmv1.NewValue().Value(1).Unit("minute")
	nodePool, err := cmv1.NewNodePool().ID(nodePoolName).Version(version).
		AWSNodePool(awsNodePool).AvailabilityZone("us-east-1a").NodeDrainGracePeriod(nodeDrain).Build()
	Expect(err).ToNot(HaveOccurred())
	return fmt.Sprintf("{\n  \"items\": [\n    %s\n  ],\n  \"page\": 0,\n  \"size\": 1,\n  \"total\": 1\n}",
		test.FormatResource(nodePool))
}

// formatMachinePool simulates the output of APIs for a fake machine pool list
func formatMachinePool() string {
	awsMachinePoolPool := cmv1.NewAWSMachinePool().SpotMarketOptions(cmv1.NewAWSSpotMarketOptions().MaxPrice(5))
	machinePool, err := cmv1.NewMachinePool().ID(nodePoolName).AWS(awsMachinePoolPool).InstanceType("m5.xlarge").
		AvailabilityZones("us-east-1a", "us-east-1b", "us-east-1c").Build()
	Expect(err).ToNot(HaveOccurred())
	return fmt.Sprintf("{\n  \"items\": [\n    %s\n  ],\n  \"page\": 0,\n  \"size\": 1,\n  \"total\": 1\n}",
		test.FormatResource(machinePool))
}

func formatResource(resource interface{}) string {
	var outputJson bytes.Buffer
	var err error
	switch reflect.TypeOf(resource).String() {
	case "*v1.KubeletConfig":
		if res, ok := resource.(*v1.KubeletConfig); ok {
			err = v1.MarshalKubeletConfig(res, &outputJson)
		}
	case "*v1.Version":
		if res, ok := resource.(*v1.Version); ok {
			err = v1.MarshalVersion(res, &outputJson)
		}
	case "*v1.NodePool":
		if res, ok := resource.(*v1.NodePool); ok {
			err = v1.MarshalNodePool(res, &outputJson)
		}
	case "*v1.MachinePool":
		if res, ok := resource.(*v1.MachinePool); ok {
			err = v1.MarshalMachinePool(res, &outputJson)
		}
	case "*v1.ClusterAutoscaler":
		if res, ok := resource.(*v1.ClusterAutoscaler); ok {
			err = v1.MarshalClusterAutoscaler(res, &outputJson)
		}
	case "*v1.ControlPlaneUpgradePolicy":
		if res, ok := resource.(*v1.ControlPlaneUpgradePolicy); ok {
			err = v1.MarshalControlPlaneUpgradePolicy(res, &outputJson)
		}
	case "*v1.ExternalAuth":
		if res, ok := resource.(*v1.ExternalAuth); ok {
			err = v1.MarshalExternalAuth(res, &outputJson)
		}
	case "*v1.BreakGlassCredential":
		if res, ok := resource.(*v1.BreakGlassCredential); ok {
			err = v1.MarshalBreakGlassCredential(res, &outputJson)
		}
	case "*v1.Account":
		if res, ok := resource.(*amsv1.Account); ok {
			err = amsv1.MarshalAccount(res, &outputJson)
		}
	default:
		{
			return "NOTIMPLEMENTED"
		}
	}
	if err != nil {
		return err.Error()
	}

	return outputJson.String()
}
