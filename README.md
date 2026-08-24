# openstack-fleeting-plugin

> [!note]
> This is a fork of [sardinasystems/fleeting-plugin-openstack](https://github.com/sardinasystems/fleeting-plugin-openstack), detached from upstream. It adds `volume_type`/`volume_size` boot-from-volume support (see [Resource Sizing](#resource-sizing)), dynamic SSH key injection for cloud-init images and not just Ignition (see [Default connector config](#default-connector-config)), computes VM names itself instead of requiring a `server_spec.name` template (see [VM Naming](#vm-naming)), and fixes real bugs in `nova_microversion`/`boot_time`/`min_count` handling.

This is a [fleeting plugin](https://gitlab.com/gitlab-org/fleeting/fleeting) for OpenStack, used with the `instance` or `docker-autoscaler` executor. It allows GitLab Runner to provision virtual machines from an image, enabling CI/CD jobs to be executed on dynamically created instances in your OpenStack project.

Documentation: https://docs.gitlab.com/runner/executors/docker_autoscaler.html


## Provider Configuration

The following parameters go under `[runners.autoscaler.plugin_config]`. This is the common case — everything a typical deployment needs:

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `name` | string | Yes | Identifier for the instance group, used to find and recognize this group's own instances among everything else in the project |
| `server_spec` | object | Yes | Server spec used to create instances — see [Server Spec](#server-spec) below. Mirrors the [Compute API's create-server body](https://docs.openstack.org/api-ref/compute/#create-server) |
| `cloud` | string | No | Name of the cloud entry to use from `clouds.yaml`. If unset, falls back to `OS_*` environment variables (see [Authentication](#authentication)) |
| `volume_type` | string | Sometimes | Boots the instance from a Cinder volume of this type, created from `server_spec.imageRef`/`image_name`, instead of the flavor's own local disk. Must be set together with `volume_size` — see [Resource Sizing](#resource-sizing). Some flavor catalogs require this |
| `volume_size` | int | Sometimes | Size of the boot volume in GB. Must be set together with `volume_type` |

See [Advanced Configuration](#advanced-configuration) for everything else — `clouds_config`, `nova_microversion`, `boot_time`, `use_ignition` all have working defaults and only need to be touched for specific edge cases.

### Server Spec

`server_spec` fields, in addition to the standard [Compute API create-server fields](https://docs.openstack.org/api-ref/compute/#create-server):

| Parameter | Type | Description |
|-----------|------|-------------|
| `imageRef` / `image_name` | string | Image ID or name to boot from — see [Resolving Names](#resolving-names) |
| `flavorRef` / `flavor_name` | string | Flavor ID or name — see [Resolving Names](#resolving-names) |
| `networks` / `network_names` | array | `[{ uuid = "..." }]`, or a plain list of network names — see [Resolving Names](#resolving-names) |
| `security_groups` | array of string | Security group names |

`server_spec.name`, if set, is ignored — see [VM Naming](#vm-naming). See [Advanced Configuration](#advanced-configuration) for `key_name`, `tags`, `scheduler_hints`, `user_data`, and everything else the Compute API accepts here.

### Resolving Names

The raw Compute API this plugin calls needs UUIDs for `imageRef`/`flavorRef`/`networks`, unlike the `openstack` CLI, which resolves names to UUIDs itself before sending the request. For readability, this plugin does that same resolution for you, on every instance creation (so a renamed/re-tagged resource takes effect on the next clone without touching config):

| Instead of | Use | Looked up via |
|---|---|---|
| `imageRef = "00000000-..."` | `image_name = "debian-11-infra-runners-0.1.1"` | Glance, by exact name |
| `flavorRef = "00000000-..."` | `flavor_name = "r2.4-16"` | Nova, by exact name |
| `networks = [{ uuid = "00000000-..." }]` | `network_names = ["nad-net-dc3"]` | Neutron, by exact name |

Each lookup fails loudly if the name matches zero or more than one resource — names aren't guaranteed unique in OpenStack, so an ambiguous name is treated as a hard error rather than silently picking one. `network_names` entries are appended to whatever's already in `networks`, so both can be used together if some networks are more conveniently referenced by ID.

> [!warning]
> `min_count`/`max_count` (standard Compute API fields, settable here since `server_spec` embeds the full create-server body) are forced to `0` before every request and cannot be used. The plugin already creates one server per call in its own scaling loop — letting Nova create more than one server per call would produce name collisions and silently double instance counts.

### VM Naming

Instances are named `<name>-<id>`, where `<name>` is the top-level `name` parameter and `<id>` is a random 8-character identifier generated per instance:

```
ci-runner-a1b2c3d4
```

Any `server_spec.name` set in config is ignored — the plugin always computes the name itself, the same way the vSphere fleeting plugin does. This is also what becomes the guest's hostname. Since the identifier is random rather than a sequential counter, names stay collision-free across `gitlab-runner` restarts (an in-process counter would restart from 1 and could collide with an existing instance that hasn't been cleaned up yet).

Unlike the vSphere fleeting plugin, this one doesn't need the name for instance recognition — every instance also gets a `fleeting-cluster` metadata key set to `name`, and that's what `getInstances` actually filters on, not the name prefix. The name is purely for human/hostname readability; `name` itself should still stay consistent between runs, since it's what shows up as the guest hostname and in `openstack server list`.

### Authentication

Two ways to authenticate, resolved by the underlying OpenStack client library — there is no separate `auth_from_env` toggle:

- Set `cloud` (and optionally `clouds_config`) to use a named entry from `clouds.yaml`.
- Leave `cloud` unset to fall back to `OS_*` environment variables on the process running `gitlab-runner`.

Application credentials (`OS_AUTH_TYPE=v3applicationcredential`) must **not** also set `OS_PROJECT_NAME`/`OS_PROJECT_DOMAIN_NAME`/`OS_USER_DOMAIN_NAME` — an application credential is already scoped to a project, and Keystone rejects a scoped auth request layered on top of one.

### Default connector config

| Parameter | Default |
|-----------|---------|
| `os` | `linux` |
| `protocol` | `ssh` |
| `username` | unset |
| `use_static_credentials` | `false` |

Two ways to get SSH access into the instance, for both cloud-init and Ignition images alike:

- **Dynamic (default, `use_static_credentials = false`)**: the plugin generates its own SSH keypair once at startup — reusing the private key from `connector_config.key`/`key_path` if one is already configured there, or generating a fresh one otherwise — and injects the public half into the instance at boot: as a `passwd.users` entry for Ignition, merged into `server_spec.user_data`'s `users:` list for cloud-init (existing `user_data` content is preserved, not overwritten). No Nova `key_name` is required for this path. `connector_config.username` selects which user gets the key; if unset, the plugin falls back to the image's `os_admin_user` property.
- **Static (`use_static_credentials = true`)**: the plugin does nothing — you're responsible for `server_spec.key_name` pointing at an already-registered Nova keypair, and `connector_config.username`/`key_path` matching it on the connector side.

On the cloud-init path, the plugin also sets `package_update`/`package_upgrade`/`package_reboot_if_required` to `false` and a `final_message` — the same defaults the vSphere fleeting plugin applies, for the same reason: the image is expected to already have everything it needs, and updating/rebooting on every clone only slows down provisioning and risks drift between clones of the same image. `final_message: "Cloud-init finished successfully at $TIMESTAMP"` shows up in the guest's own `/var/log/cloud-init-output.log`, useful when troubleshooting a clone that came up unreachable. Any of these already present in a user-supplied `server_spec.user_data` are left as the user set them, not overwritten.

Rendered example (no `user_data` set, `connector_config.username = "debian"`):

```yaml
#cloud-config
final_message: Cloud-init finished successfully at $TIMESTAMP
package_reboot_if_required: false
package_update: false
package_upgrade: false
users:
    - lock_passwd: false
      name: debian
      ssh_authorized_keys:
        - ssh-ed25519 AAAA... fleeting@ci-runner-a1b2c3d4
      sudo: ALL=(ALL) NOPASSWD:ALL
```


## Resource Sizing

By default the instance boots from the flavor's own local disk, at whatever size the flavor defines. `volume_type`/`volume_size` boot from a Cinder volume instead, letting several `[[runners]]` tiers share one flavor/image while requesting different disk sizes — the same purpose `num_cpus`/`memory_mb`/`disk_size_gb` serve on the vSphere fleeting plugin, achieved here through OpenStack's own [block-device-mapping mechanism](https://docs.openstack.org/nova/latest/user/block-device-mapping.html) (`block_device_mapping_v2`, `source_type: image` → `destination_type: volume`) rather than a clone-time hardware override, since CPU/RAM sizing on OpenStack is already handled natively by picking different flavors per tier.

```toml
[runners.autoscaler.plugin_config]
  volume_type = "ssd"
  volume_size = 100
```

> [!note]
> Some flavor catalogs forbid booting from local disk entirely (a `CUSTOM_LOCAL_DISK: forbidden` trait, not visible via `openstack flavor show` for non-admin users — only in the flavor object embedded in a server's own create/show response) — `volume_type`/`volume_size` are required, not optional, on those. Boot-from-volume can also be flakier than a local-disk boot depending on the backend (observed: real, intermittent host-level failures unrelated to this plugin). This isn't something the plugin can paper over — it's exactly the kind of transient failure GitLab Runner's own autoscaler is already designed to retry past, so a failed clone should simply be allowed to fail and get retried, not masked with client-side retry logic in the plugin itself.


## Advanced Configuration

Everything below has a working default and exists for edge cases — most deployments never set any of it.

`[runners.autoscaler.plugin_config]`:

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `clouds_config` | string | unset | Path to `clouds.yaml`. Only relevant if `cloud` is also set and the file isn't in one of `clouds.Parse`'s default search locations |
| `nova_microversion` | string | `2.79` | Nova Compute API microversion. An explicit `OS_COMPUTE_API_VERSION` environment variable takes priority over this field if both are set |
| `boot_time` | string | `2m` | How long to wait for cloud-init/Ignition to finish (checked via console log) before treating the instance as running anyway, Go duration string e.g. `"5m"` |
| `use_ignition` | bool | `false` | Use Ignition (Fedora CoreOS / Flatcar) instead of cloud-init for SSH key injection |

`server_spec`, on top of the [common fields](#server-spec):

| Parameter | Type | Description |
|-----------|------|-------------|
| `key_name` | string | Nova keypair name — only relevant under `use_static_credentials = true` (see [Authentication](#authentication)) |
| `tags` | array of string | Server tags, single-word or free-form text. Purely cosmetic Nova metadata — this plugin does not read tags for anything, it recognizes its own instances by a separate metadata key set internally. `"key: value"` strings are a common formatting convention, not a distinct mechanism Nova parses |
| `scheduler_hints` | object | e.g. `{ group = "..." }` for a (anti-)affinity server group |
| `user_data` | string | Raw `#cloud-config` or Ignition JSON, merged with rather than replaced by the plugin's own SSH key injection — see [Authentication](#authentication) |
| everything else `servers.CreateOpts` accepts | — | `description`, `availability_zone`, `metadata`, `config_drive`, `personality`, `access_ipv4`/`access_ipv6`, `hostname`, `OS-DCF:diskConfig`, `hypervisor_hostname`, etc. — passed straight through to the [Compute API](https://docs.openstack.org/api-ref/compute/#create-server) if set, none of it read by the plugin itself. `adminPass` in particular has no effect on how this plugin connects — access is always SSH-key based |


## OpenStack Setup

1. Create a dedicated user (recommended) and project, then either export `clouds.yaml` or set `OS_*` environment variables for it.
2. Optionally create a tenant network for workers (remember a router if it needs external access) — the manager VM then needs a port on both its own network and the workers' tenant network to reach them.
3. Upload an image with a container runtime installed. Any cloud-init-capable Linux image works; Fedora CoreOS/Flatcar are supported via Ignition.
4. *(Optional)* A Nova keypair is only needed if you want `use_static_credentials = true` — the default dynamic mode needs nothing pre-registered (see [Authentication](#authentication)).

Example resource provisioning via Heat: [heat/stack.yaml](heat/stack.yaml) — a starting point, not a turnkey template.


## Example Runner Config

Anonymized example — a `docker-autoscaler` tier booting from a 100GB volume, cloud-init image, dynamic SSH credentials (no Nova keypair needed):

```toml
concurrent = 16
check_interval = 0
shutdown_timeout = 0

[[runners]]
  name = "ci-runner"
  url = "https://gitlab.example.com"
  token = "glrt-xxxxxxxxxxxxxxxxxxxx"
  executor = "docker-autoscaler"
  limit = 16

  [runners.docker]
    image = "docker.example.com/python:3.11"
    privileged = false
    volumes = ["/var/run/docker.sock:/var/run/docker.sock", "/cache"]

  [runners.autoscaler]
    plugin = "fleeting-plugin-openstack"
    capacity_per_instance = 2
    max_use_count = 10
    max_instances = 16

    [runners.autoscaler.connector_config]
      username = "debian"
      use_external_addr = false

    [[runners.autoscaler.policy]]
      idle_count = 1
      idle_time = "5m0s"

    [runners.autoscaler.plugin_config]
      name = "ci-runner"
      volume_type = "ssd"
      volume_size = 100

      [runners.autoscaler.plugin_config.server_spec]
        image_name = "debian-11-infra-runners-0.1.1"
        flavor_name = "r2.4-16"
        network_names = [ "ci-runners-net" ]
        security_groups = [ "ci-runners" ]
```

`OS_*` authentication environment variables (see [Authentication](#authentication)) are set in the environment `gitlab-runner` runs under, not in `config.toml`.


## Development

### Build

```sh
make build
```

Cross-compile targets and other build details: see the [Makefile](Makefile).

### Tests

```sh
make test              # unit tests, no cloud access needed
make integration-test   # provisions a real instance and tears it down; needs a real OpenStack project
```

`integration-test` skips cleanly if `test/integration/config.json` doesn't exist — copy `test/integration/config.example.json` and fill in real values to run it.

Note for anyone extending the integration test: a fleeting `Instance.ID()` is the Nova server UUID here (whatever `createInstance` returns to the fleeting core library), not the computed `<name>-<id>` name — unlike the vSphere fleeting plugin, where the clone's unique name serves as both. The guest's actual hostname comes from the computed name (see [VM Naming](#vm-naming)), not from `ID()`.
