package machinepool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/briandowns/spinner"
	diskValidator "github.com/openshift-online/ocm-common/pkg/machinepool/validations"
	commonUtils "github.com/openshift-online/ocm-common/pkg/utils"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/spf13/cobra"

	"github.com/openshift/rosa/pkg/helper"
	"github.com/openshift/rosa/pkg/helper/features"
	"github.com/openshift/rosa/pkg/helper/machinepools"
	mpHelpers "github.com/openshift/rosa/pkg/helper/machinepools"
	"github.com/openshift/rosa/pkg/helper/versions"
	"github.com/openshift/rosa/pkg/interactive"
	"github.com/openshift/rosa/pkg/interactive/confirm"
	"github.com/openshift/rosa/pkg/interactive/securitygroups"
	"github.com/openshift/rosa/pkg/ocm"
	ocmOutput "github.com/openshift/rosa/pkg/ocm/output"
	"github.com/openshift/rosa/pkg/output"
	"github.com/openshift/rosa/pkg/rosa"
)

var fetchMessage string = "Fetching %s '%s' for cluster '%s'"
var notFoundMessage string = "Machine pool '%s' not found"

// Regular expression to used to make sure that the identifier given by the
// user is safe and that it there is no risk of SQL injection:
var machinePoolKeyRE = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

type CreateMachinepoolUserOptions struct {
	Name                  string
	InstanceType          string
	Replicas              int
	AutoscalingEnabled    bool
	MinReplicas           int
	MaxReplicas           int
	Labels                string
	Taints                string
	UseSpotInstances      bool
	SpotMaxPrice          string
	MultiAvailabilityZone bool
	AvailabilityZone      string
	Subnet                string
	Version               string
	Autorepair            bool
	TuningConfigs         string
	KubeletConfigs        string
	RootDiskSize          string
	SecurityGroupIds      []string
	NodeDrainGracePeriod  string
	Tags                  []string
}

//go:generate mockgen -source=machinepool.go -package=machinepool -destination=machinepool_mock.go
type MachinePoolService interface {
	DescribeMachinePool(r *rosa.Runtime, cluster *cmv1.Cluster, clusterKey string, machinePoolId string) error
	ListMachinePools(r *rosa.Runtime, clusterKey string, cluster *cmv1.Cluster) error
	DeleteMachinePool(r *rosa.Runtime, machinePoolId string, clusterKey string, cluster *cmv1.Cluster) error
	CreateMachinePool(r *rosa.Runtime, cmd *cobra.Command, clusterKey string, cluster *cmv1.Cluster, options *CreateMachinepoolUserOptions) error
	CreateNodePools(r *rosa.Runtime, cmd *cobra.Command, clusterKey string, cluster *cmv1.Cluster, options *CreateMachinepoolUserOptions) error
}

type machinePool struct {
}

var _ MachinePoolService = &machinePool{}

func NewMachinePoolService() MachinePoolService {
	return &machinePool{}
}

func (m *machinePool) CreateMachinePool(r *rosa.Runtime, cmd *cobra.Command, clusterKey string, cluster *cmv1.Cluster,
	args *CreateMachinepoolUserOptions) error {

	// Validate flags that are only allowed for multi-AZ clusters
	isMultiAvailabilityZoneSet := cmd.Flags().Changed("multi-availability-zone")
	if isMultiAvailabilityZoneSet && !cluster.MultiAZ() {
		return fmt.Errorf("Setting the `multi-availability-zone` flag is only allowed for multi-AZ clusters")
	}
	isAvailabilityZoneSet := cmd.Flags().Changed("availability-zone")
	if isAvailabilityZoneSet && !cluster.MultiAZ() {
		return fmt.Errorf("Setting the `availability-zone` flag is only allowed for multi-AZ clusters")
	}

	// Validate flags that are only allowed for BYOVPC cluster
	isSubnetSet := cmd.Flags().Changed("subnet")
	isByoVpc := helper.IsBYOVPC(cluster)
	if !isByoVpc && isSubnetSet {
		return fmt.Errorf("Setting the `subnet` flag is only allowed for BYO VPC clusters")
	}

	isSecurityGroupIdsSet := cmd.Flags().Changed(securitygroups.MachinePoolSecurityGroupFlag)
	isVersionCompatibleComputeSgIds, err := versions.IsGreaterThanOrEqual(
		cluster.Version().RawID(), ocm.MinVersionForAdditionalComputeSecurityGroupIdsDay2)
	if err != nil {
		return fmt.Errorf("There was a problem checking version compatibility: %v", err)
	}
	if isSecurityGroupIdsSet {
		if !isByoVpc {
			return fmt.Errorf("Setting the `%s` flag is only allowed for BYOVPC clusters",
				securitygroups.MachinePoolSecurityGroupFlag)
		}
		if !isVersionCompatibleComputeSgIds {
			formattedVersion, err := versions.FormatMajorMinorPatch(
				ocm.MinVersionForAdditionalComputeSecurityGroupIdsDay2,
			)
			if err != nil {
				return fmt.Errorf(versions.MajorMinorPatchFormattedErrorOutput, err)
			}
			return fmt.Errorf("Parameter '%s' is not supported prior to version '%s'",
				securitygroups.MachinePoolSecurityGroupFlag, formattedVersion)
		}
	}

	if isSubnetSet && isAvailabilityZoneSet {
		return fmt.Errorf("Setting both `subnet` and `availability-zone` flag is not supported." +
			" Please select `subnet` or `availability-zone` to create a single availability zone machine pool")
	}

	// Validate `subnet` or `availability-zone` flags are set for a single AZ machine pool
	if isAvailabilityZoneSet && isMultiAvailabilityZoneSet && args.MultiAvailabilityZone {
		return fmt.Errorf("Setting the `availability-zone` flag is only supported for creating a single AZ " +
			"machine pool in a multi-AZ cluster")
	}
	if isSubnetSet && isMultiAvailabilityZoneSet && args.MultiAvailabilityZone {
		return fmt.Errorf("Setting the `subnet` flag is only supported for creating a single AZ machine pool")
	}

	mpHelpers.HostedClusterOnlyFlag(r, cmd, "version")
	mpHelpers.HostedClusterOnlyFlag(r, cmd, "autorepair")
	mpHelpers.HostedClusterOnlyFlag(r, cmd, "tuning-configs")
	mpHelpers.HostedClusterOnlyFlag(r, cmd, "kubelet-configs")

	// Machine pool name:
	name := strings.Trim(args.Name, " \t")
	if name == "" && !interactive.Enabled() {
		interactive.Enable()
		r.Reporter.Infof("Enabling interactive mode")
	}
	if name == "" || interactive.Enabled() {
		name, err = interactive.GetString(interactive.Input{
			Question: "Machine pool name",
			Default:  name,
			Required: true,
			Validators: []interactive.Validator{
				interactive.RegExp(machinePoolKeyRE.String()),
			},
		})
		if err != nil {
			return fmt.Errorf("Expected a valid name for the machine pool: %s", err)
		}
	}
	name = strings.Trim(name, " \t")
	if !machinePoolKeyRE.MatchString(name) {
		return fmt.Errorf("Expected a valid name for the machine pool")
	}

	// Allow the user to select subnet for a single AZ BYOVPC cluster
	var subnet string
	if !cluster.MultiAZ() && isByoVpc {
		subnet, err = getSubnetFromUser(cmd, r, isSubnetSet, cluster, args)
		if err != nil {
			return err
		}
	}

	// Single AZ machine pool for a multi-AZ cluster
	var multiAZMachinePool bool
	var availabilityZone string
	if cluster.MultiAZ() {
		// Choosing a single AZ machine pool implicitly (providing availability zone or subnet)
		if isAvailabilityZoneSet || isSubnetSet {
			isMultiAvailabilityZoneSet = true
			args.MultiAvailabilityZone = false
		}

		if !isMultiAvailabilityZoneSet && interactive.Enabled() && !confirm.Yes() {
			multiAZMachinePool, err = interactive.GetBool(interactive.Input{
				Question: "Create multi-AZ machine pool",
				Help:     cmd.Flags().Lookup("multi-availability-zone").Usage,
				Default:  true,
				Required: false,
			})
			if err != nil {
				return fmt.Errorf("Expected a valid value for create multi-AZ machine pool")
			}
		} else {
			multiAZMachinePool = args.MultiAvailabilityZone
		}

		if !multiAZMachinePool {
			// Allow to create a single AZ machine pool providing the subnet
			if isByoVpc && args.AvailabilityZone == "" {
				subnet, err = getSubnetFromUser(cmd, r, isSubnetSet, cluster, args)
				if err != nil {
					return err
				}
			}

			// Select availability zone if the user didn't select subnet
			if subnet == "" {
				availabilityZone = cluster.Nodes().AvailabilityZones()[0]
				if !isAvailabilityZoneSet && interactive.Enabled() {
					availabilityZone, err = interactive.GetOption(interactive.Input{
						Question: "AWS availability zone",
						Help:     cmd.Flags().Lookup("availability-zone").Usage,
						Options:  cluster.Nodes().AvailabilityZones(),
						Default:  availabilityZone,
						Required: true,
					})
					if err != nil {
						return fmt.Errorf("Expected a valid AWS availability zone: %s", err)
					}
				} else if isAvailabilityZoneSet {
					availabilityZone = args.AvailabilityZone
				}

				if !helper.Contains(cluster.Nodes().AvailabilityZones(), availabilityZone) {
					return fmt.Errorf("Availability zone '%s' doesn't belong to the cluster's availability zones",
						availabilityZone)
				}
			}
		}
	}

	isMinReplicasSet := cmd.Flags().Changed("min-replicas")
	isMaxReplicasSet := cmd.Flags().Changed("max-replicas")
	isAutoscalingSet := cmd.Flags().Changed("enable-autoscaling")
	isReplicasSet := cmd.Flags().Changed("replicas")

	minReplicas := args.MinReplicas
	maxReplicas := args.MaxReplicas
	autoscaling := args.AutoscalingEnabled
	replicas := args.Replicas

	// Autoscaling
	if !isReplicasSet && !autoscaling && !isAutoscalingSet && interactive.Enabled() {
		autoscaling, err = interactive.GetBool(interactive.Input{
			Question: "Enable autoscaling",
			Help:     cmd.Flags().Lookup("enable-autoscaling").Usage,
			Default:  autoscaling,
			Required: false,
		})
		if err != nil {
			return fmt.Errorf("Expected a valid value for enable-autoscaling: %s", err)
		}
	}

	if autoscaling {
		// if the user set replicas and enabled autoscaling
		if isReplicasSet {
			return fmt.Errorf("Replicas can't be set when autoscaling is enabled")
		}
		if interactive.Enabled() || !isMinReplicasSet {
			minReplicas, err = interactive.GetInt(interactive.Input{
				Question: "Min replicas",
				Help:     cmd.Flags().Lookup("min-replicas").Usage,
				Default:  minReplicas,
				Required: true,
				Validators: []interactive.Validator{
					minReplicaValidator(multiAZMachinePool),
				},
			})
			if err != nil {
				return fmt.Errorf("Expected a valid number of min replicas: %s", err)
			}
		}
		err = minReplicaValidator(multiAZMachinePool)(minReplicas)
		if err != nil {
			return err
		}

		if interactive.Enabled() || !isMaxReplicasSet {
			maxReplicas, err = interactive.GetInt(interactive.Input{
				Question: "Max replicas",
				Help:     cmd.Flags().Lookup("max-replicas").Usage,
				Default:  maxReplicas,
				Required: true,
				Validators: []interactive.Validator{
					maxReplicaValidator(minReplicas, multiAZMachinePool),
				},
			})
			if err != nil {
				return fmt.Errorf("Expected a valid number of max replicas: %s", err)
			}
		}
		err = maxReplicaValidator(minReplicas, multiAZMachinePool)(maxReplicas)
		if err != nil {
			return err
		}
	} else {
		// if the user set min/max replicas and hasn't enabled autoscaling
		if isMinReplicasSet || isMaxReplicasSet {
			return fmt.Errorf("Autoscaling must be enabled in order to set min and max replicas")
		}
		if interactive.Enabled() || !isReplicasSet {
			replicas, err = interactive.GetInt(interactive.Input{
				Question: "Replicas",
				Help:     cmd.Flags().Lookup("replicas").Usage,
				Default:  replicas,
				Required: true,
				Validators: []interactive.Validator{
					minReplicaValidator(multiAZMachinePool),
				},
			})
			if err != nil {
				return fmt.Errorf("Expected a valid number of replicas: %s", err)
			}
		}
		err = minReplicaValidator(multiAZMachinePool)(replicas)
		if err != nil {
			return err
		}
	}

	securityGroupIds := args.SecurityGroupIds
	if interactive.Enabled() && isVersionCompatibleComputeSgIds &&
		isByoVpc && !isSecurityGroupIdsSet {
		securityGroupIds, err = getSecurityGroupsOption(r, cmd, cluster)
		if err != nil {
			return err
		}
	}
	for i, sg := range securityGroupIds {
		securityGroupIds[i] = strings.TrimSpace(sg)
	}

	// Machine pool instance type:
	instanceType := args.InstanceType
	if instanceType == "" && !interactive.Enabled() {
		return fmt.Errorf("You must supply a valid instance type")
	}

	var spin *spinner.Spinner
	if r.Reporter.IsTerminal() && !output.HasFlag() {
		spin = spinner.New(spinner.CharSets[9], 100*time.Millisecond)
	}
	if spin != nil {
		r.Reporter.Infof("Checking available instance types for machine pool '%s'", name)
		spin.Start()
	}

	// Determine machine pool availability zones to filter supported machine types
	availabilityZonesFilter, err := getMachinePoolAvailabilityZones(r, cluster, multiAZMachinePool, availabilityZone,
		subnet)
	if err != nil {
		return err
	}

	instanceTypeList, err := r.OCMClient.GetAvailableMachineTypesInRegion(
		cluster.Region().ID(),
		availabilityZonesFilter,
		cluster.AWS().STS().RoleARN(),
		r.AWSClient,
	)
	if err != nil {
		return fmt.Errorf(fmt.Sprintf("%s", err))
	}

	if spin != nil {
		spin.Stop()
	}

	if interactive.Enabled() {
		if instanceType == "" {
			instanceType = instanceTypeList.Items[0].MachineType.ID()
		}
		instanceType, err = interactive.GetOption(interactive.Input{
			Question: "Instance type",
			Help:     cmd.Flags().Lookup("instance-type").Usage,
			Options:  instanceTypeList.GetAvailableIDs(cluster.MultiAZ()),
			Default:  instanceType,
			Required: true,
		})
		if err != nil {
			return fmt.Errorf("Expected a valid instance type: %s", err)
		}
	}

	err = instanceTypeList.ValidateMachineType(instanceType, cluster.MultiAZ())
	if err != nil {
		return fmt.Errorf("Expected a valid instance type: %s", err)
	}

	existingLabels := make(map[string]string, 0)
	labelMap := mpHelpers.GetLabelMap(cmd, r, existingLabels, args.Labels)

	existingTaints := make([]*cmv1.Taint, 0)
	taintBuilders := mpHelpers.GetTaints(cmd, r, existingTaints, args.Taints)

	// Spot instances
	isSpotSet := cmd.Flags().Changed("use-spot-instances")
	isSpotMaxPriceSet := cmd.Flags().Changed("spot-max-price")

	useSpotInstances := args.UseSpotInstances
	spotMaxPrice := args.SpotMaxPrice
	if isSpotMaxPriceSet && isSpotSet && !useSpotInstances {
		return fmt.Errorf("Can't set max price when not using spot instances")
	}

	// Validate spot instance are supported
	var isLocalZone bool
	if subnet != "" {
		isLocalZone, err = r.AWSClient.IsLocalAvailabilityZone(availabilityZonesFilter[0])
		if err != nil {
			return err
		}
	}
	if isLocalZone && useSpotInstances {
		return fmt.Errorf("Spot instances are not supported for local zones")
	}

	if !isSpotSet && !isSpotMaxPriceSet && !isLocalZone && interactive.Enabled() {
		useSpotInstances, err = interactive.GetBool(interactive.Input{
			Question: "Use spot instances",
			Help:     cmd.Flags().Lookup("use-spot-instances").Usage,
			Default:  useSpotInstances,
			Required: false,
		})
		if err != nil {
			return fmt.Errorf("Expected a valid value for use spot instances: %s", err)
		}
	}

	if useSpotInstances && !isSpotMaxPriceSet && interactive.Enabled() {
		spotMaxPrice, err = interactive.GetString(interactive.Input{
			Question: "Spot instance max price",
			Help:     cmd.Flags().Lookup("spot-max-price").Usage,
			Required: false,
			Default:  spotMaxPrice,
			Validators: []interactive.Validator{
				spotMaxPriceValidator,
			},
		})
		if err != nil {
			return fmt.Errorf("Expected a valid value for spot max price: %s", err)
		}
	}

	var maxPrice *float64

	err = spotMaxPriceValidator(spotMaxPrice)
	if err != nil {
		return err
	}
	if spotMaxPrice != "on-demand" {
		price, _ := strconv.ParseFloat(spotMaxPrice, commonUtils.MaxByteSize)
		maxPrice = &price
	}

	awsTags := machinepools.GetAwsTags(cmd, r, args.Tags)

	mpBuilder := cmv1.NewMachinePool().
		ID(name).
		InstanceType(instanceType).
		Labels(labelMap).
		Taints(taintBuilders...)

	if autoscaling {
		mpBuilder = mpBuilder.Autoscaling(
			cmv1.NewMachinePoolAutoscaling().
				MinReplicas(minReplicas).
				MaxReplicas(maxReplicas))
	} else {
		mpBuilder = mpBuilder.Replicas(replicas)
	}

	awsMpBuilder := cmv1.NewAWSMachinePool()
	if useSpotInstances {
		spotBuilder := cmv1.NewAWSSpotMarketOptions()
		if maxPrice != nil {
			spotBuilder = spotBuilder.MaxPrice(*maxPrice)
		}
		awsMpBuilder.SpotMarketOptions(spotBuilder)
	}
	if len(securityGroupIds) > 0 {
		awsMpBuilder.AdditionalSecurityGroupIds(securityGroupIds...)
	}
	if len(awsTags) > 0 {
		awsMpBuilder.Tags(awsTags)
	}
	mpBuilder.AWS(awsMpBuilder)

	// Create a single AZ machine pool for a multi-AZ cluster
	if cluster.MultiAZ() && !multiAZMachinePool && availabilityZone != "" {
		mpBuilder.AvailabilityZones(availabilityZone)
	}

	// Create a single AZ machine pool for a BYOVPC cluster
	if subnet != "" {
		mpBuilder.Subnets(subnet)
	}

	_, _, _, _, defaultRootDiskSize, _ :=
		r.OCMClient.GetDefaultClusterFlavors(cluster.Flavour().ID())

	if args.RootDiskSize != "" || interactive.Enabled() {
		var rootDiskSizeStr string
		if args.RootDiskSize == "" {
			// We don't need to parse the default since it's returned from the OCM API and AWS
			// always defaults to GiB
			rootDiskSizeStr = helper.GigybyteStringer(defaultRootDiskSize)
		} else {
			rootDiskSizeStr = args.RootDiskSize
		}
		if interactive.Enabled() {
			// In order to avoid confusion, we want to display to the user what was passed as an
			// argument
			// Even if it was not valid, we want to display it to the user, then the CLI will show an
			// error and the value can be corrected
			// Also, if nothing is given, we want to display the default value fetched from the OCM API
			rootDiskSizeStr, err = interactive.GetString(interactive.Input{
				Question: "Root disk size (GiB or TiB)",
				Help:     cmd.Flags().Lookup("disk-size").Usage,
				Default:  rootDiskSizeStr,
				Validators: []interactive.Validator{
					interactive.MachinePoolRootDiskSizeValidator(cluster.Version().RawID()),
				},
			})
			if err != nil {
				return fmt.Errorf("Expected a valid machine pool root disk size value: %v", err)
			}
		}

		// Parse the value given by either CLI or interactive mode and return it in GigiBytes
		rootDiskSize, err := ocm.ParseDiskSizeToGigibyte(rootDiskSizeStr)
		if err != nil {
			return fmt.Errorf("Expected a valid machine pool root disk size value '%s': %v", rootDiskSizeStr, err)
		}

		err = diskValidator.ValidateMachinePoolRootDiskSize(cluster.Version().RawID(), rootDiskSize)
		if err != nil {
			return err
		}

		// If the size given by the user is different than the default, we just let the OCM server
		// handle the default root disk size
		if rootDiskSize != defaultRootDiskSize {
			mpBuilder.RootVolume(cmv1.NewRootVolume().AWS(cmv1.NewAWSVolume().Size(rootDiskSize)))
		}
	}

	machinePool, err := mpBuilder.Build()
	if err != nil {
		return fmt.Errorf("Failed to create machine pool for cluster '%s': %v", clusterKey, err)
	}

	createdMachinePool, err := r.OCMClient.CreateMachinePool(cluster.ID(), machinePool)
	if err != nil {
		return fmt.Errorf("Failed to add machine pool to cluster '%s': %v", clusterKey, err)
	}

	if output.HasFlag() {
		if err = output.Print(createdMachinePool); err != nil {
			return fmt.Errorf("Unable to print machine pool: %v", err)
		}
	} else {
		r.Reporter.Infof("Machine pool '%s' created successfully on cluster '%s'", name, clusterKey)
		r.Reporter.Infof("To view the machine pool details, run 'rosa describe machinepool --cluster %s --machinepool %s'",
			clusterKey, name)
		r.Reporter.Infof("To view all machine pools, run 'rosa list machinepools --cluster %s'", clusterKey)
	}

	return nil
}

func Split(r rune) bool {
	return r == '=' || r == ':'
}

// getMachinePoolAvailabilityZones derives the availability zone from the user input or the cluster spec
func getMachinePoolAvailabilityZones(r *rosa.Runtime, cluster *cmv1.Cluster, multiAZMachinePool bool,
	availabilityZoneUserInput string, subnetUserInput string) ([]string, error) {
	// Single AZ machine pool for a multi-AZ cluster
	if cluster.MultiAZ() && !multiAZMachinePool && availabilityZoneUserInput != "" {
		return []string{availabilityZoneUserInput}, nil
	}

	// Single AZ machine pool for a BYOVPC cluster
	if subnetUserInput != "" {
		availabilityZone, err := r.AWSClient.GetSubnetAvailabilityZone(subnetUserInput)
		if err != nil {
			return []string{}, err
		}

		return []string{availabilityZone}, nil
	}

	// Default option of cluster's nodes availability zones
	return cluster.Nodes().AvailabilityZones(), nil
}

func minReplicaValidator(multiAZMachinePool bool) interactive.Validator {
	return func(val interface{}) error {
		minReplicas, err := strconv.Atoi(fmt.Sprintf("%v", val))
		if err != nil {
			return err
		}
		if minReplicas < 0 {
			return fmt.Errorf("min-replicas must be a non-negative integer")
		}
		if multiAZMachinePool && minReplicas%3 != 0 {
			return fmt.Errorf("Multi AZ clusters require that the replicas be a multiple of 3")
		}
		return nil
	}
}

func maxReplicaValidator(minReplicas int, multiAZMachinePool bool) interactive.Validator {
	return func(val interface{}) error {
		maxReplicas, err := strconv.Atoi(fmt.Sprintf("%v", val))
		if err != nil {
			return err
		}
		if minReplicas > maxReplicas {
			return fmt.Errorf("max-replicas must be greater or equal to min-replicas")
		}
		if multiAZMachinePool && maxReplicas%3 != 0 {
			return fmt.Errorf("Multi AZ clusters require that the replicas be a multiple of 3")
		}
		return nil
	}
}

func spotMaxPriceValidator(val interface{}) error {
	spotMaxPrice := fmt.Sprintf("%v", val)
	if spotMaxPrice == "on-demand" {
		return nil
	}
	price, err := strconv.ParseFloat(spotMaxPrice, commonUtils.MaxByteSize)
	if err != nil {
		return fmt.Errorf("Expected a numeric value for spot max price")
	}

	if price <= 0 {
		return fmt.Errorf("Spot max price must be positive")
	}
	return nil
}

func (m *machinePool) CreateNodePools(r *rosa.Runtime, cmd *cobra.Command, clusterKey string, cluster *cmv1.Cluster,
	args *CreateMachinepoolUserOptions) error {

	var err error
	isAvailabilityZoneSet := cmd.Flags().Changed("availability-zone")
	isSubnetSet := cmd.Flags().Changed("subnet")
	if isSubnetSet && isAvailabilityZoneSet {
		return fmt.Errorf("Setting both `subnet` and `availability-zone` flag is not supported." +
			" Please select `subnet` or `availability-zone` to create a single availability zone machine pool")
	}

	// Machine pool name:
	name := strings.Trim(args.Name, " \t")
	if name == "" && !interactive.Enabled() {
		interactive.Enable()
		r.Reporter.Infof("Enabling interactive mode")
	}
	if name == "" || interactive.Enabled() {
		name, err = interactive.GetString(interactive.Input{
			Question: "Machine pool name",
			Default:  name,
			Required: true,
			Validators: []interactive.Validator{
				interactive.RegExp(machinePoolKeyRE.String()),
			},
		})
		if err != nil {
			return fmt.Errorf("Expected a valid name for the machine pool: %s", err)
		}
	}
	name = strings.Trim(name, " \t")
	if !machinePoolKeyRE.MatchString(name) {
		return fmt.Errorf("Expected a valid name for the machine pool")
	}

	// OpenShift version:
	isVersionSet := cmd.Flags().Changed("version")
	version := args.Version
	if isVersionSet || interactive.Enabled() {
		// NodePool will take channel group from the cluster
		channelGroup := cluster.Version().ChannelGroup()
		clusterVersion := cluster.Version().RawID()
		// This is called in HyperShift, but we don't want to exclude version which are HCP disabled for node pools
		// so we pass the relative parameter as false
		_, versionList, err := versions.GetVersionList(r, channelGroup, true, true, false, false)
		if err != nil {
			return err
		}

		// Calculate the minimal version for a new hosted machine pool
		minVersion, err := versions.GetMinimalHostedMachinePoolVersion(clusterVersion)
		if err != nil {
			return err
		}

		// Filter the available list of versions for a hosted machine pool
		filteredVersionList := versions.GetFilteredVersionList(versionList, minVersion, clusterVersion)
		if err != nil {
			return err
		}

		if version == "" {
			version = clusterVersion
		}
		if interactive.Enabled() {
			version, err = interactive.GetOption(interactive.Input{
				Question: "OpenShift version",
				Help:     cmd.Flags().Lookup("version").Usage,
				Options:  filteredVersionList,
				Default:  version,
				Required: true,
			})
			if err != nil {
				return fmt.Errorf("Expected a valid OpenShift version: %s", err)
			}
		}
		// This is called in HyperShift, but we don't want to exclude version which are HCP disabled for node pools
		// so we pass the relative parameter as false
		version, err = r.OCMClient.ValidateVersion(version, filteredVersionList, channelGroup, true, false)
		if err != nil {
			return fmt.Errorf("Expected a valid OpenShift version: %s", err)
		}
	}

	// Allow the user to select subnet for a single AZ BYOVPC cluster
	subnet, err := getSubnetFromUser(cmd, r, isSubnetSet, cluster, args)
	if err != nil {
		return err
	}

	// Select availability zone if the user didn't select subnet
	if subnet == "" {
		subnet, err = getSubnetFromAvailabilityZone(cmd, r, isAvailabilityZoneSet, cluster, args)
		if err != nil {
			return err
		}
	}

	isMinReplicasSet := cmd.Flags().Changed("min-replicas")
	isMaxReplicasSet := cmd.Flags().Changed("max-replicas")
	isAutoscalingSet := cmd.Flags().Changed("enable-autoscaling")
	isReplicasSet := cmd.Flags().Changed("replicas")

	minReplicas := args.MinReplicas
	maxReplicas := args.MaxReplicas
	autoscaling := args.AutoscalingEnabled
	replicas := args.Replicas

	// Autoscaling
	if !isReplicasSet && !autoscaling && !isAutoscalingSet && interactive.Enabled() {
		autoscaling, err = interactive.GetBool(interactive.Input{
			Question: "Enable autoscaling",
			Help:     cmd.Flags().Lookup("enable-autoscaling").Usage,
			Default:  autoscaling,
			Required: false,
		})
		if err != nil {
			return fmt.Errorf("Expected a valid value for enable-autoscaling: %s", err)
		}
	}

	// TODO Update the autoscaling input validator when multi-AZ is implemented
	if autoscaling {
		// if the user set replicas and enabled autoscaling
		if isReplicasSet {
			return fmt.Errorf("Replicas can't be set when autoscaling is enabled")
		}
		if interactive.Enabled() || !isMinReplicasSet {
			minReplicas, err = interactive.GetInt(interactive.Input{
				Question: "Min replicas",
				Help:     cmd.Flags().Lookup("min-replicas").Usage,
				Default:  minReplicas,
				Required: true,
				Validators: []interactive.Validator{
					machinepools.MinNodePoolReplicaValidator(true),
				},
			})
			if err != nil {
				return fmt.Errorf("Expected a valid number of min replicas: %s", err)
			}
		}
		err = machinepools.MinNodePoolReplicaValidator(true)(minReplicas)
		if err != nil {
			return err
		}

		if interactive.Enabled() || !isMaxReplicasSet {
			maxReplicas, err = interactive.GetInt(interactive.Input{
				Question: "Max replicas",
				Help:     cmd.Flags().Lookup("max-replicas").Usage,
				Default:  maxReplicas,
				Required: true,
				Validators: []interactive.Validator{
					machinepools.MaxNodePoolReplicaValidator(minReplicas),
				},
			})
			if err != nil {
				return fmt.Errorf("Expected a valid number of max replicas: %s", err)
			}
		}
		err = machinepools.MaxNodePoolReplicaValidator(minReplicas)(maxReplicas)
		if err != nil {
			return err
		}
	} else {
		// if the user set min/max replicas and hasn't enabled autoscaling
		if isMinReplicasSet || isMaxReplicasSet {
			return fmt.Errorf("Autoscaling must be enabled in order to set min and max replicas")
		}
		if interactive.Enabled() || !isReplicasSet {
			replicas, err = interactive.GetInt(interactive.Input{
				Question: "Replicas",
				Help:     cmd.Flags().Lookup("replicas").Usage,
				Default:  replicas,
				Required: true,
				Validators: []interactive.Validator{
					machinepools.MinNodePoolReplicaValidator(false),
				},
			})
			if err != nil {
				return fmt.Errorf("Expected a valid number of replicas: %s", err)
			}
		}
		err = machinepools.MinNodePoolReplicaValidator(false)(replicas)
		if err != nil {
			return err
		}
	}

	existingLabels := make(map[string]string, 0)
	labelMap := machinepools.GetLabelMap(cmd, r, existingLabels, args.Labels)

	existingTaints := make([]*cmv1.Taint, 0)
	taintBuilders := machinepools.GetTaints(cmd, r, existingTaints, args.Taints)

	isSecurityGroupIdsSet := cmd.Flags().Changed(securitygroups.MachinePoolSecurityGroupFlag)
	securityGroupIds := args.SecurityGroupIds
	isVersionCompatibleSecurityGroupIds, err := features.IsFeatureSupported(
		features.AdditionalDay2SecurityGroupsHcpFeature, version)
	if err != nil {
		return err
	}
	if interactive.Enabled() && !isSecurityGroupIdsSet && isVersionCompatibleSecurityGroupIds {
		securityGroupIds, err = getSecurityGroupsOption(r, cmd, cluster)
		if err != nil {
			return err
		}
	}
	for i, sg := range securityGroupIds {
		securityGroupIds[i] = strings.TrimSpace(sg)
	}

	awsTags := machinepools.GetAwsTags(cmd, r, args.Tags)

	npBuilder := cmv1.NewNodePool()
	npBuilder.ID(name).Labels(labelMap).
		Taints(taintBuilders...)

	if autoscaling {
		npBuilder = npBuilder.Autoscaling(
			cmv1.NewNodePoolAutoscaling().
				MinReplica(minReplicas).
				MaxReplica(maxReplicas))
	} else {
		npBuilder = npBuilder.Replicas(replicas)
	}

	if subnet != "" {
		npBuilder.Subnet(subnet)
	}

	// Machine pool instance type:
	// NodePools don't support MultiAZ yet, so the availabilityZonesFilters is calculated from the cluster

	// Machine pool instance type:
	instanceType := args.InstanceType
	if instanceType == "" && !interactive.Enabled() {
		return fmt.Errorf("You must supply a valid instance type")
	}

	var spin *spinner.Spinner
	if r.Reporter.IsTerminal() && !output.HasFlag() {
		spin = spinner.New(spinner.CharSets[9], 100*time.Millisecond)
	}
	if spin != nil {
		r.Reporter.Infof("Checking available instance types for machine pool '%s'", name)
		spin.Start()
	}

	availabilityZonesFilter := cluster.Nodes().AvailabilityZones()

	// If the user selects a subnet which is in a different AZ than day 1, the instance type list should be filter
	// by the new AZ not the cluster ones
	if subnet != "" {
		availabilityZone, err := r.AWSClient.GetSubnetAvailabilityZone(subnet)
		if err != nil {
			return fmt.Errorf(fmt.Sprintf("%s", err))
		}
		availabilityZonesFilter = []string{availabilityZone}
	}

	instanceTypeList, err := r.OCMClient.GetAvailableMachineTypesInRegion(cluster.Region().ID(),
		availabilityZonesFilter, cluster.AWS().STS().RoleARN(), r.AWSClient)
	if err != nil {
		return fmt.Errorf(fmt.Sprintf("%s", err))
	}

	if spin != nil {
		spin.Stop()
	}

	if interactive.Enabled() {
		if instanceType == "" {
			instanceType = instanceTypeList.Items[0].MachineType.ID()
		}
		instanceType, err = interactive.GetOption(interactive.Input{
			Question: "Instance type",
			Help:     cmd.Flags().Lookup("instance-type").Usage,
			Options:  instanceTypeList.GetAvailableIDs(cluster.MultiAZ()),
			Default:  instanceType,
			Required: true,
		})
		if err != nil {
			return fmt.Errorf("Expected a valid instance type: %s", err)
		}
	}

	err = instanceTypeList.ValidateMachineType(instanceType, cluster.MultiAZ())
	if err != nil {
		return fmt.Errorf("Expected a valid instance type: %s", err)
	}

	autorepair := args.Autorepair
	if interactive.Enabled() {
		autorepair, err = interactive.GetBool(interactive.Input{
			Question: "Autorepair",
			Help:     cmd.Flags().Lookup("autorepair").Usage,
			Default:  autorepair,
			Required: false,
		})
		if err != nil {
			return fmt.Errorf("Expected a valid value for autorepair: %s", err)
		}
	}

	npBuilder.AutoRepair(autorepair)

	var inputTuningConfig []string
	tuningConfigs := args.TuningConfigs
	// Get the list of available tuning configs
	availableTuningConfigs, err := r.OCMClient.GetTuningConfigsName(cluster.ID())
	if err != nil {
		return err
	}
	if tuningConfigs != "" {
		if len(availableTuningConfigs) > 0 {
			inputTuningConfig = strings.Split(tuningConfigs, ",")
		} else {
			// Parameter will be ignored
			r.Reporter.Warnf("No tuning config available for cluster '%s'. "+
				"Any tuning config in input will be ignored", cluster.ID())
		}
	}
	if interactive.Enabled() {
		// Skip if no tuning configs are available
		if len(availableTuningConfigs) > 0 {
			inputTuningConfig, err = interactive.GetMultipleOptions(interactive.Input{
				Question: "Tuning configs",
				Help:     cmd.Flags().Lookup("tuning-configs").Usage,
				Options:  availableTuningConfigs,
				Default:  inputTuningConfig,
				Required: false,
			})
			if err != nil {
				return fmt.Errorf("Expected a valid value for tuning configs: %s", err)
			}
		}
	}

	if len(inputTuningConfig) != 0 {
		npBuilder.TuningConfigs(inputTuningConfig...)
	}

	kubeletConfigs := args.KubeletConfigs

	if kubeletConfigs != "" || interactive.Enabled() {
		var inputKubeletConfigs []string
		// Get the list of available kubelet configs
		availableKubeletConfigs, err := r.OCMClient.ListKubeletConfigNames(cluster.ID())
		if err != nil {
			return err
		}

		if len(availableKubeletConfigs) > 0 {
			inputKubeletConfigs = strings.Split(kubeletConfigs, ",")
		} else {
			// Parameter will be ignored
			r.Reporter.Warnf("No kubelet configs available for cluster '%s'. "+
				"Any kubelet config in input will be ignored", cluster.ID())
		}

		if interactive.Enabled() {
			// Skip if no kubelet configs are available
			if len(availableKubeletConfigs) > 0 {
				inputKubeletConfigs, err = interactive.GetMultipleOptions(interactive.Input{
					Question: "Kubelet config",
					Help:     cmd.Flags().Lookup("kubelet-configs").Usage,
					Options:  availableKubeletConfigs,
					Default:  inputKubeletConfigs,
					Required: false,
					Validators: []interactive.Validator{
						ValidateKubeletConfig,
					},
				})
				if err != nil {
					return fmt.Errorf("Expected a valid value for kubelet config: %s", err)
				}
			}
		}

		err = ValidateKubeletConfig(inputKubeletConfigs)
		if err != nil {
			return fmt.Errorf(err.Error())
		}

		if len(inputKubeletConfigs) != 0 {
			npBuilder.KubeletConfigs(inputKubeletConfigs...)
		}
	}

	npBuilder.AWSNodePool(createAwsNodePoolBuilder(instanceType, securityGroupIds, awsTags))

	nodeDrainGracePeriod := args.NodeDrainGracePeriod
	if interactive.Enabled() {
		nodeDrainGracePeriod, err = interactive.GetString(interactive.Input{
			Question: "Node drain grace period",
			Help:     cmd.Flags().Lookup("node-drain-grace-period").Usage,
			Default:  nodeDrainGracePeriod,
			Required: false,
			Validators: []interactive.Validator{
				machinepools.ValidateNodeDrainGracePeriod,
			},
		})
		if err != nil {
			return fmt.Errorf("Expected a valid value for Node drain grace period: %s", err)
		}
	}
	if nodeDrainGracePeriod != "" {
		nodeDrainBuilder, err := machinepools.CreateNodeDrainGracePeriodBuilder(nodeDrainGracePeriod)
		if err != nil {
			return fmt.Errorf(err.Error())
		}
		npBuilder.NodeDrainGracePeriod(nodeDrainBuilder)
	}

	if version != "" {
		npBuilder.Version(cmv1.NewVersion().ID(version))
	}

	nodePool, err := npBuilder.Build()
	if err != nil {
		return fmt.Errorf("Failed to create machine pool for hosted cluster '%s': %v", clusterKey, err)
	}

	createdNodePool, err := r.OCMClient.CreateNodePool(cluster.ID(), nodePool)
	if err != nil {
		return fmt.Errorf("Failed to add machine pool to hosted cluster '%s': %v", clusterKey, err)
	}

	if output.HasFlag() {
		if err = output.Print(createdNodePool); err != nil {
			return fmt.Errorf("Unable to print machine pool: %v", err)
		}
	} else {
		r.Reporter.Infof("Machine pool '%s' created successfully on hosted cluster '%s'", createdNodePool.ID(), clusterKey)
		r.Reporter.Infof("To view the machine pool details, run 'rosa describe machinepool --cluster %s --machinepool %s'",
			clusterKey, name)
		r.Reporter.Infof("To view all machine pools, run 'rosa list machinepools --cluster %s'", clusterKey)
	}

	return nil
}

func getSubnetFromAvailabilityZone(cmd *cobra.Command, r *rosa.Runtime, isAvailabilityZoneSet bool,
	cluster *cmv1.Cluster, args *CreateMachinepoolUserOptions) (string, error) {

	privateSubnets, err := r.AWSClient.GetVPCPrivateSubnets(cluster.AWS().SubnetIDs()[0])
	if err != nil {
		return "", err
	}

	// Fetching the availability zones from the VPC private subnets
	subnetsMap := make(map[string][]string)
	for _, privateSubnet := range privateSubnets {
		subnetsPerAZ, exist := subnetsMap[*privateSubnet.AvailabilityZone]
		if !exist {
			subnetsPerAZ = []string{*privateSubnet.SubnetId}
		} else {
			subnetsPerAZ = append(subnetsPerAZ, *privateSubnet.SubnetId)
		}
		subnetsMap[*privateSubnet.AvailabilityZone] = subnetsPerAZ
	}
	availabilityZones := make([]string, 0)
	for availabilizyZone := range subnetsMap {
		availabilityZones = append(availabilityZones, availabilizyZone)
	}

	availabilityZone := cluster.Nodes().AvailabilityZones()[0]
	if !isAvailabilityZoneSet && interactive.Enabled() {
		availabilityZone, err = interactive.GetOption(interactive.Input{
			Question: "AWS availability zone",
			Help:     cmd.Flags().Lookup("availability-zone").Usage,
			Options:  availabilityZones,
			Default:  availabilityZone,
			Required: true,
		})
		if err != nil {
			return "", fmt.Errorf("Expected a valid AWS availability zone: %s", err)
		}
	} else if isAvailabilityZoneSet {
		availabilityZone = args.AvailabilityZone
	}

	if subnets, ok := subnetsMap[availabilityZone]; ok {
		if len(subnets) == 1 {
			return subnets[0], nil
		}
		r.Reporter.Infof("There are several subnets for availability zone '%s'", availabilityZone)
		interactive.Enable()
		subnet, err := getSubnetFromUser(cmd, r, false, cluster, args)
		if err != nil {
			return "", err
		}
		return subnet, nil
	}

	return "", fmt.Errorf("Failed to find a private subnet for '%s' availability zone", availabilityZone)
}

// ListMachinePools lists all machinepools (or, nodepools if hypershift) in a cluster
func (m *machinePool) ListMachinePools(r *rosa.Runtime, clusterKey string, cluster *cmv1.Cluster) error {
	// Load any existing machine pools for this cluster
	r.Reporter.Debugf("Loading machine pools for cluster '%s'", clusterKey)
	isHypershift := cluster.Hypershift().Enabled()
	var err error
	var machinePools []*cmv1.MachinePool
	var nodePools []*cmv1.NodePool
	if isHypershift {
		nodePools, err = r.OCMClient.GetNodePools(cluster.ID())
		if err != nil {
			return err
		}
	} else {
		machinePools, err = r.OCMClient.GetMachinePools(cluster.ID())
		if err != nil {
			return err
		}
	}

	if output.HasFlag() {
		if isHypershift {
			return output.Print(nodePools)
		}
		return output.Print(machinePools)
	}

	// Create the writer that will be used to print the tabulated results:
	writer := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	finalStringToOutput := getMachinePoolsString(machinePools)
	if isHypershift {
		finalStringToOutput = getNodePoolsString(nodePools)
	}
	fmt.Fprint(writer, finalStringToOutput)
	writer.Flush()
	return nil
}

// DescribeMachinePool describes either a machinepool, or, a nodepool (if hypershift)
func (m *machinePool) DescribeMachinePool(r *rosa.Runtime, cluster *cmv1.Cluster, clusterKey string,
	machinePoolId string) error {
	if cluster.Hypershift().Enabled() {
		return m.describeNodePool(r, cluster, clusterKey, machinePoolId)
	}

	r.Reporter.Debugf(fetchMessage, "machine pool", machinePoolId, clusterKey)
	machinePool, exists, err := r.OCMClient.GetMachinePool(cluster.ID(), machinePoolId)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf(notFoundMessage, machinePoolId)
	}

	if output.HasFlag() {
		return output.Print(machinePool)
	}

	fmt.Print(machinePoolOutput(cluster.ID(), machinePool))

	return nil
}

func (m *machinePool) describeNodePool(r *rosa.Runtime, cluster *cmv1.Cluster, clusterKey string,
	nodePoolId string) error {
	r.Reporter.Debugf(fetchMessage, "node pool", nodePoolId, clusterKey)
	nodePool, exists, err := r.OCMClient.GetNodePool(cluster.ID(), nodePoolId)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf(notFoundMessage, nodePoolId)
	}

	_, scheduledUpgrade, err := r.OCMClient.GetHypershiftNodePoolUpgrade(cluster.ID(), clusterKey, nodePoolId)
	if err != nil {
		return err
	}

	if output.HasFlag() {
		var formattedOutput map[string]interface{}
		formattedOutput, err = formatNodePoolOutput(nodePool, scheduledUpgrade)
		if err != nil {
			return err
		}
		return output.Print(formattedOutput)
	}

	// Attach and print scheduledUpgrades if they exist, otherwise, print output normally
	fmt.Print(appendUpgradesIfExist(scheduledUpgrade, nodePoolOutput(cluster.ID(), nodePool)))

	return nil
}

// Regular expression to used to make sure that the identifier given by the
// user is safe and that it there is no risk of SQL injection:
var MachinePoolKeyRE = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// DeleteMachinePool deletes a machinepool from a cluster if it is possible- this function also calls the hypershift
// equivalent, deleteNodePool if it is a hypershift cluster
func (m *machinePool) DeleteMachinePool(r *rosa.Runtime, machinePoolId string, clusterKey string,
	cluster *cmv1.Cluster) error {
	if cluster.Hypershift().Enabled() {
		return deleteNodePool(r, machinePoolId, clusterKey, cluster)
	}

	// Try to find the machine pool:
	r.Reporter.Debugf("Loading machine pools for cluster '%s'", clusterKey)
	machinePools, err := r.OCMClient.GetMachinePools(cluster.ID())
	if err != nil {
		return fmt.Errorf("Failed to get machine pools for cluster '%s': %v", clusterKey, err)
	}

	var machinePool *cmv1.MachinePool
	for _, item := range machinePools {
		if item.ID() == machinePoolId {
			machinePool = item
		}
	}
	if machinePool == nil {
		return fmt.Errorf("Failed to get machine pool '%s' for cluster '%s'", machinePoolId, clusterKey)
	}

	if confirm.Confirm("delete machine pool '%s' on cluster '%s'", machinePoolId, clusterKey) {
		r.Reporter.Debugf("Deleting machine pool '%s' on cluster '%s'", machinePool.ID(), clusterKey)
		err = r.OCMClient.DeleteMachinePool(cluster.ID(), machinePool.ID())
		if err != nil {
			return fmt.Errorf("Failed to delete machine pool '%s' on cluster '%s': %s",
				machinePool.ID(), clusterKey, err)
		}
		r.Reporter.Infof("Successfully deleted machine pool '%s' from cluster '%s'", machinePoolId, clusterKey)
	}
	return nil
}

// deleteNodePool is the hypershift version of DeleteMachinePool - deleteNodePool is called in DeleteMachinePool
// if the cluster is hypershift
func deleteNodePool(r *rosa.Runtime, nodePoolID string, clusterKey string, cluster *cmv1.Cluster) error {
	// Try to find the machine pool:
	r.Reporter.Debugf("Loading machine pools for hosted cluster '%s'", clusterKey)
	nodePool, exists, err := r.OCMClient.GetNodePool(cluster.ID(), nodePoolID)
	if err != nil {
		return fmt.Errorf("Failed to get machine pools for hosted cluster '%s': %v", clusterKey, err)
	}
	if !exists {
		return fmt.Errorf("Machine pool '%s' does not exist for hosted cluster '%s'", nodePoolID, clusterKey)
	}

	if confirm.Confirm("delete machine pool '%s' on hosted cluster '%s'", nodePoolID, clusterKey) {
		r.Reporter.Debugf("Deleting machine pool '%s' on hosted cluster '%s'", nodePool.ID(), clusterKey)
		err = r.OCMClient.DeleteNodePool(cluster.ID(), nodePool.ID())
		if err != nil {
			return fmt.Errorf("Failed to delete machine pool '%s' on hosted cluster '%s': %s",
				nodePool.ID(), clusterKey, err)
		}
		r.Reporter.Infof("Successfully deleted machine pool '%s' from hosted cluster '%s'", nodePoolID,
			clusterKey)
	}
	return nil
}

func formatNodePoolOutput(nodePool *cmv1.NodePool,
	scheduledUpgrade *cmv1.NodePoolUpgradePolicy) (map[string]interface{}, error) {

	var b bytes.Buffer
	err := cmv1.MarshalNodePool(nodePool, &b)
	if err != nil {
		return nil, err
	}
	ret := make(map[string]interface{})
	err = json.Unmarshal(b.Bytes(), &ret)
	if err != nil {
		return nil, err
	}
	if scheduledUpgrade != nil &&
		scheduledUpgrade.State() != nil &&
		len(scheduledUpgrade.Version()) > 0 &&
		len(scheduledUpgrade.State().Value()) > 0 {
		upgrade := make(map[string]interface{})
		upgrade["version"] = scheduledUpgrade.Version()
		upgrade["state"] = scheduledUpgrade.State().Value()
		upgrade["nextRun"] = scheduledUpgrade.NextRun().Format("2006-01-02 15:04 MST")
		ret["scheduledUpgrade"] = upgrade
	}

	return ret, nil
}

func appendUpgradesIfExist(scheduledUpgrade *cmv1.NodePoolUpgradePolicy, output string) string {
	if scheduledUpgrade != nil {
		return fmt.Sprintf("%s"+
			"Scheduled upgrade:                     %s %s on %s\n",
			output,
			scheduledUpgrade.State().Value(),
			scheduledUpgrade.Version(),
			scheduledUpgrade.NextRun().Format("2006-01-02 15:04 MST"),
		)
	}
	return output
}

func getMachinePoolsString(machinePools []*cmv1.MachinePool) string {
	outputString := "ID\tAUTOSCALING\tREPLICAS\tINSTANCE TYPE\tLABELS\t\tTAINTS\t" +
		"\tAVAILABILITY ZONES\t\tSUBNETS\t\tSPOT INSTANCES\tDISK SIZE\tSG IDs\n"
	for _, machinePool := range machinePools {
		outputString += fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t\t%s\t\t%s\t\t%s\t\t%s\t%s\t%s\n",
			machinePool.ID(),
			ocmOutput.PrintMachinePoolAutoscaling(machinePool.Autoscaling()),
			ocmOutput.PrintMachinePoolReplicas(machinePool.Autoscaling(), machinePool.Replicas()),
			machinePool.InstanceType(),
			ocmOutput.PrintLabels(machinePool.Labels()),
			ocmOutput.PrintTaints(machinePool.Taints()),
			output.PrintStringSlice(machinePool.AvailabilityZones()),
			output.PrintStringSlice(machinePool.Subnets()),
			ocmOutput.PrintMachinePoolSpot(machinePool),
			ocmOutput.PrintMachinePoolDiskSize(machinePool),
			output.PrintStringSlice(machinePool.AWS().AdditionalSecurityGroupIds()),
		)
	}
	return outputString
}

func getNodePoolsString(nodePools []*cmv1.NodePool) string {
	outputString := "ID\tAUTOSCALING\tREPLICAS\t" +
		"INSTANCE TYPE\tLABELS\t\tTAINTS\t\tAVAILABILITY ZONE\tSUBNET\tVERSION\tAUTOREPAIR\t\n"
	for _, nodePool := range nodePools {
		outputString += fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t\t%s\t\t%s\t%s\t%s\t%s\t\n",
			nodePool.ID(),
			ocmOutput.PrintNodePoolAutoscaling(nodePool.Autoscaling()),
			ocmOutput.PrintNodePoolReplicasShort(
				ocmOutput.PrintNodePoolCurrentReplicas(nodePool.Status()),
				ocmOutput.PrintNodePoolReplicasInline(nodePool.Autoscaling(), nodePool.Replicas()),
			),
			ocmOutput.PrintNodePoolInstanceType(nodePool.AWSNodePool()),
			ocmOutput.PrintLabels(nodePool.Labels()),
			ocmOutput.PrintTaints(nodePool.Taints()),
			nodePool.AvailabilityZone(),
			nodePool.Subnet(),
			ocmOutput.PrintNodePoolVersion(nodePool.Version()),
			ocmOutput.PrintNodePoolAutorepair(nodePool.AutoRepair()),
		)
	}
	return outputString
}
