package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"gitlab.com/gitlab-org/fleeting/fleeting"
	"gitlab.com/gitlab-org/fleeting/fleeting/connector"
	"gitlab.com/gitlab-org/fleeting/fleeting/provider"

	osplugin "github.com/myplixa/openstack-fleeting-plugin"
)

var (
	pluginBinaryPath = flag.String("plugin-binary-path", "", "Path to the plugin binary")
	configFilePath   = flag.String("config-path", "", "Path to the configuration file")
)

type Config struct {
	PluginConfig    osplugin.InstanceGroup   `json:"plugin_config"`
	ConnectorConfig provider.ConnectorConfig `json:"connector_config"`
}

func TestRealOpenStackProvisioning(t *testing.T) {
	if *pluginBinaryPath == "" {
		t.Skip("plugin binary path is missing, skipping")
	}
	if *configFilePath == "" {
		t.Skip("config file path is missing, skipping")
	}
	if _, err := os.Stat(*configFilePath); os.IsNotExist(err) {
		t.Skipf("config file %q does not exist, skipping (copy config.example.json and fill in real OpenStack details to run this test)", *configFilePath)
	}

	configFile, err := os.Open(*configFilePath)
	require.NoError(t, err)
	defer func() { require.NoError(t, configFile.Close()) }()

	var cfg Config
	require.NoError(t, json.NewDecoder(configFile).Decode(&cfg))

	configJSON, err := json.Marshal(&cfg.PluginConfig)
	require.NoError(t, err)

	runner, err := fleeting.RunPlugin(*pluginBinaryPath, configJSON)
	require.NoError(t, err)
	t.Cleanup(runner.Kill)

	subCh := make(chan fleeting.Instance, 10)

	provisioner, err := fleeting.Init(context.Background(), nil, runner.InstanceGroup(),
		fleeting.WithMaxSize(1),
		fleeting.WithInstanceGroupSettings(provider.Settings{
			ConnectorConfig: cfg.ConnectorConfig,
		}),
		fleeting.WithSubscriber(func(instances []fleeting.Instance) {
			for _, inst := range instances {
				subCh <- inst
			}
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { provisioner.Shutdown(context.Background()) })

	t.Log("requesting 1 instance")
	provisioner.Request(1)

	var inst fleeting.Instance
	timeout := time.After(6 * time.Minute)
loop:
	for {
		select {
		case i := <-subCh:
			t.Logf("instance update: id=%s state=%s cause=%s", i.ID(), i.State(), i.Cause())
			if i.State() == provider.StateRunning {
				inst = i
				break loop
			}
		case <-timeout:
			t.Fatal("timed out waiting for instance to reach running state")
		}
	}

	require.Equal(t, fleeting.CauseRequested, inst.Cause())
	t.Logf("instance running: %s", inst.ID())

	t.Cleanup(func() {
		t.Logf("deleting instance: %s", inst.ID())
		inst.Delete()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		for {
			select {
			case i := <-subCh:
				if i.ID() == inst.ID() && i.State() == provider.StateDeleted {
					return
				}
			case <-ctx.Done():
				t.Logf("timed out waiting for instance %s to report deleted; provisioner shutdown will retry", inst.ID())
				return
			}
		}
	})

	info, err := inst.ConnectInfo(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, info.Username, "username unexpectedly empty from fleeting plugin")

	runCommand := func(t *testing.T, command string) string {
		t.Helper()

		var stdout, stderr bytes.Buffer
		err := connector.Run(context.Background(), info, connector.ConnectorOptions{
			RunOptions: connector.RunOptions{
				Command: command,
				Stdout:  &stdout,
				Stderr:  &stderr,
			},
		})
		require.NoErrorf(t, err, "command %q failed, stderr: %s", command, stderr.String())

		return stdout.String()
	}

	t.Run("ssh access", func(t *testing.T) {
		out := runCommand(t, "echo fleeting-realtest-ok")
		require.Contains(t, out, "fleeting-realtest-ok")
	})

	t.Run("dynamic ssh key actually authenticates", func(t *testing.T) {
		out := runCommand(t, "whoami")
		require.Contains(t, out, info.Username)
	})

	t.Run("guest hostname is prefixed with the instance group name", func(t *testing.T) {
		out := runCommand(t, "hostname")
		require.Contains(t, out, cfg.PluginConfig.Name)
	})
}
