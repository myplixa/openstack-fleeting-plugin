package openstackclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"

	"time"

	"github.com/caarlos0/env/v11"
	"github.com/go-viper/mapstructure/v2"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/openstack/config"
	"github.com/gophercloud/gophercloud/v2/openstack/config/clouds"
	"github.com/gophercloud/gophercloud/v2/openstack/image/v2/images"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/networks"
	"github.com/gophercloud/gophercloud/v2/openstack/utils"
	osClient "github.com/gophercloud/utils/v2/client"
)

type AuthConfig interface {
	Parse() (gophercloud.AuthOptions, gophercloud.EndpointOpts, *tls.Config, error)
	HTTPOpts() (debug bool, computeApiVersion string)
}

type CloudOpts struct {
	AllowReauth bool `envDefault:"true"`
}

type CloudConfig struct {
	ClientConfigFile  string `json:"client-config-file" env:"OS_CLIENT_CONFIG_FILE"`
	Cloud             string `json:"cloud" env:"OS_CLOUD"`
	RegionName        string `json:"region-name" env:"OS_REGION_NAME"`
	EndpointType      string `json:"endpoint-type" env:"OS_ENDPOINT_TYPE"`
	Debug             bool   `json:"debug" env:"OS_DEBUG"`
	ComputeApiVersion string `json:"compute-api-version" env:"OS_COMPUTE_API_VERSION" envDefault:"2.79"`
}

type EnvCloudConfig struct {
	CloudConfig `embed:"" yaml:",inline"`

	AuthURL                     string `json:"auth-url" env:"OS_AUTH_URL"`
	Username                    string `json:"username" env:"OS_USERNAME"`
	UserID                      string `json:"user-id" env:"OS_USER_ID"`
	Password                    string `json:"password" env:"OS_PASSWORD"`
	Passcode                    string `json:"passcode" env:"OS_PASSCODE"`
	ProjectName                 string `json:"project-name" env:"OS_PROJECT_NAME"`
	ProjectID                   string `json:"project-id" env:"OS_PROJECT_ID"`
	UserDomainName              string `json:"user-domain-name" env:"OS_USER_DOMAIN_NAME"`
	UserDomainID                string `json:"user-domain-id" env:"OS_USER_DOMAIN_ID"`
	ApplicationCredentialID     string `json:"application-credential-id" env:"OS_APPLICATION_CREDENTIAL_ID"`
	ApplicationCredentialName   string `json:"application-credential-name" env:"OS_APPLICATION_CREDENTIAL_NAME"`
	ApplicationCredentialSecret string `json:"application-credential-secret" env:"OS_APPLICATION_CREDENTIAL_SECRET"`
}

type ImageProperties struct {
	Architecture string `json:"architecture,omitempty" mapstructure:"architecture,omitempty"`
	OSType       string `json:"os_type,omitempty" mapstructure:"os_type,omitempty"`
	OSDistro     string `json:"os_distro,omitempty" mapstructure:"os_distro,omitempty"`
	OSVersion    string `json:"os_version,omitempty" mapstructure:"os_version,omitempty"`
	OSAdminUser  string `json:"os_admin_user,omitempty" mapstructure:"os_admin_user,omitempty"`
}

type Client interface {
	GetImageProperties(ctx context.Context, imageRef string) (*ImageProperties, error)
	GetImageByName(ctx context.Context, imageName string) (string, *ImageProperties, error)
	GetFlavorByName(ctx context.Context, flavorName string) (string, error)
	GetNetworkByName(ctx context.Context, networkName string) (string, error)
	ShowServerConsoleOutput(ctx context.Context, serverId string) (string, error)
	GetServer(ctx context.Context, serverId string) (*servers.Server, error)
	ListServers(ctx context.Context) ([]servers.Server, error)
	CreateServer(ctx context.Context, spec servers.CreateOptsBuilder, hintOpts servers.SchedulerHintOptsBuilder) (*servers.Server, error)
	DeleteServer(ctx context.Context, serverId string) error
	CreateVolumeFromImage(ctx context.Context, imageRef, volumeType string, sizeGB int, availabilityZone, name string) (string, error)
	DeleteVolume(ctx context.Context, volumeID string) error
}

type client struct {
	compute      *gophercloud.ServiceClient
	image        *gophercloud.ServiceClient
	network      *gophercloud.ServiceClient
	blockstorage *gophercloud.ServiceClient
}

func New(ctx context.Context, authConfig AuthConfig, cloudOpts *CloudOpts) (Client, error) {
	if cloudOpts == nil {
		cloudOpts = &CloudOpts{}
	}

	var err error
	err = env.Parse(cloudOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cloudOpts: %w", err)
	}

	var preservedComputeApiVersion string
	if ecc, ok := authConfig.(*EnvCloudConfig); ok {
		preservedComputeApiVersion = ecc.ComputeApiVersion
	}

	err = env.Parse(authConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse authConfig: %w", err)
	}

	if ecc, ok := authConfig.(*EnvCloudConfig); ok {
		restoreComputeApiVersion(ecc, preservedComputeApiVersion)
	}

	providerClient, endpointOps, err := NewProviderClient(ctx, authConfig, cloudOpts)
	if err != nil {
		return nil, err
	}

	computeClient, err := NewComputeClient(ctx, providerClient, endpointOps, authConfig)
	if err != nil {
		return nil, err
	}

	imageClient, err := openstack.NewImageV2(providerClient, endpointOps)
	if err != nil {
		return nil, err
	}

	networkClient, err := openstack.NewNetworkV2(providerClient, endpointOps)
	if err != nil {
		return nil, err
	}

	blockstorageClient, err := openstack.NewBlockStorageV3(providerClient, endpointOps)
	if err != nil {
		return nil, err
	}

	return &client{
		compute:      computeClient,
		image:        imageClient,
		network:      networkClient,
		blockstorage: blockstorageClient,
	}, nil
}

func restoreComputeApiVersion(ecc *EnvCloudConfig, preserved string) {
	if _, envSet := os.LookupEnv("OS_COMPUTE_API_VERSION"); !envSet && preserved != "" {
		ecc.ComputeApiVersion = preserved
	}
}

func (cloudConfig *CloudConfig) HTTPOpts() (debug bool, computeApiVersion string) {
	return cloudConfig.Debug, cloudConfig.ComputeApiVersion
}

func (cloudConfig *CloudConfig) Parse() (gophercloud.AuthOptions, gophercloud.EndpointOpts, *tls.Config, error) {
	parseOpts := []clouds.ParseOption{clouds.WithCloudName(cloudConfig.Cloud)}
	if cloudConfig.ClientConfigFile != "" {
		parseOpts = append(parseOpts, clouds.WithLocations(cloudConfig.ClientConfigFile))
	}

	authOptions, endpointOpts, tlsCfg, err := clouds.Parse(parseOpts...)
	if err != nil {
		return gophercloud.AuthOptions{}, gophercloud.EndpointOpts{}, nil, fmt.Errorf("failed to parse clouds.yaml: %w", err)
	}

	if cloudConfig.RegionName != "" {
		endpointOpts.Region = cloudConfig.RegionName
	}
	if cloudConfig.EndpointType != "" {
		endpointOpts.Availability = gophercloud.Availability(cloudConfig.EndpointType)
	}

	return authOptions, endpointOpts, tlsCfg, nil
}

func (envCloudConfig *EnvCloudConfig) Parse() (gophercloud.AuthOptions, gophercloud.EndpointOpts, *tls.Config, error) {
	if envCloudConfig.Cloud != "" {
		authOptions, endpointOpts, tlsCfg, err := envCloudConfig.CloudConfig.Parse()
		if err != nil {
			return gophercloud.AuthOptions{}, gophercloud.EndpointOpts{}, nil, err
		}

		if envCloudConfig.ProjectName != "" {
			authOptions.TenantName = envCloudConfig.ProjectName
			authOptions.TenantID = ""
		}
		if envCloudConfig.ProjectID != "" {
			authOptions.TenantID = envCloudConfig.ProjectID
			authOptions.TenantName = ""
		}

		return authOptions, endpointOpts, tlsCfg, nil
	}

	authOptions := gophercloud.AuthOptions{
		IdentityEndpoint:            envCloudConfig.AuthURL,
		UserID:                      envCloudConfig.UserID,
		Username:                    envCloudConfig.Username,
		Password:                    envCloudConfig.Password,
		Passcode:                    envCloudConfig.Passcode,
		TenantID:                    envCloudConfig.ProjectID,
		TenantName:                  envCloudConfig.ProjectName,
		DomainID:                    envCloudConfig.UserDomainID,
		DomainName:                  envCloudConfig.UserDomainName,
		ApplicationCredentialID:     envCloudConfig.ApplicationCredentialID,
		ApplicationCredentialName:   envCloudConfig.ApplicationCredentialName,
		ApplicationCredentialSecret: envCloudConfig.ApplicationCredentialSecret,
	}

	endpointOpts := gophercloud.EndpointOpts{
		Region:       envCloudConfig.RegionName,
		Availability: gophercloud.Availability(envCloudConfig.EndpointType),
	}

	return authOptions, endpointOpts, nil, nil
}

func NewHTTPClient(tlsCfg *tls.Config) http.Client {
	httpClient := http.Client{
		Transport: http.DefaultTransport.(*http.Transport).Clone(),
	}

	if tlsCfg != nil {
		tr := httpClient.Transport.(*http.Transport)
		tr.TLSClientConfig = tlsCfg
	}

	httpClient.Transport = &osClient.RoundTripper{
		Rt: httpClient.Transport,
	}
	return httpClient
}

func NewProviderClient(ctx context.Context, authConfig AuthConfig, cloudOpts *CloudOpts) (*gophercloud.ProviderClient, gophercloud.EndpointOpts, error) {
	authOptions, endpointOpts, tlsCfg, err := authConfig.Parse()
	if err != nil {
		return nil, gophercloud.EndpointOpts{}, err
	}

	httpClient := NewHTTPClient(tlsCfg)
	authOptions.AllowReauth = cloudOpts.AllowReauth

	providerClient, err := config.NewProviderClient(ctx, authOptions, config.WithHTTPClient(httpClient))
	if err != nil {
		return nil, gophercloud.EndpointOpts{}, err
	}

	return providerClient, endpointOpts, nil
}

func NewComputeClient(ctx context.Context, providerClient *gophercloud.ProviderClient, endpointOps gophercloud.EndpointOpts, authConfig AuthConfig) (*gophercloud.ServiceClient, error) {
	_, computeApiVersion := authConfig.HTTPOpts()

	computeClient, err := openstack.NewComputeV2(providerClient, endpointOps)
	if err != nil {
		return &gophercloud.ServiceClient{}, err
	}

	_computeClient, err := utils.RequireMicroversion(ctx, *computeClient, computeApiVersion)
	if err != nil {
		return &gophercloud.ServiceClient{}, err
	}

	return &_computeClient, err
}

func (c *client) GetImageProperties(ctx context.Context, imageRef string) (*ImageProperties, error) {
	image, err := images.Get(ctx, c.image, imageRef).Extract()
	if err != nil {
		return nil, fmt.Errorf("failed to get image %s: %w", imageRef, err)
	}

	out := new(ImageProperties)
	err = mapstructure.Decode(image.Properties, out)
	if err != nil {
		return nil, fmt.Errorf("failed to parse properties: %w", err)
	}

	return out, nil
}

func (c *client) GetImageByName(ctx context.Context, imageName string) (string, *ImageProperties, error) {
	page, err := images.List(c.image, images.ListOpts{Name: imageName}).AllPages(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("failed to list images: %w", err)
	}

	imgs, err := images.ExtractImages(page)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse images: %w", err)
	}

	if len(imgs) == 0 {
		err = gophercloud.ErrResourceNotFound{Name: imageName, ResourceType: "image"}
		return "", nil, err
	} else if len(imgs) > 1 {
		err = gophercloud.ErrMultipleResourcesFound{Name: imageName, Count: len(imgs), ResourceType: "image"}
		return "", nil, err
	}

	out := new(ImageProperties)
	err = mapstructure.Decode(imgs[0].Properties, out)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse properties: %w", err)
	}

	return imgs[0].ID, out, nil
}

func (c *client) GetFlavorByName(ctx context.Context, flavorName string) (string, error) {
	page, err := flavors.ListDetail(c.compute, flavors.ListOpts{}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list flavors: %w", err)
	}

	all, err := flavors.ExtractFlavors(page)
	if err != nil {
		return "", fmt.Errorf("failed to parse flavors: %w", err)
	}

	var matches []flavors.Flavor
	for _, f := range all {
		if f.Name == flavorName {
			matches = append(matches, f)
		}
	}

	if len(matches) == 0 {
		return "", gophercloud.ErrResourceNotFound{Name: flavorName, ResourceType: "flavor"}
	} else if len(matches) > 1 {
		return "", gophercloud.ErrMultipleResourcesFound{Name: flavorName, Count: len(matches), ResourceType: "flavor"}
	}

	return matches[0].ID, nil
}

func (c *client) GetNetworkByName(ctx context.Context, networkName string) (string, error) {
	page, err := networks.List(c.network, networks.ListOpts{Name: networkName}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list networks: %w", err)
	}

	all, err := networks.ExtractNetworks(page)
	if err != nil {
		return "", fmt.Errorf("failed to parse networks: %w", err)
	}

	if len(all) == 0 {
		return "", gophercloud.ErrResourceNotFound{Name: networkName, ResourceType: "network"}
	} else if len(all) > 1 {
		return "", gophercloud.ErrMultipleResourcesFound{Name: networkName, Count: len(all), ResourceType: "network"}
	}

	return all[0].ID, nil
}

func (c *client) ShowServerConsoleOutput(ctx context.Context, serverId string) (string, error) {
	return servers.ShowConsoleOutput(ctx, c.compute, serverId, servers.ShowConsoleOutputOpts{
		Length: 100,
	}).Extract()
}

func (c *client) GetServer(ctx context.Context, serverId string) (*servers.Server, error) {
	return servers.Get(ctx, c.compute, serverId).Extract()
}

func (c *client) ListServers(ctx context.Context) ([]servers.Server, error) {
	page, err := servers.List(c.compute, nil).AllPages(ctx)
	if err != nil {
		return nil, fmt.Errorf("server listing error: %w", err)
	}

	allServers, err := servers.ExtractServers(page)
	if err != nil {
		return nil, fmt.Errorf("server listing extract error: %w", err)
	}

	return allServers, nil
}

func (c *client) CreateServer(ctx context.Context, spec servers.CreateOptsBuilder, hintOpts servers.SchedulerHintOptsBuilder) (*servers.Server, error) {
	return servers.Create(ctx, c.compute, spec, hintOpts).Extract()
}

func (c *client) DeleteServer(ctx context.Context, serverId string) error {
	return servers.Delete(ctx, c.compute, serverId).ExtractErr()
}

// CreateVolumeFromImage creates a Cinder volume from the given image, pinned
// to availabilityZone, and blocks until it reaches "available". Used by the
// volume_pre_create path to avoid the AZ mismatch that can occur when Nova
// creates the boot volume implicitly during server create (see
// https://docs.openstack.org/nova/latest/admin/availability-zones.html,
// [cinder] cross_az_attach). The returned volume ID is valid even when err
// is non-nil, so the caller can clean it up.
func (c *client) CreateVolumeFromImage(ctx context.Context, imageRef, volumeType string, sizeGB int, availabilityZone, name string) (string, error) {
	vol, err := volumes.Create(ctx, c.blockstorage, volumes.CreateOpts{
		Size:             sizeGB,
		VolumeType:       volumeType,
		ImageID:          imageRef,
		AvailabilityZone: availabilityZone,
		Name:             name,
	}, nil).Extract()
	if err != nil {
		return "", fmt.Errorf("failed to create volume: %w", err)
	}

	for {
		current, err := volumes.Get(ctx, c.blockstorage, vol.ID).Extract()
		if err != nil {
			return vol.ID, fmt.Errorf("failed to poll volume %s: %w", vol.ID, err)
		}

		switch current.Status {
		case "available":
			return vol.ID, nil
		case "error", "error_deleting":
			return vol.ID, fmt.Errorf("volume %s entered status %q while creating from image", vol.ID, current.Status)
		}

		select {
		case <-ctx.Done():
			return vol.ID, fmt.Errorf("timed out waiting for volume %s to become available: %w", vol.ID, ctx.Err())
		case <-time.After(3 * time.Second):
		}
	}
}

func (c *client) DeleteVolume(ctx context.Context, volumeID string) error {
	return volumes.Delete(ctx, c.blockstorage, volumeID, nil).ExtractErr()
}
