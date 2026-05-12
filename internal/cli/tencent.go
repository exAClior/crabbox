package cli

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	tccommon "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	tcprofile "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	tccvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	tcsts "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/sts/v20180813"
	tctag "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tag/v20180813"
	tcvpc "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc/v20170312"
)

const (
	tencentChargeTypePostpaid = "POSTPAID_BY_HOUR"
	tencentSecurityGroupName  = "crabbox-runners"
	tencentSSHBandwidthMbps   = int64(10)
)

func init() {
	RegisterProvider(tencentProvider{})
}

type tencentProvider struct{}

func (tencentProvider) Name() string      { return "tencent" }
func (tencentProvider) Aliases() []string { return []string{"tencent-cloud"} }
func (tencentProvider) Spec() ProviderSpec {
	return ProviderSpec{
		Name:        "tencent",
		Kind:        ProviderKindSSHLease,
		Targets:     []TargetSpec{{OS: targetLinux}},
		Features:    FeatureSet{FeatureSSH, FeatureCrabboxSync, FeatureCleanup},
		Coordinator: CoordinatorNever,
	}
}

type tencentFlagValues struct {
	SecretID  *string
	SecretKey *string
	Region    *string
	Zone      *string
	ImageID   *string
	VpcID     *string
	SubnetID  *string
	RootGB    *int64
}

func (tencentProvider) RegisterFlags(fs *flag.FlagSet, defaults Config) any {
	return tencentFlagValues{
		SecretID:  fs.String("tencent-secret-id", defaults.TencentSecretID, "Tencent Cloud CAM SecretId"),
		SecretKey: fs.String("tencent-secret-key", defaults.TencentSecretKey, "Tencent Cloud CAM SecretKey"),
		Region:    fs.String("tencent-region", defaults.TencentRegion, "Tencent Cloud region"),
		Zone:      fs.String("tencent-zone", defaults.TencentZone, "Tencent Cloud availability zone"),
		ImageID:   fs.String("tencent-image-id", defaults.TencentImageID, "Tencent Cloud CVM image ID"),
		VpcID:     fs.String("tencent-vpc-id", defaults.TencentVpcID, "Tencent Cloud VPC ID"),
		SubnetID:  fs.String("tencent-subnet-id", defaults.TencentSubnetID, "Tencent Cloud subnet ID"),
		RootGB:    fs.Int64("tencent-root-gb", defaults.TencentRootGB, "Tencent Cloud root disk size in GB"),
	}
}

func (tencentProvider) ApplyFlags(cfg *Config, fs *flag.FlagSet, values any) error {
	v, ok := values.(tencentFlagValues)
	if !ok {
		return nil
	}
	if flagWasSet(fs, "tencent-secret-id") {
		cfg.TencentSecretID = *v.SecretID
	}
	if flagWasSet(fs, "tencent-secret-key") {
		cfg.TencentSecretKey = *v.SecretKey
	}
	if flagWasSet(fs, "tencent-region") {
		cfg.TencentRegion = *v.Region
	}
	if flagWasSet(fs, "tencent-zone") {
		cfg.TencentZone = *v.Zone
	}
	if flagWasSet(fs, "tencent-image-id") {
		cfg.TencentImageID = *v.ImageID
	}
	if flagWasSet(fs, "tencent-vpc-id") {
		cfg.TencentVpcID = *v.VpcID
	}
	if flagWasSet(fs, "tencent-subnet-id") {
		cfg.TencentSubnetID = *v.SubnetID
	}
	if flagWasSet(fs, "tencent-root-gb") {
		cfg.TencentRootGB = *v.RootGB
	}
	return nil
}

func (p tencentProvider) Configure(cfg Config, rt Runtime) (Backend, error) {
	return &tencentBackend{spec: p.Spec(), cfg: cfg, rt: rt}, nil
}

type tencentBackend struct {
	spec ProviderSpec
	cfg  Config
	rt   Runtime
}

func (b *tencentBackend) Spec() ProviderSpec { return b.spec }

func (b *tencentBackend) Acquire(ctx context.Context, req AcquireRequest) (LeaseTarget, error) {
	cfg := b.cfg
	client, err := newTencentClient(cfg)
	if err != nil {
		return LeaseTarget{}, err
	}
	leaseID := newLeaseID()
	servers, err := client.ListCrabboxServers(ctx)
	if err != nil {
		return LeaseTarget{}, err
	}
	slug := allocateDirectLeaseSlug(leaseID, servers)
	keyPath, publicKey, err := ensureTestboxKeyWithType(leaseID, "rsa")
	if err != nil {
		return LeaseTarget{}, err
	}
	cfg.SSHKey = keyPath
	cfg.ProviderKey = tencentProviderKeyForLease(leaseID)
	fmt.Fprintf(b.rt.Stderr, "provisioning provider=tencent lease=%s slug=%s class=%s preferred_type=%s region=%s zone=%s keep=%v\n", leaseID, slug, cfg.Class, cfg.ServerType, cfg.TencentRegion, cfg.TencentZone, req.Keep)
	server, cfg, err := client.CreateServerWithFallback(ctx, cfg, publicKey, leaseID, slug, req.Keep, func(format string, args ...any) {
		fmt.Fprintf(b.rt.Stderr, format, args...)
	})
	if err != nil {
		return LeaseTarget{}, err
	}
	fmt.Fprintf(b.rt.Stderr, "provisioned lease=%s server=%s type=%s\n", leaseID, server.DisplayID(), cfg.ServerType)
	server, err = client.waitForServerIP(ctx, server.CloudID)
	if err != nil {
		return LeaseTarget{}, err
	}
	target := sshTargetFromConfig(cfg, server.PublicNet.IPv4.IP)
	if err := waitForSSH(ctx, &target, b.rt.Stderr); err != nil {
		if !req.Keep {
			_ = client.DeleteServer(context.Background(), server.CloudID)
		}
		return LeaseTarget{}, err
	}
	server.Labels["state"] = "ready"
	if err := client.SetLabels(ctx, server.CloudID, server.Labels); err != nil {
		fmt.Fprintf(b.rt.Stderr, "warning: set tags: %v\n", err)
	}
	return LeaseTarget{Server: server, SSH: target, LeaseID: leaseID}, nil
}

func (b *tencentBackend) Resolve(ctx context.Context, req ResolveRequest) (LeaseTarget, error) {
	client, err := newTencentClient(b.cfg)
	if err != nil {
		return LeaseTarget{}, err
	}
	var server Server
	var leaseID string
	if strings.HasPrefix(req.ID, "ins-") {
		server, err = client.GetServer(ctx, req.ID)
		if err != nil {
			return LeaseTarget{}, err
		}
		leaseID = blank(server.Labels["lease"], req.ID)
	} else {
		servers, err := client.ListCrabboxServers(ctx)
		if err != nil {
			return LeaseTarget{}, err
		}
		if server, leaseID, err = findServerByAlias(servers, req.ID); err != nil {
			return LeaseTarget{}, err
		} else if leaseID == "" {
			return LeaseTarget{}, exit(4, "lease/server not found: %s", req.ID)
		}
	}
	target := sshTargetFromConfig(b.cfg, server.PublicNet.IPv4.IP)
	useStoredTestboxKey(&target, leaseID)
	return LeaseTarget{Server: server, SSH: target, LeaseID: leaseID}, nil
}

func (b *tencentBackend) List(ctx context.Context, req ListRequest) ([]LeaseView, error) {
	client, err := newTencentClient(b.cfg)
	if err != nil {
		return nil, err
	}
	servers, err := client.ListCrabboxServers(ctx)
	if err != nil {
		return nil, err
	}
	return servers, nil
}

func (b *tencentBackend) ReleaseLease(ctx context.Context, req ReleaseLeaseRequest) error {
	client, err := newTencentClient(b.cfg)
	if err != nil {
		return err
	}
	if req.Lease.Server.CloudID != "" {
		if err := client.DeleteServer(ctx, req.Lease.Server.CloudID); err != nil {
			return err
		}
	}
	if keyName := serverProviderKey(req.Lease.Server); validTencentProviderKey(keyName) {
		return client.DeleteSSHKey(ctx, keyName)
	}
	return nil
}

func (b *tencentBackend) Touch(ctx context.Context, req TouchRequest) (Server, error) {
	client, err := newTencentClient(b.cfg)
	if err != nil {
		return req.Lease.Server, err
	}
	server := req.Lease.Server
	if server.Labels == nil {
		server.Labels = map[string]string{}
	}
	server.Labels = touchDirectLeaseLabels(server.Labels, b.cfg, req.State, time.Now().UTC())
	if err := client.SetLabels(ctx, server.CloudID, server.Labels); err != nil {
		return server, err
	}
	return server, nil
}

type TencentClient struct {
	cvm       *tccvm.Client
	vpc       *tcvpc.Client
	tag       *tctag.Client
	sts       *tcsts.Client
	region    string
	accountID string
	cfg       Config
}

func newTencentClient(cfg Config) (*TencentClient, error) {
	secretID := strings.TrimSpace(cfg.TencentSecretID)
	if secretID == "" {
		secretID = getenv("TENCENT_SECRET_ID", getenv("TENCENTCLOUD_SECRET_ID", ""))
	}
	secretKey := strings.TrimSpace(cfg.TencentSecretKey)
	if secretKey == "" {
		secretKey = getenv("TENCENT_SECRET_KEY", getenv("TENCENTCLOUD_SECRET_KEY", ""))
	}
	region := strings.TrimSpace(cfg.TencentRegion)
	if region == "" {
		region = getenv("CRABBOX_TENCENT_REGION", "")
	}
	if secretID == "" || secretKey == "" {
		return nil, exit(3, "TENCENT_SECRET_ID and TENCENT_SECRET_KEY are required")
	}
	if region == "" {
		return nil, exit(3, "tencent.region or CRABBOX_TENCENT_REGION is required")
	}

	credential := tccommon.NewCredential(secretID, secretKey)
	cvmClient, err := tccvm.NewClient(credential, region, tcprofile.NewClientProfile())
	if err != nil {
		return nil, err
	}
	vpcClient, err := tcvpc.NewClient(credential, region, tcprofile.NewClientProfile())
	if err != nil {
		return nil, err
	}
	tagClient, err := tctag.NewClient(credential, region, tcprofile.NewClientProfile())
	if err != nil {
		return nil, err
	}
	stsClient, err := tcsts.NewClient(credential, region, tcprofile.NewClientProfile())
	if err != nil {
		return nil, err
	}
	cfg.TencentSecretID = secretID
	cfg.TencentSecretKey = secretKey
	cfg.TencentRegion = region
	return &TencentClient{cvm: cvmClient, vpc: vpcClient, tag: tagClient, sts: stsClient, region: region, cfg: cfg}, nil
}

func (c *TencentClient) ListCrabboxServers(ctx context.Context) ([]Server, error) {
	servers := make([]Server, 0)
	offset := int64(0)
	limit := int64(100)
	for {
		req := tccvm.NewDescribeInstancesRequest()
		req.Filters = []*tccvm.Filter{
			{Name: tccommon.StringPtr("tag:crabbox"), Values: tccommon.StringPtrs([]string{"true"})},
		}
		req.Offset = tccommon.Int64Ptr(offset)
		req.Limit = tccommon.Int64Ptr(limit)
		resp, err := c.cvm.DescribeInstancesWithContext(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp == nil || resp.Response == nil {
			return servers, nil
		}
		set := resp.Response.InstanceSet
		for _, instance := range set {
			if instance != nil {
				servers = append(servers, tencentInstanceToServer(instance))
			}
		}
		if len(set) == 0 || int64(len(set)) < limit {
			return servers, nil
		}
		offset += int64(len(set))
		if resp.Response.TotalCount != nil && offset >= *resp.Response.TotalCount {
			return servers, nil
		}
	}
}

func (c *TencentClient) EnsureSSHKey(ctx context.Context, name, publicKey string) (string, error) {
	name = tencentKeyName(name)
	key, err := c.findSSHKeyByName(ctx, name)
	if err != nil {
		return "", err
	}
	if key != nil {
		if strings.TrimSpace(tcString(key.PublicKey)) != strings.TrimSpace(publicKey) {
			return "", exit(3, "tencent ssh key %q exists with different public key", name)
		}
		return tcString(key.KeyId), nil
	}

	req := tccvm.NewImportKeyPairRequest()
	req.KeyName = tccommon.StringPtr(name)
	req.ProjectId = tccommon.Int64Ptr(0)
	req.PublicKey = tccommon.StringPtr(publicKey)
	req.TagSpecification = []*tccvm.TagSpecification{
		{ResourceType: tccommon.StringPtr("keypair"), Tags: tencentCVMTags(map[string]string{"crabbox": "true", "created_by": "crabbox"})},
	}
	resp, err := c.cvm.ImportKeyPairWithContext(ctx, req)
	if err != nil {
		// ImportKeyPair is not idempotent. If another process won the race, reuse it.
		if strings.Contains(tencentErrorCode(err), "Duplicate") || strings.Contains(err.Error(), "already") {
			key, findErr := c.findSSHKeyByName(ctx, name)
			if findErr == nil && key != nil {
				return tcString(key.KeyId), nil
			}
		}
		return "", err
	}
	if resp == nil || resp.Response == nil || tcString(resp.Response.KeyId) == "" {
		return "", exit(5, "tencent returned no ssh key id")
	}
	return tcString(resp.Response.KeyId), nil
}

func (c *TencentClient) DeleteSSHKey(ctx context.Context, name string) error {
	name = tencentKeyName(name)
	key, err := c.findSSHKeyByName(ctx, name)
	if err != nil {
		return err
	}
	if key == nil || tcString(key.KeyId) == "" {
		return nil
	}
	req := tccvm.NewDeleteKeyPairsRequest()
	req.KeyIds = tccommon.StringPtrs([]string{tcString(key.KeyId)})
	_, err = c.cvm.DeleteKeyPairsWithContext(ctx, req)
	if isTencentKeyNotFound(err) {
		return nil
	}
	return err
}

func (c *TencentClient) CreateServerWithFallback(ctx context.Context, cfg Config, publicKey, leaseID, slug string, keep bool, logf func(string, ...any)) (Server, Config, error) {
	if cfg.ProviderKey == "" {
		cfg.ProviderKey = tencentProviderKeyForLease(leaseID)
	}
	cfg.ProviderKey = tencentKeyName(cfg.ProviderKey)
	keyID, err := c.EnsureSSHKey(ctx, cfg.ProviderKey, publicKey)
	if err != nil {
		return Server{}, cfg, err
	}
	securityGroupID, err := c.ensureSecurityGroup(ctx, cfg)
	if err != nil {
		return Server{}, cfg, err
	}
	candidates := tencentLaunchCandidates(cfg)
	var errs []error
	for i, instanceType := range candidates {
		next := cfg
		next.ServerType = instanceType
		if i > 0 && logf != nil {
			logf("fallback provisioning type=%s after capacity rejection\n", instanceType)
		}
		server, err := c.createServer(ctx, next, publicKey, leaseID, slug, keep, keyID, securityGroupID)
		if err == nil {
			return server, next, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", instanceType, err))
		if !isRetryableTencentProvisioningError(err) {
			return Server{}, next, joinErrors(errs)
		}
	}
	if cfg.ServerTypeExplicit {
		return Server{}, cfg, fmt.Errorf("requested exact Tencent instance type %s failed; remove --type to allow class fallback: %w", cfg.ServerType, joinErrors(errs))
	}
	return Server{}, cfg, joinErrors(errs)
}

func (c *TencentClient) createServer(ctx context.Context, cfg Config, publicKey, leaseID, slug string, keep bool, keyID, securityGroupID string) (Server, error) {
	if cfg.TencentZone == "" {
		return Server{}, exit(3, "tencent.zone is required")
	}
	if cfg.TencentImageID == "" {
		return Server{}, exit(3, "tencent.imageId is required")
	}
	vpcID, subnetID, err := tencentNetwork(cfg)
	if err != nil {
		return Server{}, err
	}
	rootGB := cfg.TencentRootGB
	if rootGB <= 0 {
		rootGB = 400
	}
	name := leaseProviderName(leaseID, slug)
	now := time.Now().UTC()
	labels := directLeaseLabels(cfg, leaseID, slug, "tencent", "on-demand", keep, now)
	one := int64(1)
	req := tccvm.NewRunInstancesRequest()
	req.InstanceChargeType = tccommon.StringPtr(tencentChargeTypePostpaid)
	req.Placement = &tccvm.Placement{Zone: tccommon.StringPtr(cfg.TencentZone)}
	req.ImageId = tccommon.StringPtr(cfg.TencentImageID)
	req.InstanceType = tccommon.StringPtr(cfg.ServerType)
	req.SystemDisk = &tccvm.SystemDisk{DiskType: tccommon.StringPtr("CLOUD_PREMIUM"), DiskSize: tccommon.Int64Ptr(rootGB)}
	req.VirtualPrivateCloud = &tccvm.VirtualPrivateCloud{VpcId: tccommon.StringPtr(vpcID), SubnetId: tccommon.StringPtr(subnetID)}
	req.InternetAccessible = &tccvm.InternetAccessible{
		InternetChargeType:      tccommon.StringPtr("TRAFFIC_POSTPAID_BY_HOUR"),
		InternetMaxBandwidthOut: tccommon.Int64Ptr(tencentSSHBandwidthMbps),
		PublicIpAssigned:        tccommon.BoolPtr(true),
	}
	req.InstanceCount = tccommon.Int64Ptr(one)
	req.MinCount = tccommon.Int64Ptr(one)
	req.InstanceName = tccommon.StringPtr(name)
	req.LoginSettings = &tccvm.LoginSettings{KeyIds: tccommon.StringPtrs([]string{keyID})}
	req.SecurityGroupIds = tccommon.StringPtrs([]string{securityGroupID})
	req.EnhancedService = &tccvm.EnhancedService{
		SecurityService: &tccvm.RunSecurityServiceEnabled{Enabled: tccommon.BoolPtr(true)},
		MonitorService:  &tccvm.RunMonitorServiceEnabled{Enabled: tccommon.BoolPtr(true)},
	}
	req.ClientToken = tccommon.StringPtr(leaseID)
	req.TagSpecification = []*tccvm.TagSpecification{{ResourceType: tccommon.StringPtr("instance"), Tags: tencentCVMTags(labels)}}
	req.UserData = tccommon.StringPtr(base64.StdEncoding.EncodeToString([]byte(cloudInit(cfg, publicKey))))

	resp, err := c.cvm.RunInstancesWithContext(ctx, req)
	if err != nil {
		return Server{}, err
	}
	if resp == nil || resp.Response == nil || len(resp.Response.InstanceIdSet) == 0 || tcString(resp.Response.InstanceIdSet[0]) == "" {
		return Server{}, exit(5, "tencent returned no instances")
	}
	server := Server{
		CloudID:  tcString(resp.Response.InstanceIdSet[0]),
		Provider: "tencent",
		Name:     name,
		Status:   "pending",
		Labels:   labels,
	}
	server.ServerType.Name = cfg.ServerType
	return server, nil
}

func (c *TencentClient) GetServer(ctx context.Context, id string) (Server, error) {
	req := tccvm.NewDescribeInstancesRequest()
	req.InstanceIds = tccommon.StringPtrs([]string{id})
	resp, err := c.cvm.DescribeInstancesWithContext(ctx, req)
	if err != nil {
		return Server{}, err
	}
	if resp == nil || resp.Response == nil || len(resp.Response.InstanceSet) == 0 || resp.Response.InstanceSet[0] == nil {
		return Server{}, exit(4, "tencent instance not found: %s", id)
	}
	return tencentInstanceToServer(resp.Response.InstanceSet[0]), nil
}

func (c *TencentClient) DeleteServer(ctx context.Context, id string) error {
	req := tccvm.NewTerminateInstancesRequest()
	req.InstanceIds = tccommon.StringPtrs([]string{id})
	req.ReleaseAddress = tccommon.BoolPtr(false)
	_, err := c.cvm.TerminateInstancesWithContext(ctx, req)
	if isTencentInstanceNotFound(err) || strings.Contains(tencentErrorCode(err), "InstanceStateTerminat") {
		return nil
	}
	return err
}

func (c *TencentClient) SetLabels(ctx context.Context, id string, labels map[string]string) error {
	accountID, err := c.tencentAccountID(ctx)
	if err != nil {
		return err
	}
	resource := fmt.Sprintf("qcs::cvm:%s:uin/%s:instance/%s", c.region, accountID, id)
	tags := tencentResourceTags(labels)
	for len(tags) > 0 {
		n := len(tags)
		if n > 9 {
			n = 9
		}
		req := tctag.NewTagResourcesRequest()
		req.ResourceList = tccommon.StringPtrs([]string{resource})
		req.Tags = tags[:n]
		resp, err := c.tag.TagResourcesWithContext(ctx, req)
		if err != nil {
			return err
		}
		if resp != nil && resp.Response != nil && len(resp.Response.FailedResources) > 0 {
			fr := resp.Response.FailedResources[0]
			if fr == nil {
				return fmt.Errorf("tencent tag failed")
			}
			return fmt.Errorf("tencent tag %s failed: %s %s", tcString(fr.Resource), tcString(fr.Code), tcString(fr.Message))
		}
		tags = tags[n:]
	}
	return nil
}

func (c *TencentClient) HourlyPriceUSD(ctx context.Context, instanceType string) (float64, error) {
	fallback := tencentStaticHourlyPriceUSD(instanceType)
	cfg := c.cfg
	if cfg.TencentZone == "" || cfg.TencentImageID == "" {
		return fallback, nil
	}
	vpcID, subnetID, err := tencentNetwork(cfg)
	if err != nil {
		return fallback, nil
	}
	rootGB := cfg.TencentRootGB
	if rootGB <= 0 {
		rootGB = 400
	}
	req := tccvm.NewInquiryPriceRunInstancesRequest()
	req.InstanceChargeType = tccommon.StringPtr(tencentChargeTypePostpaid)
	req.Placement = &tccvm.Placement{Zone: tccommon.StringPtr(cfg.TencentZone)}
	req.ImageId = tccommon.StringPtr(cfg.TencentImageID)
	req.InstanceType = tccommon.StringPtr(instanceType)
	req.SystemDisk = &tccvm.SystemDisk{DiskType: tccommon.StringPtr("CLOUD_PREMIUM"), DiskSize: tccommon.Int64Ptr(rootGB)}
	req.VirtualPrivateCloud = &tccvm.VirtualPrivateCloud{VpcId: tccommon.StringPtr(vpcID), SubnetId: tccommon.StringPtr(subnetID)}
	req.InternetAccessible = &tccvm.InternetAccessible{
		InternetChargeType:      tccommon.StringPtr("TRAFFIC_POSTPAID_BY_HOUR"),
		InternetMaxBandwidthOut: tccommon.Int64Ptr(tencentSSHBandwidthMbps),
		PublicIpAssigned:        tccommon.BoolPtr(true),
	}
	req.InstanceCount = tccommon.Int64Ptr(1)
	resp, err := c.cvm.InquiryPriceRunInstancesWithContext(ctx, req)
	if err != nil {
		if fallback > 0 {
			return fallback, nil
		}
		return 0, err
	}
	price := tencentHourlyPriceFromResponse(resp)
	if price > 0 {
		return price, nil
	}
	return fallback, nil
}

func (c *TencentClient) waitForServerIP(ctx context.Context, id string) (Server, error) {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		server, err := c.GetServer(ctx, id)
		if err == nil && server.PublicNet.IPv4.IP != "" {
			return server, nil
		}
		if err != nil && !isLocalNotFoundExit(err) {
			return Server{}, err
		}
		if time.Now().After(deadline) {
			return Server{}, exit(5, "timed out waiting for Tencent instance public IP")
		}
		time.Sleep(5 * time.Second)
	}
}

func (c *TencentClient) findSSHKeyByName(ctx context.Context, name string) (*tccvm.KeyPair, error) {
	req := tccvm.NewDescribeKeyPairsRequest()
	req.Filters = []*tccvm.Filter{{Name: tccommon.StringPtr("key-name"), Values: tccommon.StringPtrs([]string{name})}}
	req.Limit = tccommon.Int64Ptr(100)
	resp, err := c.cvm.DescribeKeyPairsWithContext(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	for _, key := range resp.Response.KeyPairSet {
		if key != nil && tcString(key.KeyName) == name {
			return key, nil
		}
	}
	return nil, nil
}

func (c *TencentClient) ensureSecurityGroup(ctx context.Context, cfg Config) (string, error) {
	existing, err := c.findSecurityGroup(ctx, tencentSecurityGroupName)
	if err != nil {
		return "", err
	}
	var groupID string
	if existing != nil {
		groupID = tcString(existing.SecurityGroupId)
	} else {
		req := tcvpc.NewCreateSecurityGroupRequest()
		req.GroupName = tccommon.StringPtr(tencentSecurityGroupName)
		req.GroupDescription = tccommon.StringPtr("Crabbox ephemeral test runners")
		req.ProjectId = tccommon.StringPtr("0")
		req.Tags = tencentVPCTags(map[string]string{"crabbox": "true", "created_by": "crabbox"})
		resp, err := c.vpc.CreateSecurityGroupWithContext(ctx, req)
		if err != nil {
			return "", err
		}
		if resp == nil || resp.Response == nil || resp.Response.SecurityGroup == nil || tcString(resp.Response.SecurityGroup.SecurityGroupId) == "" {
			return "", exit(5, "tencent returned no security group id")
		}
		groupID = tcString(resp.Response.SecurityGroup.SecurityGroupId)
	}
	for _, port := range sshPortCandidates(cfg.SSHPort, cfg.SSHFallbackPorts) {
		if err := c.allowTCP(ctx, groupID, port); err != nil {
			return "", err
		}
	}
	if err := c.allowAllEgress(ctx, groupID); err != nil {
		return "", err
	}
	return groupID, nil
}

func (c *TencentClient) findSecurityGroup(ctx context.Context, name string) (*tcvpc.SecurityGroup, error) {
	req := tcvpc.NewDescribeSecurityGroupsRequest()
	req.Filters = []*tcvpc.Filter{
		{Name: tccommon.StringPtr("security-group-name"), Values: tccommon.StringPtrs([]string{name})},
	}
	req.Limit = tccommon.StringPtr("100")
	resp, err := c.vpc.DescribeSecurityGroupsWithContext(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Response == nil {
		return nil, nil
	}
	for _, group := range resp.Response.SecurityGroupSet {
		if group != nil && tcString(group.SecurityGroupName) == name {
			return group, nil
		}
	}
	return nil, nil
}

func (c *TencentClient) allowTCP(ctx context.Context, groupID, port string) error {
	if _, ok := parsePort32(port); !ok {
		return exit(2, "invalid SSH port: %s", port)
	}
	req := tcvpc.NewCreateSecurityGroupPoliciesRequest()
	req.SecurityGroupId = tccommon.StringPtr(groupID)
	req.SecurityGroupPolicySet = &tcvpc.SecurityGroupPolicySet{
		Ingress: []*tcvpc.SecurityGroupPolicy{
			{
				Protocol:          tccommon.StringPtr("TCP"),
				Port:              tccommon.StringPtr(port),
				CidrBlock:         tccommon.StringPtr("0.0.0.0/0"),
				Action:            tccommon.StringPtr("ACCEPT"),
				PolicyDescription: tccommon.StringPtr("Crabbox SSH"),
			},
		},
	}
	_, err := c.vpc.CreateSecurityGroupPoliciesWithContext(ctx, req)
	if strings.Contains(tencentErrorCode(err), "DuplicatePolicy") {
		return nil
	}
	return err
}

func (c *TencentClient) allowAllEgress(ctx context.Context, groupID string) error {
	req := tcvpc.NewCreateSecurityGroupPoliciesRequest()
	req.SecurityGroupId = tccommon.StringPtr(groupID)
	req.SecurityGroupPolicySet = &tcvpc.SecurityGroupPolicySet{
		Egress: []*tcvpc.SecurityGroupPolicy{
			{
				Protocol:          tccommon.StringPtr("ALL"),
				Port:              tccommon.StringPtr("all"),
				CidrBlock:         tccommon.StringPtr("0.0.0.0/0"),
				Action:            tccommon.StringPtr("ACCEPT"),
				PolicyDescription: tccommon.StringPtr("Crabbox egress"),
			},
		},
	}
	_, err := c.vpc.CreateSecurityGroupPoliciesWithContext(ctx, req)
	if strings.Contains(tencentErrorCode(err), "DuplicatePolicy") {
		return nil
	}
	return err
}

func (c *TencentClient) tencentAccountID(ctx context.Context) (string, error) {
	if c.accountID != "" {
		return c.accountID, nil
	}
	resp, err := c.sts.GetCallerIdentityWithContext(ctx, tcsts.NewGetCallerIdentityRequest())
	if err != nil {
		return "", err
	}
	if resp == nil || resp.Response == nil || tcString(resp.Response.AccountId) == "" {
		return "", exit(5, "tencent STS returned no account id")
	}
	c.accountID = tcString(resp.Response.AccountId)
	return c.accountID, nil
}

func tencentInstanceToServer(instance *tccvm.Instance) Server {
	labels := make(map[string]string)
	for _, tag := range instance.Tags {
		if tag == nil {
			continue
		}
		key := tcString(tag.Key)
		if key != "" {
			labels[key] = tcString(tag.Value)
		}
	}
	id := tcString(instance.InstanceId)
	name := tcString(instance.InstanceName)
	if name == "" {
		name = id
	}
	server := Server{
		CloudID:  id,
		Provider: "tencent",
		Name:     name,
		Status:   strings.ToLower(tcString(instance.InstanceState)),
		Labels:   labels,
	}
	if len(instance.PublicIpAddresses) > 0 {
		server.PublicNet.IPv4.IP = tcString(instance.PublicIpAddresses[0])
	}
	server.ServerType.Name = tcString(instance.InstanceType)
	return server
}

func tencentNetwork(cfg Config) (string, string, error) {
	vpcID := strings.TrimSpace(cfg.TencentVpcID)
	subnetID := strings.TrimSpace(cfg.TencentSubnetID)
	if vpcID == "" && subnetID == "" {
		return "DEFAULT", "DEFAULT", nil
	}
	if vpcID == "" || subnetID == "" {
		return "", "", exit(3, "tencent.vpcId and tencent.subnetId must be configured together")
	}
	return vpcID, subnetID, nil
}

func tencentInstanceTypeCandidatesForClass(class string) []string {
	switch class {
	case "standard":
		return []string{"S5.4XLARGE64", "S6.4XLARGE64"}
	case "fast":
		return []string{"S5.8XLARGE128", "S6.8XLARGE128"}
	case "large":
		return []string{"S5.12XLARGE192"}
	case "beast":
		return []string{"S5.16XLARGE256", "S6.16XLARGE256"}
	default:
		return []string{class}
	}
}

func tencentLaunchCandidates(cfg Config) []string {
	if cfg.ServerTypeExplicit {
		return []string{cfg.ServerType}
	}
	return appendUniqueStrings([]string{cfg.ServerType}, tencentInstanceTypeCandidatesForClass(cfg.Class)...)
}

func isRetryableTencentProvisioningError(err error) bool {
	code := tencentErrorCode(err)
	if strings.HasPrefix(code, "ResourcesSoldOut") || strings.HasPrefix(code, "ResourceInsufficient") {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "ResourcesSoldOut") ||
		strings.Contains(s, "ResourceInsufficient") ||
		strings.Contains(s, "InsufficientOffering") ||
		strings.Contains(s, "SoldOut")
}

func isTencentInstanceNotFound(err error) bool {
	if err == nil {
		return false
	}
	code := tencentErrorCode(err)
	return strings.Contains(code, "InvalidInstanceId.NotFound") || strings.Contains(code, "ResourceNotFound")
}

func isTencentKeyNotFound(err error) bool {
	if err == nil {
		return false
	}
	code := tencentErrorCode(err)
	return strings.Contains(code, "KeyPairNotFound") ||
		strings.Contains(code, "InvalidKeyPairId.NotFound") ||
		strings.Contains(code, "InvalidKeyPair.NotFound") ||
		strings.Contains(code, "ResourceNotFound")
}

func tencentErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var sdkErr *tcerrors.TencentCloudSDKError
	if errors.As(err, &sdkErr) {
		return sdkErr.GetCode()
	}
	return ""
}

func isLocalNotFoundExit(err error) bool {
	var exitErr ExitError
	return AsExitError(err, &exitErr) && exitErr.Code == 4
}

func tencentCVMTags(labels map[string]string) []*tccvm.Tag {
	keys := sortedLabelKeys(labels)
	tags := make([]*tccvm.Tag, 0, len(keys))
	for _, key := range keys {
		tags = append(tags, &tccvm.Tag{Key: tccommon.StringPtr(key), Value: tccommon.StringPtr(labels[key])})
	}
	return tags
}

func tencentVPCTags(labels map[string]string) []*tcvpc.Tag {
	keys := sortedLabelKeys(labels)
	tags := make([]*tcvpc.Tag, 0, len(keys))
	for _, key := range keys {
		tags = append(tags, &tcvpc.Tag{Key: tccommon.StringPtr(key), Value: tccommon.StringPtr(labels[key])})
	}
	return tags
}

func tencentResourceTags(labels map[string]string) []*tctag.Tag {
	keys := sortedLabelKeys(labels)
	tags := make([]*tctag.Tag, 0, len(keys))
	for _, key := range keys {
		tags = append(tags, &tctag.Tag{TagKey: tccommon.StringPtr(key), TagValue: tccommon.StringPtr(labels[key])})
	}
	return tags
}

func sortedLabelKeys(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func tencentProviderKeyForLease(leaseID string) string {
	return tencentKeyName("crabbox_" + leaseID)
}

func validTencentProviderKey(name string) bool {
	return strings.HasPrefix(name, "crabbox_cbx_")
}

func tencentKeyName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else if r == '-' {
			b.WriteByte('_')
		}
		if b.Len() >= 25 {
			break
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "crabbox_key"
	}
	return out
}

func tencentHourlyPriceFromResponse(resp *tccvm.InquiryPriceRunInstancesResponse) float64 {
	if resp == nil || resp.Response == nil || resp.Response.Price == nil || resp.Response.Price.InstancePrice == nil {
		return 0
	}
	price := resp.Response.Price.InstancePrice
	if price.UnitPriceDiscount != nil && *price.UnitPriceDiscount > 0 {
		return *price.UnitPriceDiscount
	}
	if price.UnitPrice != nil && *price.UnitPrice > 0 {
		return *price.UnitPrice
	}
	return 0
}

func tencentStaticHourlyPriceUSD(instanceType string) float64 {
	switch instanceType {
	case "S5.4XLARGE64", "S6.4XLARGE64":
		return 1.60
	case "S5.8XLARGE128", "S6.8XLARGE128":
		return 3.20
	case "S5.12XLARGE192":
		return 4.80
	case "S5.16XLARGE256", "S6.16XLARGE256":
		return 6.40
	default:
		return 0
	}
}

func tcString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
