openstack-fleeting-plugin
==========================

GitLab [Fleeting](https://gitlab.com/gitlab-org/fleeting/fleeting) plugin for OpenStack, used with the `instance` or `docker-autoscaler` executor.

> [!note]
> This repository is a detached fork of [sardinasystems/fleeting-plugin-openstack](https://github.com/sardinasystems/fleeting-plugin-openstack). It has its own history and its own releases going forward, with no live sync back to upstream.

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
| `name` | string | Server name template. `%d` is replaced with a per-instance counter, e.g. `"ci-runner-%d"` |
| `imageRef` | string | Image ID to boot from |
| `flavorRef` | string | Flavor ID — **must be an ID, not a name** (unlike the `openstack` CLI, the Compute API this plugin calls does not resolve flavor names) |
| `networks` | array | `[{ uuid = "..." }]` |
| `security_groups` | array of string | Security group names |

See [Advanced Configuration](#advanced-configuration) for `image_name`, `key_name`, `tags`, `scheduler_hints`, `user_data`, and everything else the Compute API accepts here.

> [!warning]
> `min_count`/`max_count` (standard Compute API fields, settable here since `server_spec` embeds the full create-server body) are forced to `0` before every request and cannot be used. The plugin already creates one server per call in its own scaling loop, incrementing the `%d` counter each time — letting Nova create more than one server per call would produce name collisions and silently double instance counts.

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
| `image_name` | string | Resolve `imageRef` by name instead — looked up on every instance creation, so an image re-tagged under the same name takes effect on the next clone without a config change. Prefer a fixed `imageRef` for reproducible clones |
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
        name = "ci-runner-%d"
        imageRef = "00000000-0000-0000-0000-000000000000"
        flavorRef = "00000000-0000-0000-0000-000000000001"
        networks = [ { uuid = "00000000-0000-0000-0000-000000000002" } ]
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

Note for anyone extending the integration test: a fleeting `Instance.ID()` is the Nova server UUID here (whatever `createInstance` returns to the fleeting core library), not the rendered `server_spec.name` — unlike the vSphere fleeting plugin, where the clone's unique name serves as both. The guest's actual hostname comes from `server_spec.name`, not from `ID()`.
