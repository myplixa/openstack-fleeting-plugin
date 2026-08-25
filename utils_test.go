package fpoc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsCloudInitFinished(t *testing.T) {
	testCases := []struct {
		name     string
		file     string
		readLen  int
		expected bool
	}{
		{"token-not-fond-1", "testdata/console_out.txt", 4096, false},
		{"finished-1", "testdata/console_out.txt", 102400, true},
		{"token-not-fond-2", "testdata/console_ubuntu2204.txt", 4096, false},
		{"finished-2", "testdata/console_ubuntu2204.txt", 102400, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			buf, err := os.ReadFile(tc.file)
			require.NoError(t, err)

			var log string
			if len(buf) >= tc.readLen {
				log = string(buf[0:tc.readLen])
			} else {
				log = string(buf)
			}

			obtained := IsCloudInitFinished(log)
			assert.Equal(t, tc.expected, obtained)
		})
	}
}

func TestIsIgnitionFinished(t *testing.T) {
	testCases := []struct {
		name     string
		file     string
		readLen  int
		expected bool
	}{
		{"token-not-fond-1", "testdata/console_flatcar.txt", 4096, false},
		{"finished-1", "testdata/console_flatcar.txt", 102400, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			buf, err := os.ReadFile(tc.file)
			require.NoError(t, err)

			var log string
			if len(buf) >= tc.readLen {
				log = string(buf[0:tc.readLen])
			} else {
				log = string(buf)
			}

			obtained := IsIgnitionFinished(log)
			assert.Equal(t, tc.expected, obtained)
		})
	}
}

func TestExtCreateOpts(t *testing.T) {
	assert := assert.New(t)

	cfgJSON := `
	{
		"name": "gitlab-runner-%d",
		"description": "podman instance",
		"imageRef": "f2403879-6fbe-49a0-b71f-54b70039f32a",
		"flavorRef": "5",
		"key_name": "gitlab-autoscaler",
		"networks": [{"uuid": "c487d046-80ad-4da0-8b98-4a48ad3c257a"}],
		"security_groups": ["allow_gitlab_runner"],
		"scheduler_hints": {"group": "a5b557be-b7f0-4cb3-8f7c-6b5092f29c2c"},
		"tags": ["podman", "CI"],
		"user_data": "#!cloud-config\npackage_update: true\npackage_upgrade: true\n",
		"metadata": {"foo": "bar"}
	}
	`

	expected := `{"server":{"description":"podman instance","flavorRef":"5","imageRef":"f2403879-6fbe-49a0-b71f-54b70039f32a","key_name":"gitlab-autoscaler","metadata":{"foo":"bar"},"name":"gitlab-runner-%d","networks":[{"uuid":"c487d046-80ad-4da0-8b98-4a48ad3c257a"}],"security_groups":[{"name":"allow_gitlab_runner"}],"tags":["podman","CI"],"user_data":"IyFjbG91ZC1jb25maWcKcGFja2FnZV91cGRhdGU6IHRydWUKcGFja2FnZV91cGdyYWRlOiB0cnVlCg=="}}`

	cfg := new(ExtCreateOpts)
	err := json.Unmarshal([]byte(cfgJSON), cfg)
	assert.NoError(err)

	assert.Equal("a5b557be-b7f0-4cb3-8f7c-6b5092f29c2c", cfg.SchedulerHints.Group)

	omap, err := cfg.ToServerCreateMap()
	assert.NoError(err)
	assert.NotNil(omap)

	req, err := json.Marshal(omap)
	assert.NoError(err)
	assert.Equal(expected, string(req))

	//t.Log(omap)
	//t.Log(string(req))
}

func TestInsertSSHKeyIgn(t *testing.T) {
	testCases := []struct {
		name     string
		userData string
		expected string
	}{
		{"empty", "", `{"ignition":{"config":{"replace":{"verification":{}}},"proxy":{},"security":{"tls":{}},"timeouts":{},"version":"3.4.0"},"kernelArguments":{},"passwd":{"users":[{"name":"test","sshAuthorizedKeys":["testkey"]}]},"storage":{},"systemd":{}}`},
		{"diff-user", `{"ignition":{"version":"3.3.0"},"passwd":{"users":[{"name":"test2"}]}}`, `{"ignition":{"config":{"replace":{"verification":{}}},"proxy":{},"security":{"tls":{}},"timeouts":{},"version":"3.4.0"},"kernelArguments":{},"passwd":{"users":[{"name":"test2"},{"name":"test","sshAuthorizedKeys":["testkey"]}]},"storage":{},"systemd":{}}`},
		{"same-user", `{"ignition":{"version":"3.2.0"},"passwd":{"users":[{"name":"test","sshAuthorizedKeys":["testkey1"]}]}}`, `{"ignition":{"config":{"replace":{"verification":{}}},"proxy":{},"security":{"tls":{}},"timeouts":{},"version":"3.4.0"},"kernelArguments":{},"passwd":{"users":[{"name":"test","sshAuthorizedKeys":["testkey1","testkey"]}]},"storage":{},"systemd":{}}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)

			spec := &ExtCreateOpts{
				UserData: tc.userData,
			}

			err := InsertSSHKeyIgn(spec, "test", "testkey")
			assert.NoError(err)
			assert.Equal(tc.expected, spec.UserData)
		})
	}
}

func TestResolveUserDataFile(t *testing.T) {
	t.Run("loads file when user_data is empty", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cloud-init.yaml")
		require.NoError(t, os.WriteFile(path, []byte("#cloud-config\npackages:\n  - docker.io\n"), 0o600))

		opts := &ExtCreateOpts{UserDataFile: path}
		require.NoError(t, resolveUserDataFile(opts))

		assert.Equal(t, "#cloud-config\npackages:\n  - docker.io\n", opts.UserData)
	})

	t.Run("inline user_data takes precedence over file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "cloud-init.yaml")
		require.NoError(t, os.WriteFile(path, []byte("#cloud-config\npackages:\n  - docker.io\n"), 0o600))

		opts := &ExtCreateOpts{UserDataFile: path, UserData: "#cloud-config\ninline: true\n"}
		require.NoError(t, resolveUserDataFile(opts))

		assert.Equal(t, "#cloud-config\ninline: true\n", opts.UserData)
	})

	t.Run("no-op when neither is set", func(t *testing.T) {
		opts := &ExtCreateOpts{}
		require.NoError(t, resolveUserDataFile(opts))
		assert.Empty(t, opts.UserData)
	})

	t.Run("missing file returns error", func(t *testing.T) {
		opts := &ExtCreateOpts{UserDataFile: filepath.Join(t.TempDir(), "does-not-exist.yaml")}
		err := resolveUserDataFile(opts)
		require.ErrorContains(t, err, "reading user_data_file")
	})
}

func TestRenderUserDataTemplate(t *testing.T) {
	t.Run("substitutes RunnerName and Vars", func(t *testing.T) {
		opts := &ExtCreateOpts{UserData: "runner_tag = \"{{ .RunnerName }}\"\ntoken = \"{{ .Vars.download_ci_auth }}\"\n"}

		require.NoError(t, renderUserDataTemplate(opts, "ptnad-cloud", map[string]string{"download_ci_auth": "secret-token"}))

		assert.Equal(t, "runner_tag = \"ptnad-cloud\"\ntoken = \"secret-token\"\n", opts.UserData)
	})

	t.Run("substitutes Hostname with the control host's own hostname", func(t *testing.T) {
		wantHostname, err := os.Hostname()
		require.NoError(t, err)

		opts := &ExtCreateOpts{UserData: "instance = \"{{ .Hostname }}\"\n"}

		require.NoError(t, renderUserDataTemplate(opts, "ptnad-cloud", nil))

		assert.Equal(t, "instance = \""+wantHostname+"\"\n", opts.UserData)
	})

	t.Run("plain content with no directives round-trips unchanged", func(t *testing.T) {
		opts := &ExtCreateOpts{UserData: "#cloud-config\npackages:\n  - docker.io\n"}

		require.NoError(t, renderUserDataTemplate(opts, "ptnad-cloud", nil))

		assert.Equal(t, "#cloud-config\npackages:\n  - docker.io\n", opts.UserData)
	})

	t.Run("no-op when user_data is empty", func(t *testing.T) {
		opts := &ExtCreateOpts{}
		require.NoError(t, renderUserDataTemplate(opts, "ptnad-cloud", nil))
		assert.Empty(t, opts.UserData)
	})

	t.Run("broken template syntax returns an error", func(t *testing.T) {
		opts := &ExtCreateOpts{UserData: "runner_tag = \"{{ .RunnerName \"\n"}
		err := renderUserDataTemplate(opts, "ptnad-cloud", nil)
		require.ErrorContains(t, err, "parsing user_data as template")
	})
}
