# Yggdrasil Manager

A lightweight, automated peer manager for the [Yggdrasil Network](https://yggdrasil-network.github.io/). It continuously monitors your active outbound peers, tests their latency and routing cost, removes underperforming peers, and automatically discovers and connects to new, optimal peers from a public registry.

> ⚠️ **IMPORTANT PREREQUISITE**
> This tool does not replace the Yggdrasil node itself. **The Yggdrasil Network daemon must be installed and actively running** on your machine for this manager to work. The manager communicates with Yggdrasil via its Admin API socket, which must be accessible.

## ✨ Features

- **Automated Peer Management**: Frees you from manually searching and adding peers.
- **Quality Control**: Monitors peer latency (TCP ping) and Yggdrasil routing cost.
- **Strike System**: Bad peers (down, high latency, or high cost) get strikes and are permanently removed after reaching the limit.
- **Country Prioritization**: Option to prefer peers from a specific country before falling back to the global pool.
- **Concurrent Pinging**: Efficiently tests multiple candidate peers simultaneously with a semaphore limiter.
- **Entropy Selection**: Randomly selects from the Top-N fastest peers to avoid network convergence on a single node.
- **Slow Start**: Paces the addition of new peers to let Yggdrasil stabilize the routing table.
- **Zero Dependencies**: Compiles to a single static binary; Docker image is based on `scratch`.

## ⚙️ Configuration

Configuration can be done via **Command-Line Flags** or **Environment Variables**. If both are provided, flags will take precedence.

| Flag | Env Variable | Description | Default | Limits |
| :--- | :--- | :--- | :--- | :--- |
| `-e`, `--endpoint` | `YGG_ENDPOINT` | Yggdrasil admin socket endpoint | *Auto-detected* *\** | - |
| `-p`, `--max-peers` | `MAX_PEERS` | Maximum number of outbound peers | `3` | Max `4` |
| `-l`, `--max-latency`| `MAX_LATENCY_MS` | Maximum acceptable ping (ms) | `150` | Min `100` |
| `-c`, `--max-cost` | `MAX_COST` | Maximum acceptable Yggdrasil routing cost | `250.0` | Min `150.0` |
| `--country` | `PEER_COUNTRY` | Preferred country from this [json](https://raw.githubusercontent.com/Yggdrasil-Unofficial/pubpeers/refs/heads/master/peers.json) | `""` (Worldwide) | - |
| `-h`, `--help` | - | Show help message | - | - |

*\* By default, the manager automatically scans common paths for the Yggdrasil socket (e.g., `/var/run/yggdrasil.sock`, `/run/yggdrasil/yggdrasil.sock`). If not found, it defaults to `unix:///var/run/yggdrasil.sock`.*

## 🚀 Installation & Running

### 1. From Binary

Download the latest binary for your architecture from the [Releases](../../releases) page.

```bash
# Example for amd64
wget https://github.com/split174/ygg-manager/releases/latest/download/yggmgr-linux-amd64
chmod +x yggmgr-linux-amd64

# Show help and all available options
./yggmgr-linux-amd64 --help

# Run with default settings
./yggmgr-linux-amd64

# Run with custom settings via flags
./yggmgr-linux-amd64 --max-peers 4 --country netherlands

# Run with custom settings via env vars
MAX_PEERS=4 PEER_COUNTRY=netherlands ./yggmgr-linux-amd64
```

### 2. With Docker

The image is built for `linux/amd64` and `linux/arm64` and published to the GitHub Container Registry (GHCR).

#### Option A: Docker Compose (Recommended)

1. Download the `docker-compose.yml` file from this repository or create your own:
   ```yaml
   services:
     ygg-manager:
       image: ghcr.io/split174/ygg-manager:latest
       container_name: ygg-manager
       restart: unless-stopped
       volumes:
         - /var/run/yggdrasil/yggdrasil.sock:/var/run/yggdrasil/yggdrasil.sock:z
       environment:
         - MAX_PEERS=3
         - MAX_LATENCY_MS=150
         - MAX_COST=250.0
         - PEER_COUNTRY=""
   ```
2. Start the service in the background:
   ```bash
   docker compose up -d
   ```
3. Check the logs to ensure it's working:
   ```bash
   docker compose logs -f
   ```

#### Option B: Docker CLI

If you prefer running a single command without creating files:

```bash
docker run -d \
  --name ygg-manager \
  --restart unless-stopped \
  -v /var/run/yggdrasil/yggdrasil.sock:/var/run/yggdrasil/yggdrasil.sock:z \
  -e MAX_PEERS=3 \
  -e MAX_LATENCY_MS=150 \
  -e MAX_COST=250.0 \
  -e PEER_COUNTRY="" \
  ghcr.io/split174/ygg-manager:latest
```

### 3. Via Systemd

If you are running Yggdrasil on a Linux host, running the manager as a systemd service is the recommended approach. *You can use Command-Line flags inside the `ExecStart` line.*

1. Download the binary and move it to `/usr/local/bin/`:
   ```bash
   sudo cp yggmgr-linux-amd64 /usr/local/bin/ygg-manager
   sudo chmod +x /usr/local/bin/ygg-manager
   ```

2. Create a systemd service file at `/etc/systemd/system/ygg-manager.service`:
   ```ini
   [Unit]
   Description=Yggdrasil Smart Peer Manager
   After=network.target yggdrasil.service
   Requires=yggdrasil.service

   [Service]
   Type=simple
   ExecStart=/usr/local/bin/ygg-manager --max-peers 3 --max-latency 150 --max-cost 250 --country ""
   Restart=on-failure
   RestartSec=10

   [Install]
   WantedBy=multi-user.target
   ```

3. Enable and start the service:
   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable ygg-manager
   sudo systemctl start ygg-manager
   ```

4. Check the logs:
   ```bash
   sudo journalctl -u ygg-manager -f
   ```
