# Deploying with Simple Docker Ops

[Simple Docker Ops](https://simpledockerops.com) deploys Docker Compose applications to Linux machines through an agent, without SSH or inbound ports. This guide deploys mikrotik-home-netflow-plus to a server that way and covers upgrades and rollback.

You do not need this service to run the collector. Plain Docker Compose works too: see the [README](README.md#quick-start).

> The portal steps below follow the Simple Docker Ops documentation at the time of writing, which is marked beta. If a screen looks different, their docs at <https://simpledockerops.com/docs> are the authority.

- [What you need](#what-you-need)
- [1. Pair the server](#1-pair-the-server)
- [2. Create the application](#2-create-the-application)
- [3. Declare the variables](#3-declare-the-variables)
- [4. Cut a release](#4-cut-a-release)
- [5. Deploy](#5-deploy)
- [6. Check it](#6-check-it)
- [Upgrading](#upgrading)
- [Rolling back](#rolling-back)
- [Using your own build](#using-your-own-build)
- [Automating with the API](#automating-with-the-api)
- [Troubleshooting](#troubleshooting)

## What you need

- A Simple Docker Ops account.
- A Linux server on the same network as the router, with **Docker Engine 24 or newer** and the **Compose v2 plugin**, running as the system service `docker.service`. Docker installed with snap, rootless Docker and Podman are not supported by the agent.
- The router configured as described in [SETUP-MIKROTIK-ROUTER.md](SETUP-MIKROTIK-ROUTER.md), with the flow export target pointing at this server.
- The `flowmon` password you chose in that guide.

Check the server:

```sh
docker version --format '{{.Server.Version}}'   # 24 or newer
docker compose version --short                  # 2 or newer
systemctl is-active docker                      # active
```

Check that the ports the collector uses are free. It runs with host networking and binds them directly:

```sh
sudo ss -lntup | grep -E ':(8080|2055)\b'       # no output means both are free
```

If something already uses TCP 8080, pick another port now and use it for `HTTP_PORT` in step 3.

## 1. Pair the server

On the server:

```sh
curl -fsSL https://simpledockerops.com/install | sudo sh
```

The agent prints a pairing code. In the portal, open **Devices → Add device**, enter the code, name the device and choose a fleet.

The agent connects outbound only. Nothing new listens on the server.

## 2. Create the application

In the portal, open **Applications → New** and paste the contents of [`deploy/simpledockerops-compose.yml`](deploy/simpledockerops-compose.yml):

```yaml
services:
  netflow:
    image: kcdyer/mikrotik-home-netflow-plus:1.1.0
    restart: unless-stopped
    network_mode: host
    environment:
      NFP_EXPORTERS: ${ROUTER_IP}
      NFP_ROUTER_ADDR: ${ROUTER_IP}
      NFP_ROUTER_USER: ${ROUTER_USER}
      NFP_ROUTER_PASSWORD: ${ROUTER_PASSWORD}
      NFP_ROUTER_TLS: "true"
      NFP_SITES: ${SITES}
      NFP_HTTP_LISTEN: ":${HTTP_PORT}"
    volumes:
      - netflow-data:/data

volumes:
  netflow-data:
```

Three things about this file:

- **The image is public**, so no registry credentials are needed. To deploy a build of your own instead, see [Using your own build](#using-your-own-build).
- **`network_mode: host`** lets flow records arrive on UDP 2055 with the router's real source address, and serves the web interface on `HTTP_PORT`. There are no port mappings to maintain.
- **`netflow-data`** is a named volume holding the database. It survives upgrades.

There is no `env_file`. A file on your workstation does not exist on the server; every `${NAME}` becomes a variable you set in the portal.

## 3. Declare the variables

| Variable | Value | Secret |
|---|---|---|
| `ROUTER_IP` | The router's LAN address, for example `192.168.88.1` | no |
| `ROUTER_USER` | `flowmon` | no |
| `ROUTER_PASSWORD` | The password of the `flowmon` user | **yes** |
| `SITES` | Optional names for remote sites, for example `office=192.168.99.0/24`. Leave empty if you have none | no |
| `HTTP_PORT` | `8080`, or another free port | no |

Mark `ROUTER_PASSWORD` as secret. Secret values are stored encrypted and are not shown again after you save them.

Variables have defaults per application and can be overridden per fleet or per device. That is how one application can serve several sites with different routers.

To run without the router API (flow records only), remove the three `NFP_ROUTER_*` lines from the compose text and skip the matching variables.

## 4. Cut a release

Open **Releases → Cut release** and give it a version, for example `1.1.0`.

A release is an immutable snapshot: the compose text, the image pinned to its digest, and the default configuration. The preflight lists the platforms the image provides. It must include your server's architecture (`linux/amd64` for an x86 server, `linux/arm64` for a Raspberry Pi 4 or 5).

## 5. Deploy

Deploy the release to the device. For a single server one wave is enough; from the API that is `"strategy": {"waves": [100], "on_failure": "rollback"}`.

The platform counts the deployment healthy when the container is running and its health check passes. The image has a health check built in, so there is nothing to configure.

## 6. Check it

Open `http://<server>:<HTTP_PORT>` and go to **Status**:

| Card | What you want to see |
|---|---|
| Flow export | "Last export just now" |
| Router API | "Connected" |
| Flow coverage | Above 95% after about five minutes |

The sidebar indicator says **Live**. If it says **Delayed**, the Router API card shows why.

## Upgrading

1. In the application, change the image tag in the compose text to the new version.
2. Cut a new release.
3. Deploy it.

The database volume is kept. Open browser tabs and wall displays notice the new version and reload by themselves.

## Rolling back

Deploy the previous release again. Rollback re-deploys it in a single wave. The database format has not changed between releases so far; if a future release changes it, the release notes will say so.

## Using your own build

If you build the image yourself, push it to a registry the platform can pull from.

**The managed registry.** Every organisation gets private repositories under its own namespace at `registry.simpledockerops.com`:

```sh
docker login registry.simpledockerops.com
docker buildx build --platform linux/amd64 --build-arg VERSION=1.1.0 \
  -t registry.simpledockerops.com/<your-org>/mikrotik-home-netflow-plus:1.1.0 --push .
```

Log in with your portal account, a registry password set in the portal, or an API key. Then use that image name in the compose text. Devices pull from the managed registry with their own credential, so there is no `docker login` to run on the server.

**Another private registry.** Docker Hub, GHCR and any OCI registry work with a user name and token. Attach the credentials to the application; they are used to resolve the digest and for the device's pull.

Build for the architecture of the machine you deploy to, or for both with `--platform linux/amd64,linux/arm64`.

## Automating with the API

Everything above can be scripted. With an API key from **Developers** in the portal:

```sh
# Cut a release, then deploy it
curl -X POST https://app.simpledockerops.com/api/v1/applications/APP_ID/releases \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"version":"1.1.0","notes":"wall display"}'

curl -X POST https://app.simpledockerops.com/api/v1/deployments \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"release_id":"rel_…","target_type":"device","target_id":"DEVICE_ID",
       "strategy":{"waves":[100],"on_failure":"rollback"}}'

# Watch it
curl https://app.simpledockerops.com/api/v1/deployments/DEP_ID -H "Authorization: Bearer $KEY"
```

Keep the API key in your CI system's secret store, never in the repository.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| The container keeps restarting | Look at its logs from the portal, or on the server with `docker logs <container>`. `bind: address already in use` means another program holds the port: set `HTTP_PORT` to a free one and redeploy |
| The deployment never turns healthy | Same as above, or the image has no build for the server's architecture. Check the platforms in the release preflight |
| Web interface loads, Status says "No flow records received yet" | The router's flow target does not point at this server, or UDP 2055 is blocked. See [the router guide](SETUP-MIKROTIK-ROUTER.md#troubleshooting) |
| Router API "invalid user name or password" | `ROUTER_PASSWORD` is wrong, or the `flowmon` user on the router is restricted to a different address than this server's |
| Router API "TLS handshake failed" | `api-ssl` on the router has no certificate. See [the router guide](SETUP-MIKROTIK-ROUTER.md#3-give-the-api-a-certificate) |
| A changed variable has no effect | Configuration is part of what gets deployed. Redeploy after changing it; if that does not pick it up, cut a new release |
| History is gone after a redeploy | The application was renamed or recreated, which gives its volume a new name. Keep deploying new releases of the same application |
