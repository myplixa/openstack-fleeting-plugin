package fpoc

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func InsertSSHKeyCloudInit(spec *ExtCreateOpts, username, pubKey string) error {
	if username == "" {
		return fmt.Errorf("connector username is required to inject an SSH key via cloud-init")
	}

	cfg := map[string]any{}
	if spec.UserData != "" {
		body := strings.TrimPrefix(spec.UserData, "#cloud-config")
		if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
			return fmt.Errorf("failed to parse existing user_data as cloud-config: %w", err)
		}
	}

	usersRaw, _ := cfg["users"].([]any)

	var user map[string]any
	for _, u := range usersRaw {
		um, ok := u.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := um["name"].(string); name == username {
			user = um
			break
		}
	}

	if user == nil {
		user = map[string]any{
			"name":        username,
			"sudo":        "ALL=(ALL) NOPASSWD:ALL",
			"lock_passwd": false,
		}
		usersRaw = append(usersRaw, user)
	}

	keys, _ := user["ssh_authorized_keys"].([]any)
	keys = append(keys, strings.TrimSpace(pubKey))
	user["ssh_authorized_keys"] = keys

	cfg["users"] = usersRaw

	for key, defaultValue := range map[string]any{
		"package_update":             false,
		"package_upgrade":            false,
		"package_reboot_if_required": false,
		"final_message":              "Cloud-init finished successfully at $TIMESTAMP",
	} {
		if _, ok := cfg[key]; !ok {
			cfg[key] = defaultValue
		}
	}

	marshalled, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal cloud-config: %w", err)
	}

	spec.UserData = "#cloud-config\n" + string(marshalled)
	return nil
}
