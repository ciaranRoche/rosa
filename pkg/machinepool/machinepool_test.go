package machinepool

import (
	"fmt"
	aws2 "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	sdk "github.com/openshift-online/ocm-sdk-go"
	"github.com/openshift-online/ocm-sdk-go/logging"
	"github.com/openshift-online/ocm-sdk-go/testing"
	"github.com/openshift/rosa/pkg/aws"
	"github.com/openshift/rosa/pkg/ocm"
	mpOpts "github.com/openshift/rosa/pkg/options/machinepool"
	"github.com/openshift/rosa/pkg/test"
	"go.uber.org/mock/gomock"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/spf13/cobra"

	"github.com/openshift/rosa/pkg/interactive"
	ocmOutput "github.com/openshift/rosa/pkg/ocm/output"
	"github.com/openshift/rosa/pkg/output"
	"github.com/openshift/rosa/pkg/rosa"
)

var _ = FDescribe("Create Machine Pool", func() {
	var ctrl *gomock.Controller
	var cmd *cobra.Command
	var opts *mpOpts.CreateMachinepoolUserOptions
	var awsMock *aws.MockClient
	var t *test.TestingRuntime

	Context("Machine Pools", func() {
		BeforeEach(func() {
			ctrl = gomock.NewController(GinkgoT())
			cmd, opts = mpOpts.BuildMachinePoolCreateCommandWithOptions()

			t = test.NewTestRuntime()
			t.SsoServer = testing.MakeTCPServer()
			t.ApiServer = testing.MakeTCPServer()
			t.ApiServer.SetAllowUnhandledRequests(true)
			t.ApiServer.SetUnhandledRequestStatusCode(http.StatusInternalServerError)

			claims := testing.MakeClaims()
			claims["username"] = "foo"
			accessTokenObj := testing.MakeTokenObject(claims)
			accessToken := accessTokenObj.Raw

			logger, err := logging.NewGoLoggerBuilder().
				Debug(true).
				Build()
			Expect(err).To(BeNil())

			connection, err := sdk.NewConnectionBuilder().
				Logger(logger).
				Tokens(accessToken).
				URL(t.ApiServer.URL()).
				Build()
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
			awsMock = aws.NewMockClient(ctrl)
			t.RosaRuntime.AWSClient = awsMock
		})

		When("something is passed", func() {
			It("should do this", func() {
				t.ApiServer.AppendHandlers(testing.RespondWithJSON(http.StatusOK, ""))
				t.ApiServer.AppendHandlers(testing.RespondWithJSON(http.StatusOK, ""))
				t.ApiServer.AppendHandlers(testing.RespondWithJSON(http.StatusOK, ""))

				mockSubnets := []types.Subnet{
					{
						SubnetId:         aws2.String("subnet-0b761d44d3d9a4663"),
						AvailabilityZone: aws2.String("noop"),
					},
					{
						SubnetId:         aws2.String("subnet-0f87f640e56934cbc"),
						AvailabilityZone: aws2.String("noop"),
					},
				}
				mockAccessKey := &aws.AccessKey{
					AccessKeyID:     "noop",
					SecretAccessKey: "noop",
				}

				awsMock.EXPECT().GetVPCPrivateSubnets(gomock.Any()).Return(mockSubnets, nil).Times(1)
				awsMock.EXPECT().GetSubnetAvailabilityZone(gomock.Any()).Return("noop", nil).Times(1)
				awsMock.EXPECT().GetAWSAccessKeys().Return(mockAccessKey, nil).Times(1)

				mockClassicClusterReady := test.MockCluster(func(c *cmv1.ClusterBuilder) {
					c.AWS(cmv1.NewAWS().SubnetIDs("subnet-0b761d44d3d9a4663", "subnet-0f87f640e56934cbc"))
					c.Region(cmv1.NewCloudRegion().ID("us-east-1"))
					c.State(cmv1.ClusterStateReady)
					c.Hypershift(cmv1.NewHypershift().Enabled(false))
					c.Nodes(cmv1.NewClusterNodes().AvailabilityZones("noop"))
				})

				opts.Name = "noop"
				opts.Subnet = "subnet-0b761d44d3d9a4663"
				opts.Replicas = 2

				err := cmd.Flags().Set("replicas", "2")
				Expect(err).To(BeNil())

				service := NewMachinePoolService()
				err = service.CreateNodePools(t.RosaRuntime, cmd, "noop", mockClassicClusterReady, opts)
				Expect(err).To(BeNil())
			})
		})
	})
})

var policyBuilder cmv1.NodePoolUpgradePolicyBuilder
var date time.Time

var _ = Describe("Machinepool and nodepool", func() {
	Context("Nodepools", Ordered, func() {
		BeforeAll(func() {
			location, err := time.LoadLocation("America/New_York")
			Expect(err).ToNot(HaveOccurred())
			date = time.Date(2024, time.April, 2, 2, 2, 0, 0, location)
			policyBuilder = *cmv1.NewNodePoolUpgradePolicy().ID("test-policy").Version("1").
				ClusterID("test-cluster").State(cmv1.NewUpgradePolicyState().ID("test-state").
				Value(cmv1.UpgradePolicyStateValueScheduled)).
				NextRun(date)
		})
		It("Test printNodePools", func() {
			clusterBuilder := cmv1.NewCluster().ID("test").State(cmv1.ClusterStateReady).
				Hypershift(cmv1.NewHypershift().Enabled(true)).NodePools(cmv1.NewNodePoolList().
				Items(cmv1.NewNodePool().ID("np").Replicas(8).AvailabilityZone("az").
					Subnet("sn").Version(cmv1.NewVersion().ID("1")).AutoRepair(false)))
			cluster, err := clusterBuilder.Build()
			Expect(err).ToNot(HaveOccurred())
			out := getNodePoolsString(cluster.NodePools().Slice())
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(Equal(fmt.Sprintf("ID\tAUTOSCALING\tREPLICAS\t"+
				"INSTANCE TYPE\tLABELS\t\tTAINTS\t\tAVAILABILITY ZONE\tSUBNET\tVERSION\tAUTOREPAIR\t\n"+
				"%s\t%s\t%s\t%s\t%s\t\t%s\t\t%s\t%s\t%s\t%s\t\n",
				cluster.NodePools().Get(0).ID(),
				ocmOutput.PrintNodePoolAutoscaling(cluster.NodePools().Get(0).Autoscaling()),
				ocmOutput.PrintNodePoolReplicasShort(
					ocmOutput.PrintNodePoolCurrentReplicas(cluster.NodePools().Get(0).Status()),
					ocmOutput.PrintNodePoolReplicas(cluster.NodePools().Get(0).Autoscaling(),
						cluster.NodePools().Get(0).Replicas()),
				),
				ocmOutput.PrintNodePoolInstanceType(cluster.NodePools().Get(0).AWSNodePool()),
				ocmOutput.PrintLabels(cluster.NodePools().Get(0).Labels()),
				ocmOutput.PrintTaints(cluster.NodePools().Get(0).Taints()),
				cluster.NodePools().Get(0).AvailabilityZone(),
				cluster.NodePools().Get(0).Subnet(),
				ocmOutput.PrintNodePoolVersion(cluster.NodePools().Get(0).Version()),
				ocmOutput.PrintNodePoolAutorepair(cluster.NodePools().Get(0).AutoRepair()))))
		})
		It("Test appendUpgradesIfExist", func() {
			policy, err := policyBuilder.Build()
			Expect(err).ToNot(HaveOccurred())
			out := appendUpgradesIfExist(policy, "test\n")
			Expect(out).To(Equal(fmt.Sprintf("test\nScheduled upgrade:                     %s %s on %s\n",
				cmv1.UpgradePolicyStateValueScheduled, "1", date.Format("2006-01-02 15:04 MST"))))
		})
		It("Test appendUpgradesIfExist nil schedule", func() {
			out := appendUpgradesIfExist(nil, "test\n")
			Expect(out).To(Equal("test\n"))
		})
		It("Test func formatNodePoolOutput", func() {
			policy, err := policyBuilder.Build()
			Expect(err).ToNot(HaveOccurred())
			nodePool, err := cmv1.NewNodePool().ID("test-np").Version(cmv1.NewVersion().ID("1")).
				Subnet("test-subnet").Replicas(4).AutoRepair(true).Build()
			Expect(err).ToNot(HaveOccurred())

			out, err := formatNodePoolOutput(nodePool, policy)
			Expect(err).ToNot(HaveOccurred())
			expectedOutput := make(map[string]interface{})
			upgrade := make(map[string]interface{})
			upgrade["version"] = policy.Version()
			upgrade["state"] = policy.State().Value()
			upgrade["nextRun"] = policy.NextRun().Format("2006-01-02 15:04 MST")
			expectedOutput["subnet"] = "test-subnet"

			expectedOutput["kind"] = "NodePool"
			expectedOutput["id"] = "test-np"
			expectedOutput["replicas"] = 4.0
			version := make(map[string]interface{})
			version["kind"] = "Version"
			version["id"] = "1"
			expectedOutput["auto_repair"] = true
			expectedOutput["version"] = version
			expectedOutput["scheduledUpgrade"] = upgrade
			fmt.Println(out)
			Expect(fmt.Sprint(out)).To(Equal(fmt.Sprint(expectedOutput)))
		})
		It("should return an error if both `subnet` and `availability-zone` flags are set", func() {
			cmd := &cobra.Command{}
			cmd.Flags().Bool("availability-zone", true, "")
			cmd.Flags().Bool("subnet", true, "")

			clusterKey := "test-cluster-key"
			clusterBuilder := cmv1.NewCluster().ID("test").State(cmv1.ClusterStateReady)
			cluster, err := clusterBuilder.Build()
			Expect(err).ToNot(HaveOccurred())
			r := &rosa.Runtime{}
			args := mpOpts.CreateMachinepoolUserOptions{}

			cmd.Flags().Set("availability-zone", "true")
			cmd.Flags().Set("subnet", "true")

			machinePool := &machinePool{}
			err = machinePool.CreateNodePools(r, cmd, clusterKey, cluster, &args)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("Setting both `subnet` and " +
				"`availability-zone` flag is not supported. Please select `subnet` " +
				"or `availability-zone` to create a single availability zone machine pool"))
		})
		It("should fail name validation", func() {
			cmd := &cobra.Command{}
			r := &rosa.Runtime{}
			args := mpOpts.CreateMachinepoolUserOptions{}
			machinePool := &machinePool{}

			clusterKey := "test-cluster-key"
			clusterBuilder := cmv1.NewCluster().ID("test").State(cmv1.ClusterStateReady)
			cluster, err := clusterBuilder.Build()
			Expect(err).ToNot(HaveOccurred())

			cmd.Flags().StringVar(&args.Name, "name", "", "Name of the machine pool")
			invalidName := "0909+===..3"
			cmd.Flags().Set("name", invalidName)

			err = machinePool.CreateNodePools(r, cmd, clusterKey, cluster, &args)
			Expect(err).To(HaveOccurred())

			Expect(err.Error()).To(Equal("Expected a valid name for the machine pool"))
		})
	})
	Context("MachinePools", func() {
		It("Test printMachinePools", func() {
			clusterBuilder := cmv1.NewCluster().ID("test").State(cmv1.ClusterStateReady).
				MachinePools(cmv1.NewMachinePoolList().
					Items(cmv1.NewMachinePool().ID("np").Replicas(8).Subnets("sn1", "sn2").
						InstanceType("test instance type").Taints(cmv1.NewTaint().Value("test").
						Key("taint"))))
			cluster, err := clusterBuilder.Build()
			Expect(err).ToNot(HaveOccurred())
			out := getMachinePoolsString(cluster.MachinePools().Slice())
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(Equal(fmt.Sprintf("ID\tAUTOSCALING\tREPLICAS\tINSTANCE TYPE\tLABELS\t\tTAINTS\t"+
				"\tAVAILABILITY ZONES\t\tSUBNETS\t\tSPOT INSTANCES\tDISK SIZE\tSG IDs\n"+
				"%s\t%s\t%s\t%s\t%s\t\t%s\t\t%s\t\t%s\t\t%s\t%s\t%s\n",
				cluster.MachinePools().Get(0).ID(),
				ocmOutput.PrintMachinePoolAutoscaling(cluster.MachinePools().Get(0).Autoscaling()),
				ocmOutput.PrintMachinePoolReplicas(cluster.MachinePools().Get(0).Autoscaling(),
					cluster.MachinePools().Get(0).Replicas()),
				cluster.MachinePools().Get(0).InstanceType(),
				ocmOutput.PrintLabels(cluster.MachinePools().Get(0).Labels()),
				ocmOutput.PrintTaints(cluster.MachinePools().Get(0).Taints()),
				output.PrintStringSlice(cluster.MachinePools().Get(0).AvailabilityZones()),
				output.PrintStringSlice(cluster.MachinePools().Get(0).Subnets()),
				ocmOutput.PrintMachinePoolSpot(cluster.MachinePools().Get(0)),
				ocmOutput.PrintMachinePoolDiskSize(cluster.MachinePools().Get(0)),
				output.PrintStringSlice(cluster.MachinePools().Get(0).AWS().AdditionalSecurityGroupIds()))))
		})
		It("Validate invalid regex", func() {
			Expect(MachinePoolKeyRE.MatchString("$%%$%$%^$%^$%^$%^")).To(BeFalse())
			Expect(MachinePoolKeyRE.MatchString("machinepool1")).To(BeTrue())
			Expect(MachinePoolKeyRE.MatchString("1machinepool")).To(BeFalse())
			Expect(MachinePoolKeyRE.MatchString("#1machinepool")).To(BeFalse())
			Expect(MachinePoolKeyRE.MatchString("m123123123123123123123123123")).To(BeTrue())
			Expect(MachinePoolKeyRE.MatchString("m#123")).To(BeFalse())
		})
		It("Tests getMachinePoolAvailabilityZones", func() {
			r := &rosa.Runtime{}
			var expectedAZs []string
			clusterBuilder := cmv1.NewCluster().ID("test").State(cmv1.ClusterStateReady).
				MultiAZ(true).Nodes(cmv1.NewClusterNodes().
				AvailabilityZones("us-east-1a", "us-east-1b"))
			cluster, err := clusterBuilder.Build()
			Expect(err).ToNot(HaveOccurred())
			isMultiAZ := cluster.MultiAZ()
			Expect(isMultiAZ).To(Equal(true))

			multiAZMachinePool := false
			availabilityZoneUserInput := "us-east-1a"
			subnetUserInput := ""

			azs, err := getMachinePoolAvailabilityZones(r, cluster,
				multiAZMachinePool, availabilityZoneUserInput, subnetUserInput)
			Expect(err).ToNot(HaveOccurred())

			expectedAZs = append(expectedAZs, "us-east-1a")
			Expect(azs).To(Equal(expectedAZs))

			multiAZMachinePool = true
			expectedAZs = append(expectedAZs, "us-east-1b")
			azs, err = getMachinePoolAvailabilityZones(r, cluster,
				multiAZMachinePool, availabilityZoneUserInput, subnetUserInput)
			Expect(err).ToNot(HaveOccurred())

			Expect(azs).To(Equal(expectedAZs))
		})
	})
})

var _ = Describe("MachinePools", func() {
	Context("AddMachinePool validation errors", func() {
		var (
			cmd        *cobra.Command
			clusterKey string
			r          *rosa.Runtime
			args       mpOpts.CreateMachinepoolUserOptions
			cluster    *cmv1.Cluster
			err        error
		)

		JustBeforeEach(func() {
			cmd = &cobra.Command{}
			r = &rosa.Runtime{}
			args = mpOpts.CreateMachinepoolUserOptions{}
			clusterKey = "test-cluster-key"
			clusterBuilder := cmv1.NewCluster().ID("test").State(cmv1.ClusterStateReady)
			cluster, err = clusterBuilder.Build()
			Expect(err).ToNot(HaveOccurred())
		})

		It("should error when 'multi-availability-zone' flag is set for non-multi-AZ clusters", func() {
			machinePool := &machinePool{}
			cmd.Flags().Bool("multi-availability-zone", true, "")
			cmd.Flags().Set("multi-availability-zone", "true")
			err = machinePool.CreateMachinePool(r, cmd, clusterKey, cluster, &args)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("Setting the `multi-availability-zone` flag is only allowed for multi-AZ clusters"))
		})

		It("should error when 'availability-zone' flag is set for non-multi-AZ clusters", func() {
			machinePool := &machinePool{}
			// cmd.Flags().StringVar(&args.AvailabilityZone, "availability-zone", "", "")
			cmd.Flags().Set("availability-zone", "az")
			err = machinePool.CreateMachinePool(r, cmd, clusterKey, cluster, &args)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("Setting the `availability-zone` flag is only allowed for multi-AZ clusters"))
		})

		It("should error when 'subnet' flag is set for non-BYOVPC clusters", func() {
			machinePool := &machinePool{}
			cmd.Flags().StringVar(&args.Subnet, "subnet", "", "")
			cmd.Flags().Set("subnet", "test-subnet")
			err = machinePool.CreateMachinePool(r, cmd, clusterKey, cluster, &args)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("Setting the `subnet` flag is only allowed for BYO VPC clusters"))
		})
	})
})

var _ = Describe("Utility Functions", func() {
	Describe("Split function", func() {
		It("should return true for '=' rune", func() {
			Expect(Split('=')).To(BeTrue())
		})

		It("should return true for ':' rune", func() {
			Expect(Split(':')).To(BeTrue())
		})

		It("should return false for any other rune", func() {
			Expect(Split('a')).To(BeFalse())
		})
	})

	Describe("minReplicaValidator function", func() {
		var validator interactive.Validator

		BeforeEach(func() {
			validator = minReplicaValidator(true) // or false for non-multiAZ
		})

		It("should return error for non-integer input", func() {
			err := validator("non-integer")
			Expect(err).To(HaveOccurred())
		})

		It("should return error for negative input", func() {
			err := validator(-1)
			Expect(err).To(HaveOccurred())
		})

		It("should return error if not multiple of 3 for multiAZ", func() {
			err := validator(2)
			Expect(err).To(HaveOccurred())
		})

		It("should not return error for valid input", func() {
			err := validator(3)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("maxReplicaValidator function", func() {
		var validator interactive.Validator

		BeforeEach(func() {
			validator = maxReplicaValidator(1, true)
		})

		It("should return error for non-integer input", func() {
			err := validator("non-integer")
			Expect(err).To(HaveOccurred())
		})

		It("should return error if maxReplicas less than minReplicas", func() {
			err := validator(0)
			Expect(err).To(HaveOccurred())
		})

		It("should return error if not multiple of 3 for multiAZ", func() {
			err := validator(5)
			Expect(err).To(HaveOccurred())
		})

		It("should not return error for valid input", func() {
			err := validator(3)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("spotMaxPriceValidator function", func() {
		It("should return nil for 'on-demand'", func() {
			err := spotMaxPriceValidator("on-demand")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should return error for non-numeric input", func() {
			err := spotMaxPriceValidator("not-a-number")
			Expect(err).To(HaveOccurred())
		})

		It("should return error for negative price", func() {
			err := spotMaxPriceValidator("-1")
			Expect(err).To(HaveOccurred())
		})

		It("should not return error for positive price", func() {
			err := spotMaxPriceValidator("0.01")
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
