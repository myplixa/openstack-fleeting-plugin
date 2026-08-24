package fpoc

import (
	"strings"
	"testing"
)

func TestInsertSSHKeyCloudInitFresh(t *testing.T) {
	spec := &ExtCreateOpts{}

	err := InsertSSHKeyCloudInit(spec, "debian", "ssh-ed25519 AAAATEST test@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasPrefix(spec.UserData, "#cloud-config") {
		t.Fatalf("missing #cloud-config header: %s", spec.UserData)
	}
	if !strings.Contains(spec.UserData, "debian") {
		t.Fatalf("missing username: %s", spec.UserData)
	}
	if !strings.Contains(spec.UserData, "ssh-ed25519 AAAATEST test@example.com") {
		t.Fatalf("missing pubkey: %s", spec.UserData)
	}
	for _, want := range []string{
		"package_update: false",
		"package_upgrade: false",
		"package_reboot_if_required: false",
		"final_message: Cloud-init finished successfully at $TIMESTAMP",
	} {
		if !strings.Contains(spec.UserData, want) {
			t.Fatalf("missing %q: %s", want, spec.UserData)
		}
	}
}

func TestInsertSSHKeyCloudInitMergesExisting(t *testing.T) {
	spec := &ExtCreateOpts{
		UserData: "#cloud-config\npackage_update: false\ntimezone: Europe/Moscow\n",
	}

	err := InsertSSHKeyCloudInit(spec, "debian", "ssh-ed25519 AAAATEST test@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(spec.UserData, "timezone: Europe/Moscow") {
		t.Fatalf("existing user_data content lost: %s", spec.UserData)
	}
	if !strings.Contains(spec.UserData, "ssh-ed25519 AAAATEST test@example.com") {
		t.Fatalf("missing pubkey: %s", spec.UserData)
	}
}

func TestInsertSSHKeyCloudInitPreservesUserPackageChoice(t *testing.T) {
	spec := &ExtCreateOpts{
		UserData: "#cloud-config\npackage_update: true\n",
	}

	err := InsertSSHKeyCloudInit(spec, "debian", "ssh-ed25519 AAAATEST test@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(spec.UserData, "package_update: true") {
		t.Fatalf("user's own package_update: true was overwritten: %s", spec.UserData)
	}
}

func TestInsertSSHKeyCloudInitNoUsername(t *testing.T) {
	spec := &ExtCreateOpts{}

	err := InsertSSHKeyCloudInit(spec, "", "ssh-ed25519 AAAATEST test@example.com")
	if err == nil {
		t.Fatal("expected error for empty username")
	}
}
