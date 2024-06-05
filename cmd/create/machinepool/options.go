package machinepool

import (
	"github.com/openshift/rosa/pkg/machinepool"
	"github.com/openshift/rosa/pkg/reporter"
)

type CreateMachinepoolOptions struct {
	reporter *reporter.Object

	args *machinepool.CreateMachinepoolUserOptions
}

func NewCreateMachinepoolUserOptions() *machinepool.CreateMachinepoolUserOptions {
	return &machinepool.CreateMachinepoolUserOptions{
		InstanceType:          "m5.xlarge",
		AutoscalingEnabled:    false,
		MultiAvailabilityZone: true,
		Autorepair:            true,
	}
}

func NewCreateMachinepoolOptions() *CreateMachinepoolOptions {
	return &CreateMachinepoolOptions{
		reporter: reporter.CreateReporter(),
		args:     &machinepool.CreateMachinepoolUserOptions{},
	}
}

func (m *CreateMachinepoolOptions) Machinepool() *machinepool.CreateMachinepoolUserOptions {
	return m.args
}

func (m *CreateMachinepoolOptions) Bind(args *machinepool.CreateMachinepoolUserOptions, argv []string) error {
	m.args = args
	if len(argv) > 0 {
		m.args.Name = argv[0]
	}
	return nil
}
