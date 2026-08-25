package fpoc

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/hashicorp/go-hclog"
	"github.com/jinzhu/copier"
	"github.com/myplixa/openstack-fleeting-plugin/internal/openstackclient"

	"gitlab.com/gitlab-org/fleeting/fleeting/provider"
)

const MetadataKey = "fleeting-cluster"

const defaultBootTime = 2 * time.Minute

var _ provider.InstanceGroup = (*InstanceGroup)(nil)

type InstanceGroup struct {
	Cloud            string        `json:"cloud"`
	CloudsConfig     string        `json:"clouds_config"`
	Name             string        `json:"name"`
	NovaMicroversion string        `json:"nova_microversion"`
	ServerSpec       ExtCreateOpts `json:"server_spec"`
	UseIgnition      bool          `json:"use_ignition"`
	BootTimeS        string        `json:"boot_time"`
	BootTime         time.Duration

	VolumeType      string `json:"volume_type,omitempty"`
	VolumeSize      int    `json:"volume_size,omitempty"`
	VolumePreCreate bool   `json:"volume_pre_create,omitempty"`

	CloudInitVars map[string]string `json:"cloud_init_vars,omitempty"`

	client    openstackclient.Client
	settings  provider.Settings
	log       hclog.Logger
	imgProps  atomic.Pointer[openstackclient.ImageProperties]
	sshPubKey string
}

func (g *InstanceGroup) Init(ctx context.Context, log hclog.Logger, settings provider.Settings) (provider.ProviderInfo, error) {
	g.log = log.With("name", g.Name, "cloud", g.Cloud)
	g.log.Debug("Initializing fleeting-plugin-openstack")

	var err error
	g.client, err = openstackclient.New(ctx, &openstackclient.EnvCloudConfig{
		CloudConfig: openstackclient.CloudConfig{
			ClientConfigFile:  g.CloudsConfig,
			Cloud:             g.Cloud,
			ComputeApiVersion: g.NovaMicroversion,
		},
	}, nil)

	if err != nil {
		return provider.ProviderInfo{}, err
	}

	if g.ServerSpec.Name == "" {
		g.ServerSpec.Name = g.Name
	}

	if err := resolveUserDataFile(&g.ServerSpec); err != nil {
		return provider.ProviderInfo{}, err
	}

	if err := renderUserDataTemplate(&g.ServerSpec, g.Name, g.CloudInitVars); err != nil {
		return provider.ProviderInfo{}, err
	}

	_, err = g.ServerSpec.ToServerCreateMap()
	if err != nil {
		return provider.ProviderInfo{}, fmt.Errorf("failed to check server_spec: %w", err)
	}

	if g.ServerSpec.ImageRef != "" {
		imgProps, err := g.client.GetImageProperties(ctx, g.ServerSpec.ImageRef)
		if err != nil {
			return provider.ProviderInfo{}, err
		}

		g.imgProps.Store(imgProps)
	}

	if !settings.UseStaticCredentials {
		err = g.initSSHKey(ctx, log, &settings)
		if err != nil {
			return provider.ProviderInfo{}, err
		}
	}

	if g.BootTimeS != "" {
		g.BootTime, err = time.ParseDuration(g.BootTimeS)
		if err != nil {
			return provider.ProviderInfo{}, fmt.Errorf("failed to parse boot_time: %w", err)
		}
	} else {
		g.BootTime = defaultBootTime
	}

	g.settings = settings
	if _, err := g.getInstances(ctx); err != nil {
		return provider.ProviderInfo{}, err
	}

	return provider.ProviderInfo{
		ID:        path.Join("openstack", g.Cloud, g.Name),
		MaxSize:   1000,
		Version:   Version,
		BuildInfo: BuildInfo(),
	}, nil
}

func (g *InstanceGroup) Update(ctx context.Context, update func(instance string, state provider.State)) error {

	instances, err := g.getInstances(ctx)
	if err != nil {
		return err
	}

	var reterr error
	for _, srv := range instances {
		state := provider.StateCreating
		lg := g.log.With("server_id", srv.ID, "created", srv.Created, "status", srv.Status)

		switch srv.Status {
		case "BUILD", "MIGRATING", "PAUSED", "REBUILD":

		case "DELETED", "SHUTOFF", "UNKNOWN":
			state = provider.StateDeleting

		case "ERROR":
			lg.Warn("Instance is in ERROR state. Marking as a timeout.")
			state = provider.StateTimeout

		case "ACTIVE":
			if srv.Created.Add(g.BootTime).Before(time.Now()) {
				state = provider.StateRunning
			} else {
				log, err := g.client.ShowServerConsoleOutput(ctx, srv.ID)
				if err != nil {
					reterr = errors.Join(reterr, err)
					continue
				}

				if !g.UseIgnition && IsCloudInitFinished(log) {
					lg.Info("Instance cloud-init finished")
					state = provider.StateRunning
				} else if g.UseIgnition && IsIgnitionFinished(log) {
					lg.Info("Instance ignition finished")
					state = provider.StateRunning
				} else {
					lg.Debug("Instance boot time not passed and cloud-init/ignition not finished", "boot_time", g.BootTime)
				}
			}
		}

		update(srv.ID, state)
	}

	return reterr
}

func (g *InstanceGroup) Increase(ctx context.Context, delta int) (succeeded int, err error) {
	for idx := 0; idx < delta; idx++ {
		id, err2 := g.createInstance(ctx)
		if err2 != nil {
			g.log.Error("Failed to create instance", "err", err)
			err = errors.Join(err, err2)
		} else {
			g.log.Info("Instance creation request successful", "id", id)
			succeeded++
		}
	}

	g.log.Info("Increase", "delta", delta, "succeeded", succeeded)

	return
}

func (g *InstanceGroup) Decrease(ctx context.Context, instances []string) (succeeded []string, err error) {
	if len(instances) == 0 {
		return nil, nil
	}

	succeeded = make([]string, 0, len(instances))
	for _, id := range instances {
		err2 := g.client.DeleteServer(ctx, id)
		if err2 != nil {
			g.log.Error("Failed to delete instance", "err", err2, "id", id)
			err = errors.Join(err, err2)
		} else {
			g.log.Info("Instance deletion request successful", "id", id)
			succeeded = append(succeeded, id)
		}
	}

	g.log.Info("Decrease", "instances", instances)

	return
}

func (g *InstanceGroup) getInstances(ctx context.Context) ([]servers.Server, error) {
	allServers, err := g.client.ListServers(ctx)
	if err != nil {
		return nil, err
	}

	filteredServers := make([]servers.Server, 0, len(allServers))
	for _, srv := range allServers {
		cluster, ok := srv.Metadata[MetadataKey]
		if !ok || cluster != g.Name {
			continue
		}

		filteredServers = append(filteredServers, srv)
	}

	return filteredServers, nil
}

func (g *InstanceGroup) createInstance(ctx context.Context) (string, error) {
	spec := new(ExtCreateOpts)
	err := copier.Copy(spec, &g.ServerSpec)
	if err != nil {
		return "", err
	}

	spec.Min = 0
	spec.Max = 0

	spec.Name = fmt.Sprintf("%s-%s", g.Name, uuid.New().String()[:8])
	if spec.Metadata == nil {
		spec.Metadata = make(map[string]string)
	}
	spec.Metadata[MetadataKey] = g.Name

	var hintOpts servers.SchedulerHintOptsBuilder
	if spec.SchedulerHints != nil {
		hintOpts = spec.SchedulerHints
	}

	if !g.settings.UseStaticCredentials {
		var err error
		if g.UseIgnition {
			err = InsertSSHKeyIgn(spec, g.settings.Username, g.sshPubKey)
		} else {
			err = InsertSSHKeyCloudInit(spec, g.settings.Username, g.sshPubKey)
		}
		if err != nil {
			return "", err
		}
	}

	if spec.ImageName != "" {
		imageRef, imgProps, err := g.client.GetImageByName(ctx, spec.ImageName)
		if err != nil {
			return "", err
		}

		spec.ImageRef = imageRef
		g.imgProps.Store(imgProps)

		g.log.Debug("Image resolved by name", "image_name", spec.ImageName, "image_ref", spec.ImageRef)
	}

	if spec.FlavorName != "" {
		flavorRef, err := g.client.GetFlavorByName(ctx, spec.FlavorName)
		if err != nil {
			return "", err
		}

		spec.FlavorRef = flavorRef

		g.log.Debug("Flavor resolved by name", "flavor_name", spec.FlavorName, "flavor_ref", spec.FlavorRef)
	}

	for _, networkName := range spec.NetworkNames {
		networkID, err := g.client.GetNetworkByName(ctx, networkName)
		if err != nil {
			return "", err
		}

		spec.Networks = append(spec.Networks, servers.Network{UUID: networkID})

		g.log.Debug("Network resolved by name", "network_name", networkName, "network_uuid", networkID)
	}

	var preCreatedVolumeID string

	if g.VolumeType != "" && g.VolumeSize > 0 {
		if spec.ImageRef == "" {
			return "", fmt.Errorf("volume_type/volume_size require server_spec.imageRef (or image_name) to be set")
		}

		if g.VolumePreCreate {
			volCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			volumeID, err := g.client.CreateVolumeFromImage(volCtx, spec.ImageRef, g.VolumeType, g.VolumeSize, spec.AvailabilityZone, spec.Name+"-root")
			cancel()
			if err != nil {
				if volumeID != "" {
					if delErr := g.client.DeleteVolume(ctx, volumeID); delErr != nil {
						g.log.Error("Failed to clean up volume after create failure", "volume_id", volumeID, "err", delErr)
					}
				}
				return "", fmt.Errorf("pre-creating boot volume: %w", err)
			}

			preCreatedVolumeID = volumeID
			spec.BlockDevice = []servers.BlockDevice{
				{
					SourceType:          servers.SourceVolume,
					UUID:                volumeID,
					DestinationType:     servers.DestinationVolume,
					BootIndex:           0,
					DeleteOnTermination: true,
				},
			}
		} else {
			spec.BlockDevice = []servers.BlockDevice{
				{
					SourceType:          servers.SourceImage,
					UUID:                spec.ImageRef,
					DestinationType:     servers.DestinationVolume,
					VolumeSize:          g.VolumeSize,
					VolumeType:          g.VolumeType,
					BootIndex:           0,
					DeleteOnTermination: true,
				},
			}
		}
		spec.ImageRef = ""
	}

	srv, err := g.client.CreateServer(ctx, spec, hintOpts)
	if err != nil {
		if preCreatedVolumeID != "" {
			if delErr := g.client.DeleteVolume(ctx, preCreatedVolumeID); delErr != nil {
				g.log.Error("Failed to clean up pre-created volume after server create failure", "volume_id", preCreatedVolumeID, "err", delErr)
			}
		}
		return "", err
	}

	return srv.ID, nil
}

func (g *InstanceGroup) ConnectInfo(ctx context.Context, instanceID string) (provider.ConnectInfo, error) {
	srv, err := g.client.GetServer(ctx, instanceID)
	if err != nil {
		return provider.ConnectInfo{}, fmt.Errorf("failed to get server %s: %w", instanceID, err)
	}

	if srv.Status != "ACTIVE" {
		return provider.ConnectInfo{}, fmt.Errorf("instance status is not active: %s", srv.Status)
	}

	ipAddr := srv.AccessIPv4
	if ipAddr == "" {
		netAddrs, err := extractAddresses(srv)
		if err != nil {
			return provider.ConnectInfo{}, err
		}

		for net, addrs := range netAddrs {
			for _, addr := range addrs {
				ipAddr = addr.Address
				g.log.Debug("Use address", "network", net, "ip_address", ipAddr)
			}
		}
	}

	info := provider.ConnectInfo{
		ConnectorConfig: g.settings.ConnectorConfig,
		ID:              instanceID,
		InternalAddr:    ipAddr,
		ExternalAddr:    ipAddr,
	}
	info.Protocol = provider.ProtocolSSH

	imgProps := g.imgProps.Load()

	if imgProps != nil {
		switch imgProps.OSType {
		case "", "linux":
			info.Protocol = provider.ProtocolSSH
			info.OS = "linux"

		case "windows":
			g.log.Warn("Windows not really supported by the plugin.")
			info.Protocol = provider.ProtocolWinRM
			info.OS = imgProps.OSType

		default:
			g.log.Warn("Unknown image os_type", "os_type", imgProps.OSType)
			info.OS = imgProps.OSType
		}

		switch imgProps.Architecture {
		case "", "x86_64":
			info.Arch = "amd64"

		case "aarch64":
			info.Arch = "arm64"

		default:
			g.log.Warn("Unknown image arch", "arch", imgProps.Architecture)
		}

	} else {
		info.OS = "linux"
		info.Arch = "amd64"
	}

	return info, nil
}

func (g *InstanceGroup) Shutdown(ctx context.Context) error {
	return nil
}
