openstack-fleeting-plugin
==========================

GitLab [Fleeting](https://gitlab.com/gitlab-org/fleeting/fleeting) plugin for OpenStack, used with the `instance` or `docker-autoscaler` executor.

> [!note]
> This repository is a detached fork of [sardinasystems/fleeting-plugin-openstack](https://github.com/sardinasystems/fleeting-plugin-openstack). It has its own history and its own releases going forward, with no live sync back to upstream.

Documentation: https://docs.gitlab.com/runner/executors/docker_autoscaler.html


## Provider Configuration

The following parameters go under `[runners.autoscaler.plugin_config]`:

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `name` | string | Yes | Identifier for the instance group, used to find and recognize this group's own instances among everything else in the project |
| `server_spec` | object | Yes | Server spec used to create instances — see [Server Spec](#server-spec) below. Mirrors the [Compute API's create-server body](https://docs.openstack.org/api-ref/compute/#create-server) |
| `cloud` | string | No | Name of the cloud entry to use from `clouds.yaml`. If unset, falls back to `OS_*` environment variables (see [Authentication](#authentication)) |
| `clouds_config` | string | No | Path to `clouds.yaml`. Only relevant if `cloud` is set |
| `nova_microversion` | string | No | Nova Compute API microversion. Default `2.79` |
| `boot_time` | string | No | Maximum time to wait for cloud-init/Ignition to finish before treating the instance as running anyway (Go duration string, e.g. `"5m"`) |
| `use_ignition` | bool | No | Use Ignition (Fedora CoreOS / Flatcar) instead of cloud-init for SSH key injection |
| `volume_type` | string | No | Boots the instance from a Cinder volume of this type, created from `server_spec.imageRef`/`image_name`, instead of the flavor's own local disk. Must be set together with `volume_size` — see [Resource Sizing](#resource-sizing) |
| `volume_size` | int | No | Size of the boot volume in GB. Must be set together with `volume_type` |

### Server Spec

`server_spec` fields, in addition to the standard [Compute API create-server fields](https://docs.openstack.org/api-ref/compute/#create-server):

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | string | Server name template. `%d` is replaced with a per-instance counter, e.g. `"ci-runner-%d"` |
| `imageRef` | string | Image ID to boot from |
| `image_name` | string | Resolve `imageRef` by name instead — looked up on every instance creation, so an image re-tagged under the same name takes effect on the next clone without a config change |
| `flavorRef` | string | Flavor ID — **must be an ID, not a name** (unlike the `openstack` CLI, the Compute API this plugin calls does not resolve flavor names) |
| `key_name` | string | Nova keypair name for SSH access — required for cloud-init images, optional for Ignition (see [Authentication](#authentication)) |
| `networks` | array | `[{ uuid = "..." }]` |
| `security_groups` | array of string | Security group names |
| `tags` | array of string | Server tags, single-word or free-form text (Nova does not parse structure out of a tag — `"key: value"` strings are commonly used as a convention, not a distinct mechanism) |
| `scheduler_hints` | object | e.g. `{ group = "..." }` for a (anti-)affinity server group |
| `user_data` | string | Raw `#cloud-config` or Ignition JSON. With `use_ignition = true`, the plugin parses this and injects a `passwd.users` entry for the dynamically generated SSH key rather than replacing the whole document |

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

For cloud-init images, `use_static_credentials = true` is required, together with `username` and `key_path` (a private key matching `server_spec.key_name`) — the plugin does not generate or rotate credentials itself outside Ignition mode. For Ignition images, the plugin can instead generate a per-boot SSH keypair and inject it dynamically.


## Resource Sizing

By default the instance boots from the flavor's own local disk, at whatever size the flavor defines. `volume_type`/`volume_size` boot from a Cinder volume instead, letting several `[[runners]]` tiers share one flavor/image while requesting different disk sizes — the same purpose `num_cpus`/`memory_mb`/`disk_size_gb` serve on the vSphere fleeting plugin, achieved here through OpenStack's own [block-device-mapping mechanism](https://docs.openstack.org/nova/latest/user/block-device-mapping.html) (`block_device_mapping_v2`, `source_type: image` → `destination_type: volume`) rather than a clone-time hardware override, since CPU/RAM sizing on OpenStack is already handled natively by picking different flavors per tier.

```toml
[runners.autoscaler.plugin_config]
  volume_type = "ssd"
  volume_size = 100
```

> [!note]
> Some flavor catalogs forbid booting from local disk entirely (a `CUSTOM_LOCAL_DISK: forbidden` trait, not visible via `openstack flavor show` for non-admin users — only in the flavor object embedded in a server's own create/show response) — `volume_type`/`volume_size` are required, not optional, on those. Boot-from-volume can also be flakier than a local-disk boot depending on the backend (observed: real, intermittent host-level failures unrelated to this plugin, on the order of 1 in 3 attempts on one deployment). This isn't something the plugin can paper over — it's exactly the kind of transient failure GitLab Runner's own autoscaler is already designed to retry past, so a failed clone should simply be allowed to fail and get retried, not masked with client-side retry logic in the plugin itself.


## OpenStack Setup

1. Create a dedicated user (recommended) and project, then either export `clouds.yaml` or set `OS_*` environment variables for it.
2. Optionally create a tenant network for workers (remember a router if it needs external access) — the manager VM then needs a port on both its own network and the workers' tenant network to reach them.
3. Upload an image with a container runtime installed. Any cloud-init-capable Linux image works; Fedora CoreOS/Flatcar are supported via Ignition.
4. For cloud-init images, generate an SSH keypair and register the public key with Nova (`openstack keypair create`) — required, since cloud-init mode has no dynamic-key path (see [Authentication](#authentication)). Not required for Ignition, which the plugin can key dynamically per boot.

Example resource provisioning via Heat: [heat/stack.yaml](heat/stack.yaml) — a starting point, not a turnkey template.


## Example Runner Config

Anonymized example — a `docker-autoscaler` tier booting from a 100GB volume, cloud-init image, static SSH credentials:

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
      use_static_credentials = true
      key_path = "/etc/gitlab-runner/id_ed25519"
      use_external_addr = false

    [[runners.autoscaler.policy]]
      idle_count = 1
      idle_time = "5m0s"

    [runners.autoscaler.plugin_config]
      name = "ci-runner"
      nova_microversion = "2.79"
      boot_time = "5m"
      volume_type = "ssd"
      volume_size = 100

      [runners.autoscaler.plugin_config.server_spec]
        name = "ci-runner-%d"
        imageRef = "00000000-0000-0000-0000-000000000000"
        flavorRef = "00000000-0000-0000-0000-000000000001"
        key_name = "ci-runners"
        networks = [ { uuid = "00000000-0000-0000-0000-000000000002" } ]
        security_groups = [ "ci-runners" ]
        tags = ["managed_by: fleeting-plugin"]
```

`OS_*` authentication environment variables (see [Authentication](#authentication)) are set in the environment `gitlab-runner` runs under, not in `config.toml`.
