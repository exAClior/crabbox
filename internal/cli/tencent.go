package cli

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	tencentHAIEndpoint          = "https://hai.tencentcloudapi.com"
	tencentHAIHost              = "hai.tencentcloudapi.com"
	tencentHAIService           = "hai"
	tencentHAIVersion           = "2023-08-12"
	tencentHAIDefaultRegion     = "ap-singapore"
	tencentHAIDefaultAppID      = "app-khcjsi2t"
	tencentHAIDefaultDiskGB     = int64(80)
	tencentHAIDefaultDiskType   = "CLOUD_PREMIUM"
	tencentHAIInstanceNamePrefx = "crabbox-"
)

func init() {
	RegisterProvider(tencentProvider{})
}

type tencentProvider struct{}

func (tencentProvider) Name() string      { return "tencent" }
func (tencentProvider) Aliases() []string { return []string{"tencent-cloud", "tencent-hai"} }
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
	SecretID     *string
	SecretKey    *string
	Region       *string
	Application  *string
	BundleType   *string
	SystemDiskGB *int64
}

func (tencentProvider) RegisterFlags(fs *flag.FlagSet, defaults Config) any {
	return tencentFlagValues{
		SecretID:     fs.String("tencent-secret-id", defaults.TencentSecretID, "Tencent Cloud CAM SecretId"),
		SecretKey:    fs.String("tencent-secret-key", defaults.TencentSecretKey, "Tencent Cloud CAM SecretKey"),
		Region:       fs.String("tencent-region", defaults.TencentRegion, "Tencent Cloud HAI region"),
		Application:  fs.String("tencent-application-id", defaults.TencentApplicationID, "Tencent Cloud HAI application ID"),
		BundleType:   fs.String("tencent-bundle-type", defaults.TencentBundleType, "Tencent Cloud HAI bundle type"),
		SystemDiskGB: fs.Int64("tencent-system-disk-gb", defaults.TencentSystemDiskGB, "Tencent Cloud HAI system disk size in GB"),
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
	if flagWasSet(fs, "tencent-application-id") {
		cfg.TencentApplicationID = *v.Application
	}
	if flagWasSet(fs, "tencent-bundle-type") {
		cfg.TencentBundleType = *v.BundleType
		cfg.ServerType = *v.BundleType
	}
	if flagWasSet(fs, "tencent-system-disk-gb") {
		cfg.TencentSystemDiskGB = *v.SystemDiskGB
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
	if err := validateTencentHAISSHConfig(cfg); err != nil {
		return LeaseTarget{}, err
	}
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
	cfg.ProviderKey = ""
	fmt.Fprintf(b.rt.Stderr, "provisioning provider=tencent-hai lease=%s slug=%s class=%s bundle=%s region=%s application=%s auth=baked-image ssh_user=%s ssh_key=%s keep=%v\n", leaseID, slug, cfg.Class, cfg.ServerType, cfg.TencentRegion, cfg.TencentApplicationID, cfg.SSHUser, cfg.SSHKey, req.Keep)
	server, cfg, err := client.CreateServerWithFallback(ctx, cfg, leaseID, slug, req.Keep, func(format string, args ...any) {
		fmt.Fprintf(b.rt.Stderr, format, args...)
	})
	if err != nil {
		return LeaseTarget{}, err
	}
	fmt.Fprintf(b.rt.Stderr, "provisioned lease=%s server=%s bundle=%s\n", leaseID, server.DisplayID(), cfg.ServerType)
	server, err = client.waitForServerIP(ctx, server.CloudID)
	if err != nil {
		return LeaseTarget{}, err
	}
	target := sshTargetFromConfig(cfg, server.PublicNet.IPv4.IP)
	if err := waitForSSH(ctx, &target, b.rt.Stderr); err != nil {
		if !req.Keep {
			_ = client.DeleteServer(context.Background(), server.CloudID)
		}
		return LeaseTarget{}, fmt.Errorf("HAI instance was created, but SSH never became usable. HAI does not expose CVM cloud-init or SSH key injection; the application image in region %s must already contain the public key for user %s and the local private key must be %s: %w", cfg.TencentRegion, target.User, target.Key, err)
	}
	server.Labels["state"] = "ready"
	return LeaseTarget{Server: server, SSH: target, LeaseID: leaseID}, nil
}

func (b *tencentBackend) Resolve(ctx context.Context, req ResolveRequest) (LeaseTarget, error) {
	client, err := newTencentClient(b.cfg)
	if err != nil {
		return LeaseTarget{}, err
	}
	var server Server
	var leaseID string
	if strings.HasPrefix(req.ID, "hai-") {
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
		} else if server.CloudID == "" {
			return LeaseTarget{}, exit(4, "lease/server not found: %s", req.ID)
		} else if leaseID == "" {
			leaseID = server.CloudID
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
		return client.DeleteServer(ctx, req.Lease.Server.CloudID)
	}
	return nil
}

func (b *tencentBackend) Touch(ctx context.Context, req TouchRequest) (Server, error) {
	server := req.Lease.Server
	if server.Labels == nil {
		server.Labels = map[string]string{}
	}
	server.Labels = touchDirectLeaseLabels(server.Labels, b.cfg, req.State, time.Now().UTC())
	return server, nil
}

type TencentClient struct {
	secretID  string
	secretKey string
	region    string
	http      *http.Client
	cfg       Config
}

func newTencentClient(cfg Config) (*TencentClient, error) {
	secretID := strings.TrimSpace(cfg.TencentSecretID)
	if secretID == "" {
		secretID = getenv("CRABBOX_TENCENT_SECRET_ID", getenv("TENCENT_SECRET_ID", getenv("TENCENTCLOUD_SECRET_ID", "")))
	}
	secretKey := strings.TrimSpace(cfg.TencentSecretKey)
	if secretKey == "" {
		secretKey = getenv("CRABBOX_TENCENT_SECRET_KEY", getenv("TENCENT_SECRET_KEY", getenv("TENCENTCLOUD_SECRET_KEY", "")))
	}
	region := strings.TrimSpace(cfg.TencentRegion)
	if region == "" {
		region = getenv("CRABBOX_TENCENT_REGION", "")
	}
	if secretID == "" || secretKey == "" {
		return nil, exit(3, "CRABBOX_TENCENT_SECRET_ID/CRABBOX_TENCENT_SECRET_KEY or TENCENT_SECRET_ID/TENCENT_SECRET_KEY are required")
	}
	if region == "" {
		return nil, exit(3, "tencent.region or CRABBOX_TENCENT_REGION is required")
	}
	cfg.TencentSecretID = secretID
	cfg.TencentSecretKey = secretKey
	cfg.TencentRegion = region
	return &TencentClient{secretID: secretID, secretKey: secretKey, region: region, http: &http.Client{Timeout: 60 * time.Second}, cfg: cfg}, nil
}

func (c *TencentClient) ListCrabboxServers(ctx context.Context) ([]Server, error) {
	servers := make([]Server, 0)
	offset := 0
	limit := 100
	for {
		var resp haiDescribeInstancesResponse
		if err := c.do(ctx, "DescribeInstances", map[string]any{"Offset": offset, "Limit": limit}, &resp); err != nil {
			return nil, err
		}
		for _, instance := range resp.Response.InstanceSet {
			server := tencentHAIInstanceToServer(instance)
			if strings.HasPrefix(server.Name, tencentHAIInstanceNamePrefx) {
				servers = append(servers, server)
			}
		}
		if len(resp.Response.InstanceSet) == 0 || len(resp.Response.InstanceSet) < limit || offset+len(resp.Response.InstanceSet) >= resp.Response.TotalCount {
			return servers, nil
		}
		offset += len(resp.Response.InstanceSet)
	}
}

func (c *TencentClient) CreateServerWithFallback(ctx context.Context, cfg Config, leaseID, slug string, keep bool, logf func(string, ...any)) (Server, Config, error) {
	candidates := tencentLaunchCandidates(cfg)
	var errs []error
	for i, bundleType := range candidates {
		next := cfg
		next.ServerType = bundleType
		if i > 0 && logf != nil {
			logf("fallback provisioning bundle=%s after capacity rejection\n", bundleType)
		}
		server, err := c.createServer(ctx, next, leaseID, slug, keep)
		if err == nil {
			return server, next, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", bundleType, err))
		if !isRetryableTencentProvisioningError(err) {
			return Server{}, next, joinErrors(errs)
		}
	}
	if cfg.ServerTypeExplicit {
		return Server{}, cfg, fmt.Errorf("requested exact Tencent HAI bundle %s failed; remove --type to allow class fallback: %w", cfg.ServerType, joinErrors(errs))
	}
	return Server{}, cfg, joinErrors(errs)
}

func (c *TencentClient) createServer(ctx context.Context, cfg Config, leaseID, slug string, keep bool) (Server, error) {
	applicationID := strings.TrimSpace(cfg.TencentApplicationID)
	if applicationID == "" {
		applicationID = tencentHAIDefaultAppID
	}
	bundleType := strings.TrimSpace(cfg.ServerType)
	if bundleType == "" {
		bundleType = tencentBundleTypeCandidatesForClass(cfg.Class)[0]
	}
	diskGB := cfg.TencentSystemDiskGB
	if diskGB <= 0 {
		diskGB = tencentHAIDefaultDiskGB
	}
	name := leaseProviderName(leaseID, slug)
	now := time.Now().UTC()
	labels := directLeaseLabels(cfg, leaseID, slug, "tencent", "hai", keep, now)
	labels["application_id"] = applicationID
	req := haiRunInstancesRequest{
		ApplicationID: applicationID,
		BundleType:    bundleType,
		SystemDisk:    &haiSystemDisk{DiskType: tencentHAIDefaultDiskType, DiskSize: diskGB},
		InstanceCount: 1,
		InstanceName:  name,
		ClientToken:   leaseID,
	}
	var resp haiRunInstancesResponse
	if err := c.do(ctx, "RunInstances", req, &resp); err != nil {
		return Server{}, err
	}
	if len(resp.Response.InstanceIDSet) == 0 || resp.Response.InstanceIDSet[0] == "" {
		return Server{}, exit(5, "tencent HAI returned no instances")
	}
	server := Server{CloudID: resp.Response.InstanceIDSet[0], Provider: "tencent", Name: name, Status: "pending", Labels: labels}
	server.ServerType.Name = bundleType
	return server, nil
}

func (c *TencentClient) GetServer(ctx context.Context, id string) (Server, error) {
	var resp haiDescribeInstancesResponse
	if err := c.do(ctx, "DescribeInstances", map[string]any{"InstanceIds": []string{id}, "Offset": 0, "Limit": 1}, &resp); err != nil {
		return Server{}, err
	}
	if len(resp.Response.InstanceSet) == 0 {
		return Server{}, exit(4, "tencent HAI instance not found: %s", id)
	}
	return tencentHAIInstanceToServer(resp.Response.InstanceSet[0]), nil
}

func (c *TencentClient) DeleteServer(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	err := c.do(ctx, "TerminateInstances", map[string]any{"InstanceIds": []string{id}}, &haiRequestIDResponse{})
	if isTencentInstanceNotFound(err) {
		return nil
	}
	return err
}

func (c *TencentClient) SetLabels(ctx context.Context, id string, labels map[string]string) error {
	return nil
}
func (c *TencentClient) DeleteSSHKey(ctx context.Context, name string) error { return nil }

func validateTencentHAISSHConfig(cfg Config) error {
	user := strings.TrimSpace(cfg.SSHUser)
	if user == "" {
		return exit(3, "Tencent HAI requires ssh.user or CRABBOX_SSH_USER matching the custom application image user")
	}
	key := strings.TrimSpace(cfg.SSHKey)
	if key == "" {
		return exit(3, "Tencent HAI requires ssh.key or CRABBOX_SSH_KEY pointing to the private key for the public key baked into the custom application image")
	}
	if strings.HasSuffix(key, ".pub") {
		return exit(3, "Tencent HAI CRABBOX_SSH_KEY must point to the private key, not the public key %s", key)
	}
	info, err := os.Stat(key)
	if err != nil {
		return exit(3, "Tencent HAI SSH key %s is not readable; set CRABBOX_SSH_KEY to the private key matching the custom application image: %v", key, err)
	}
	if info.IsDir() {
		return exit(3, "Tencent HAI SSH key %s is a directory; set CRABBOX_SSH_KEY to a private key file", key)
	}
	return nil
}

func (c *TencentClient) HourlyPriceCNY(ctx context.Context, bundleType string) (float64, error) {
	diskGB := c.cfg.TencentSystemDiskGB
	if diskGB <= 0 {
		diskGB = tencentHAIDefaultDiskGB
	}
	applicationID := strings.TrimSpace(c.cfg.TencentApplicationID)
	if applicationID == "" {
		applicationID = tencentHAIDefaultAppID
	}
	var resp haiInquirePriceRunInstancesResponse
	err := c.do(ctx, "InquirePriceRunInstances", haiRunInstancesRequest{
		ApplicationID: applicationID,
		BundleType:    bundleType,
		SystemDisk:    &haiSystemDisk{DiskType: tencentHAIDefaultDiskType, DiskSize: diskGB},
		InstanceCount: 1,
	}, &resp)
	if err != nil {
		fallback := tencentStaticHourlyPriceCNY(bundleType)
		if fallback > 0 {
			return fallback, nil
		}
		return 0, err
	}
	price := resp.Response.Price.hourlyUnitPriceCNY()
	if price > 0 {
		return price, nil
	}
	fallback := tencentStaticHourlyPriceCNY(bundleType)
	if fallback > 0 {
		return fallback, nil
	}
	return 0, fmt.Errorf("tencent HAI price response did not include hourly price for bundle %s", bundleType)
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
			return Server{}, exit(5, "timed out waiting for Tencent HAI instance public IP")
		}
		time.Sleep(5 * time.Second)
	}
}

func (c *TencentClient) do(ctx context.Context, action string, body any, out any) error {
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			return err
		}
	} else {
		payload.WriteString("{}")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tencentHAIEndpoint, bytes.NewReader(payload.Bytes()))
	if err != nil {
		return err
	}
	tencentSignRequest(req, c.secretID, c.secretKey, c.region, action, payload.Bytes(), time.Now().UTC())
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var envelope haiErrorEnvelope
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Response.Error != nil {
		return tencentAPIError{Code: envelope.Response.Error.Code, Message: envelope.Response.Error.Message}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tencent HAI %s failed: HTTP %d: %s", action, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode Tencent HAI %s response: %w", action, err)
		}
	}
	return nil
}

func tencentSignRequest(req *http.Request, secretID, secretKey, region, action string, payload []byte, now time.Time) {
	timestamp := now.Unix()
	date := now.Format("2006-01-02")
	hashedPayload := tencentSHA256Hex(payload)
	canonicalHeaders := "content-type:application/json; charset=utf-8\nhost:" + tencentHAIHost + "\nx-tc-action:" + strings.ToLower(action) + "\n"
	signedHeaders := "content-type;host;x-tc-action"
	canonicalRequest := strings.Join([]string{http.MethodPost, "/", "", canonicalHeaders, signedHeaders, hashedPayload}, "\n")
	credentialScope := date + "/" + tencentHAIService + "/tc3_request"
	stringToSign := strings.Join([]string{"TC3-HMAC-SHA256", fmt.Sprint(timestamp), credentialScope, tencentSHA256Hex([]byte(canonicalRequest))}, "\n")
	secretDate := hmacSHA256([]byte("TC3"+secretKey), date)
	secretService := hmacSHA256(secretDate, tencentHAIService)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))
	authorization := "TC3-HMAC-SHA256 Credential=" + secretID + "/" + credentialScope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", tencentHAIHost)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Region", region)
	req.Header.Set("X-TC-Timestamp", fmt.Sprint(timestamp))
	req.Header.Set("X-TC-Version", tencentHAIVersion)
}

func tencentHAIInstanceToServer(instance haiInstance) Server {
	labels := tencentLabelsFromHAIName(instance.InstanceName)
	labels["provider"] = "tencent"
	labels["market"] = "hai"
	id := instance.InstanceID
	name := instance.InstanceName
	if name == "" {
		name = id
	}
	server := Server{CloudID: id, Provider: "tencent", Name: name, Status: strings.ToLower(instance.InstanceState), Labels: labels}
	if len(instance.PublicIPAddresses) > 0 {
		server.PublicNet.IPv4.IP = instance.PublicIPAddresses[0]
	}
	if len(instance.PrivateIPAddresses) > 0 {
		server.PrivateNet.IPv4.IP = instance.PrivateIPAddresses[0]
	}
	server.ServerType.Name = blank(instance.BundleName, labels["server_type"])
	return server
}

func tencentLabelsFromHAIName(name string) map[string]string {
	labels := map[string]string{"crabbox": "true", "created_by": "crabbox"}
	if strings.HasPrefix(name, tencentHAIInstanceNamePrefx) {
		trimmed := strings.TrimPrefix(name, tencentHAIInstanceNamePrefx)
		if head, _, ok := strings.Cut(trimmed, "-"); ok {
			labels["slug"] = normalizeLeaseSlug(head)
		}
	}
	return labels
}

func tencentBundleTypeCandidatesForClass(class string) []string {
	switch class {
	case "standard":
		return []string{"XL"}
	case "fast":
		return []string{"24GB_A"}
	case "large":
		return []string{"3XL"}
	case "beast":
		return []string{"4XL"}
	default:
		return []string{class}
	}
}

func tencentLaunchCandidates(cfg Config) []string {
	if cfg.ServerTypeExplicit {
		return []string{cfg.ServerType}
	}
	if cfg.TencentBundleType != "" {
		return appendUniqueStrings([]string{cfg.TencentBundleType}, tencentBundleTypeCandidatesForClass(cfg.Class)...)
	}
	return appendUniqueStrings([]string{cfg.ServerType}, tencentBundleTypeCandidatesForClass(cfg.Class)...)
}

func isRetryableTencentProvisioningError(err error) bool {
	code := tencentErrorCode(err)
	if strings.HasPrefix(code, "ResourcesSoldOut") || strings.HasPrefix(code, "ResourceInsufficient") {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "ResourcesSoldOut") || strings.Contains(s, "ResourceInsufficient") || strings.Contains(s, "InsufficientOffering") || strings.Contains(s, "SoldOut")
}

func isTencentInstanceNotFound(err error) bool {
	if err == nil {
		return false
	}
	code := tencentErrorCode(err)
	return strings.Contains(code, "InvalidInstanceId.NotFound") || strings.Contains(code, "ResourceNotFound") || strings.Contains(code, "InvalidInstanceId")
}

func tencentErrorCode(err error) string {
	var apiErr tencentAPIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

func isLocalNotFoundExit(err error) bool {
	var exitErr ExitError
	return AsExitError(err, &exitErr) && exitErr.Code == 4
}

// tencentStaticHourlyPriceCNY returns public "starting from" pay-as-you-go
// HAI prices in RMB/CNY per hour. Exact billing still comes from
// InquirePriceRunInstances because region, application, discounts, and disk
// choices can change the result. 4XL has no public static price, so don't guess.
func tencentStaticHourlyPriceCNY(bundleType string) float64 {
	switch bundleType {
	case "XL":
		return 1.20
	case "24GB_A", "3XL":
		return 3.60
	default:
		return 0
	}
}

func validTencentProviderKey(name string) bool { return false }

func tencentSHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

type tencentAPIError struct{ Code, Message string }

func (e tencentAPIError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

type haiSystemDisk struct {
	DiskType string `json:"DiskType,omitempty"`
	DiskSize int64  `json:"DiskSize,omitempty"`
}

type haiRunInstancesRequest struct {
	ApplicationID string         `json:"ApplicationId,omitempty"`
	BundleType    string         `json:"BundleType,omitempty"`
	SystemDisk    *haiSystemDisk `json:"SystemDisk,omitempty"`
	InstanceCount int            `json:"InstanceCount,omitempty"`
	InstanceName  string         `json:"InstanceName,omitempty"`
	ClientToken   string         `json:"ClientToken,omitempty"`
}

type haiRunInstancesResponse struct {
	Response struct {
		InstanceIDSet []string `json:"InstanceIdSet"`
		RequestID     string   `json:"RequestId"`
	} `json:"Response"`
}

type haiDescribeInstancesResponse struct {
	Response struct {
		TotalCount  int           `json:"TotalCount"`
		InstanceSet []haiInstance `json:"InstanceSet"`
		RequestID   string        `json:"RequestId"`
	} `json:"Response"`
}

type haiInstance struct {
	InstanceID         string   `json:"InstanceId"`
	InstanceName       string   `json:"InstanceName"`
	InstanceState      string   `json:"InstanceState"`
	ApplicationName    string   `json:"ApplicationName"`
	BundleName         string   `json:"BundleName"`
	PrivateIPAddresses []string `json:"PrivateIpAddresses"`
	PublicIPAddresses  []string `json:"PublicIpAddresses"`
}

type haiInquirePriceRunInstancesResponse struct {
	Response struct {
		Price     haiPrice `json:"Price"`
		RequestID string   `json:"RequestId"`
	} `json:"Response"`
}

type haiPrice struct {
	InstancePrice  haiItemPrice `json:"InstancePrice"`
	CloudDiskPrice haiItemPrice `json:"CloudDiskPrice"`
}

func (p haiPrice) hourlyUnitPriceCNY() float64 {
	instancePrice := p.InstancePrice.hourlyUnitPriceCNY()
	if instancePrice <= 0 {
		return 0
	}
	return instancePrice + p.CloudDiskPrice.hourlyUnitPriceCNY()
}

type haiItemPrice struct {
	UnitPrice         float64 `json:"UnitPrice"`
	DiscountUnitPrice float64 `json:"DiscountUnitPrice"`
}

func (p haiItemPrice) hourlyUnitPriceCNY() float64 {
	if p.DiscountUnitPrice > 0 {
		return p.DiscountUnitPrice
	}
	return p.UnitPrice
}

type haiRequestIDResponse struct {
	Response struct {
		RequestID string `json:"RequestId"`
	} `json:"Response"`
}

type haiErrorEnvelope struct {
	Response struct {
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
		RequestID string `json:"RequestId"`
	} `json:"Response"`
}

func tcString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
