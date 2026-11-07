# Configuration reference

Etapo reads one YAML file when it starts. Pass its path with `--config` (default: `config.yaml` in the working directory):

```bash
cp config.example.yaml config.yaml
etapo --config config.yaml run
```

Settings are read at startup, so restart etapo after editing the file. The one exception is the dashboard's **Update config.yaml** button, which changes a single plug address while etapo keeps running. See [Plug addresses](#plug-addresses) and [What etapo writes to the file](#what-etapo-writes-to-the-file).

Keep the file readable only by you, for example with `chmod 600 config.yaml`. It holds your TP-Link password, and so does every backup etapo makes of it.

## Validation

At startup etapo checks the whole file. If something is wrong it stops and names the field, for example:

```
invalid config "config.yaml": plugs[1] (kitchen-kettle): ip must be a valid IP address for type "tapo", got "plug.local"
```

These are rejected:

- A port outside 1 to 65535, a `flush_interval_sec`, `max_buffer_size`, `retention_days` or `poll_interval_sec` of zero or less, or a negative `cost_per_kwh`.
- A standby `start` or `end` that is not `HH:MM`, or a window where `start` equals `end`.
- Two plugs with the same `id`, a plug without an `id`, or a `type` other than `tapo` or `mock`.
- A `tapo` plug without a valid IP address.
- A `tapo` plug without a TP-Link username or password, whether set for the plug or in `tapo_auth`.
- Credentials that still hold the example placeholders (`your-tplink-email@example.com`, `your-tplink-password`, or any value starting with `REPLACE_WITH_`).

## Environment variables

Environment variables override the file. They are applied after the file is read.

| Variable | Overrides | Description |
|---|---|---|
| `ETAPO_PORT` | `server.port` | Port for the dashboard and API. Ignored if not a positive number. |
| `ETAPO_DB_PATH` | `storage.db_path` | SQLite database file. |
| `TAPO_USERNAME` | `tapo_auth.username` | TP-Link account email. |
| `TAPO_PASSWORD` | `tapo_auth.password` | TP-Link account password. |

## Example file

`config.example.yaml` in the repository is the starting point, with every section filled in. The sections below describe each key.

## server

| Key | Default | Description |
|---|---|---|
| `host` | `"0.0.0.0"` | Address the dashboard and API listen on. Use `"127.0.0.1"` to accept connections from this machine only. |
| `port` | `8080` | Port the dashboard and API listen on, from 1 to 65535. |

## storage

| Key | Default | Description |
|---|---|---|
| `db_path` | `"./data/etapo.db"` | SQLite database file. The folder is created if it does not exist. |
| `flush_interval_sec` | `30` | Seconds between writes of buffered readings to the database. Must be above 0. |
| `max_buffer_size` | `100` | Number of readings kept in memory before they are written at once. Must be above 0. |
| `retention_days` | `90` | Must be above 0. **Not enforced yet:** etapo does not delete readings older than this. |

## collector

| Key | Default | Description |
|---|---|---|
| `poll_interval_sec` | `10` | Seconds between readings from each plug. Must be above 0. Also sets when a plug counts as offline, see [Offline plugs](#offline-plugs). |

## billing

| Key | Default | Description |
|---|---|---|
| `currency` | `"EUR"` | Currency shown on the dashboard. `EUR`, `GBP` and `USD` show as €, £ and $; other codes are shown as written. |
| `cost_per_kwh` | `0.32` | Price per kWh, used for every cost estimate. Must not be negative. |

## standby

The standby baseline is the average power inside a time window, using the server's local time.

| Key | Default | Description |
|---|---|---|
| `start` | `"02:00"` | Start of the window, `HH:MM` in 24-hour time. |
| `end` | `"05:00"` | End of the window. An end earlier than the start wraps past midnight. |

## tapo_auth

The TP-Link account that the Tapo plugs are linked to.

| Key | Default | Description |
|---|---|---|
| `username` | none | TP-Link account email. Used by every `tapo` plug that does not set its own `username`. |
| `password` | none | TP-Link account password. Used by every `tapo` plug that does not set its own `password`. |

Enable **Third-Party Compatibility** in the Tapo app before the first start. See the README for the steps.

## plugs

A list of plugs. Each item has these keys:

| Key | Default | Description |
|---|---|---|
| `id` | none, required | Unique name for the plug. It appears in API URLs and is stored in the database. Changing it creates a new plug, and the history stays under the old id. |
| `name` | none | Name shown on the dashboard. |
| `model` | `"P115"` | Model shown on the dashboard. |
| `category` | `"General"` | Group used by the dashboard's category filter, for example `"Kitchen"`. |
| `type` | `"mock"` | `"tapo"` for a real plug. `"mock"` (or no value) for a simulated plug. |
| `ip` | none, required for `tapo` | Local IP address of the plug. This is where etapo looks for the plug first. See [Plug addresses](#plug-addresses). |
| `username` | `tapo_auth.username` | TP-Link account for this plug only. |
| `password` | `tapo_auth.password` | TP-Link password for this plug only. |
| `mock_profile` | `"default"` | For `mock` plugs: `fridge`, `tv`, `workstation`, `kettle` or `default`. Other values behave like `default`. |

```yaml
plugs:
  - id: "living-room-tv"
    name: "Living Room TV"
    model: "P115"
    category: "Entertainment"
    type: "tapo"
    ip: "192.168.1.50"
```

## Plug addresses

This section applies to `tapo` plugs.

Reserve each plug's IP address in your router, bound to the plug's MAC address. Most routers call this a DHCP reservation, and a reserved plug gets the same address every time it connects. Without a reservation, the router can give the plug a new address, for example after a power cut or a router restart.

etapo does not need the MAC address in the configuration. It learns each plug's MAC address on the first successful connection and stores it in the database. The `GET /api/plugs` response includes it as `mac`, so you can use it for the reservation. The Tapo app also shows it.

When a plug stops answering at its configured address, etapo does the following:

1. **Checks the device before logging in.** Each time etapo opens a session to a plug, it first asks the device at the configured address for its MAC address. If the address now belongs to a different plug on the same account, etapo refuses it and sends no commands, so that plug's session is not disturbed.
2. **Searches for the plug.** After three polls in a row fail, which is about 30 seconds with the default poll interval, etapo searches the /24 network of the plug's last known address for its MAC address. It searches at most once a minute.
3. **Moves to the new address.** If the plug is found at a different address, etapo uses that address immediately. Readings and on/off control work again without a restart.
4. **Offers to update `config.yaml`.** The dashboard shows a banner: the plug was found at one address, but the file still has the other. The **Update config.yaml** button writes the new address to the file. **Dismiss** hides the banner until the page is reloaded.

Limits:

- The search covers the /24 network (addresses `x.y.z.1` to `x.y.z.254`) of the plug's last known address. A plug that moved to another network is not found. Reserve its address in the router instead.
- A plug that has never connected has no stored MAC address, so it cannot be searched for. Its address has to be correct at first start.
- The search needs the same network reachability as the normal polling, because it sends a small UDP packet to each address.

## Offline plugs

A plug is offline when its latest stored reading is older than three poll intervals plus one flush interval. With the defaults that is 3 × 10 s + 30 s, so 60 seconds.

An offline plug shows an **Offline** badge on the dashboard. Its live watts are not counted in the total live power or the live burn rate. The API reports it as `"online": false`, and `GET /api/summary` gives the threshold in `offline_after_sec`.

## What etapo writes to the file

etapo changes `config.yaml` only when you press **Update config.yaml** in the dashboard. That action:

- Changes the `ip` line of that one plug. Every other line, comment and quote style is kept as it was.
- Copies the current file first to `config-YYYYMMDD-HHMMSS.bak` in the same folder, using the file's base name. Only the two newest backups are kept, and other `.bak` files are left alone.
- Writes the new content to a temporary file in the same folder, then renames it over the original, so an interrupted write does not leave a half-written file.
- Refuses the change if the plug is not at the address you clicked for, because it moved again in the meantime. Reload the dashboard and try again.
- Refuses the change if etapo was started without a file (no `--config`), or if the plug's `ip` value is not on the same line as its `ip:` key. In that case, edit the file by hand.

The running etapo updates its own copy of the address at once. Other settings in the file are read at the next start.

Only the `ip` of the plug is ever changed by etapo. Credentials, plug names and every other setting are never written.
