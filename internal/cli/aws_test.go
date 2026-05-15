package cli

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func TestApplyAWSRunInstanceTargetOptionsEnablesNestedVirtualizationForWSL2(t *testing.T) {
	input := &ec2.RunInstancesInput{}
	applyAWSRunInstanceTargetOptions(input, Config{
		TargetOS:    targetWindows,
		WindowsMode: windowsModeWSL2,
	})
	if input.CpuOptions == nil {
		t.Fatal("CpuOptions=nil, want nested virtualization enabled")
	}
	if input.CpuOptions.NestedVirtualization != types.NestedVirtualizationSpecificationEnabled {
		t.Fatalf("NestedVirtualization=%q", input.CpuOptions.NestedVirtualization)
	}
}

func TestApplyAWSRunInstanceTargetOptionsLeavesNativeWindowsDefault(t *testing.T) {
	input := &ec2.RunInstancesInput{}
	applyAWSRunInstanceTargetOptions(input, Config{
		TargetOS:    targetWindows,
		WindowsMode: windowsModeNormal,
	})
	if input.CpuOptions != nil {
		t.Fatalf("CpuOptions=%#v, want nil", input.CpuOptions)
	}
}

func TestStaleAWSCrabboxSSHIngressPermissionsPrunesOnlyOwnedCIDRs(t *testing.T) {
	group := types.SecurityGroup{
		IpPermissions: []types.IpPermission{
			{
				FromPort:   aws.Int32(2222),
				ToPort:     aws.Int32(2222),
				IpProtocol: aws.String("tcp"),
				IpRanges: []types.IpRange{
					{CidrIp: aws.String("203.0.113.10/32"), Description: aws.String(awsSSHIngressDescription)},
					{CidrIp: aws.String("198.51.100.20/32"), Description: aws.String(awsSSHIngressDescription)},
					{CidrIp: aws.String("192.0.2.30/32"), Description: aws.String("operator access")},
				},
				Ipv6Ranges: []types.Ipv6Range{
					{CidrIpv6: aws.String("2001:db8::1/128"), Description: aws.String(awsSSHIngressDescription)},
				},
			},
			{
				FromPort:   aws.Int32(22),
				ToPort:     aws.Int32(22),
				IpProtocol: aws.String("tcp"),
				IpRanges: []types.IpRange{
					{CidrIp: aws.String("198.51.100.20/32"), Description: aws.String(awsSSHIngressDescription)},
				},
			},
			{
				FromPort:   aws.Int32(443),
				ToPort:     aws.Int32(443),
				IpProtocol: aws.String("tcp"),
				IpRanges: []types.IpRange{
					{CidrIp: aws.String("198.51.100.20/32"), Description: aws.String("operator access")},
				},
			},
		},
	}

	stale := staleAWSCrabboxSSHIngressPermissions(group, []string{"2222"}, []string{"203.0.113.10/32"})
	if len(stale) != 2 {
		t.Fatalf("len(stale)=%d, want 2: %#v", len(stale), stale)
	}
	byPort := map[int32]types.IpPermission{}
	for _, permission := range stale {
		byPort[aws.ToInt32(permission.FromPort)] = permission
	}
	currentPort := byPort[2222]
	if len(currentPort.IpRanges) != 1 || aws.ToString(currentPort.IpRanges[0].CidrIp) != "198.51.100.20/32" {
		t.Fatalf("IpRanges=%#v, want only stale Crabbox IPv4 range on current port", currentPort.IpRanges)
	}
	if len(currentPort.Ipv6Ranges) != 1 || aws.ToString(currentPort.Ipv6Ranges[0].CidrIpv6) != "2001:db8::1/128" {
		t.Fatalf("Ipv6Ranges=%#v, want only stale Crabbox IPv6 range on current port", currentPort.Ipv6Ranges)
	}
	removedPort := byPort[22]
	if len(removedPort.IpRanges) != 1 || aws.ToString(removedPort.IpRanges[0].CidrIp) != "198.51.100.20/32" {
		t.Fatalf("removed port IpRanges=%#v, want Crabbox ranges pruned from removed port", removedPort.IpRanges)
	}
}

func TestStaleAWSCrabboxSSHIngressPermissionsKeepsDefaultCIDR(t *testing.T) {
	group := types.SecurityGroup{
		IpPermissions: []types.IpPermission{
			{
				FromPort:   aws.Int32(22),
				ToPort:     aws.Int32(22),
				IpProtocol: aws.String("tcp"),
				IpRanges: []types.IpRange{
					{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String(awsSSHIngressDescription)},
				},
			},
		},
	}

	stale := staleAWSCrabboxSSHIngressPermissions(group, []string{"22"}, nil)
	if len(stale) != 0 {
		t.Fatalf("stale=%#v, want none for default fallback CIDR", stale)
	}
}
