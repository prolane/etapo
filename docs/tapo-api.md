# Tapo plug API (P115)

This document describes the local API of TP-Link Tapo P115 plugs as etapo uses it: the requests it sends, the fields it gets back, their units and values, and what etapo does with each field.

TP-Link does not publish a local API specification. The field list below comes from three sources:

- **Probe**: read-only calls to eight P115 plugs on a LAN, firmware `1.4.8 Build 260804 Rel.045837`, 2026-11-07 (about 23:20 Europe/Amsterdam). Only read methods were called. Values are from that moment. Identifiers, coordinates and SSIDs are left out.
- **Code**: `internal/collector/tapo/klap.go` and `discover.go`, which show what etapo reads.
- **Specs**: the Rust `tapo` crate 0.12.2 docs, which document some fields and their units; the python-kasa error-code list; and the `tapo-energy` Home Assistant integration, which reports a firmware issue with batched requests.

Where a meaning is not documented, this file says so. Items marked *unverified* have not been confirmed against a source.

---

## 1. Transport (KLAP)

Plugs answer over plain HTTP on port 80 on the LAN, with the KLAP (Kasa Local Authentication Protocol) handshake and AES encryption.

| Step | Request | Response | Notes |
|---|---|---|---|
| Handshake 1 | `POST http://<ip>/app/handshake1`, body: 16 random bytes (local seed) | 48 bytes: remote seed (16) + server hash (32) | Etapo checks the server hash = `SHA-256(local seed ‖ remote seed ‖ authHash)`. A mismatch means wrong credentials. |
| Handshake 2 | `POST http://<ip>/app/handshake2`, body: `SHA-256(remote seed ‖ local seed ‖ authHash)` | HTTP 200 | Sends the `TP_SESSIONID` cookie from handshake 1, if present. |
| Request | `POST http://<ip>/app/request?seq=N` | 32-byte signature + AES-128-CBC ciphertext | Body: `SHA-256(sig ‖ seq ‖ ciphertext)` (32 bytes) followed by the encrypted JSON `{"method", "params", "request_time", "terminalUUID"}`. |

`authHash = SHA-256(SHA-1(tapo email) ‖ SHA-1(tapo password))`. The SHA-1 is required by the protocol, see ADR-006.

Response JSON: `{"error_code": 0, "result": {...}}`. A non-zero `error_code` is returned by etapo as `tapo API error code: N`.

Session behaviour in etapo:

- A session is opened on the first request and renewed when it is older than 5 minutes, or after any request error (one retry).
- Each login first runs the discovery probe (UDP port 20002) to check the MAC address at the configured IP, then does the handshake, then calls `get_device_info` and refuses the session if the MAC is not the expected one (ADR-007).
- Etapo does **not** verify the 32-byte signature on responses. The LAN is trusted, so this is a known gap, not a bug that affects the data.

Request volume per plug (current code): one `get_device_info` plus one `get_energy_usage` every poll (10 s by default), and about three extra requests on each login (every 5 minutes), so about 750 requests per plug per hour.

---

## 2. Methods used by etapo

| Method | Sent by | When | What etapo uses |
|---|---|---|---|
| `get_energy_usage` | `Driver.GetEnergyUsage` | Every poll | Power, today and month energy, used for every stored reading. |
| `get_device_info` | `Driver.GetInfo`, `verifyDevice` | Every poll, and once per login | `device_on` (relay state), `mac` (identity check). `nickname` and `rssi` are decoded but not stored. |
| `set_device_info` with `{"device_on": bool}` | `Driver.SetPower` | When a user switches a plug (`POST /api/plugs/{id}/power`) | Switches the relay. |
| Discovery probe (UDP 20002) | `checkAddress`, network search | Before a login, and when a plug has moved | MAC address of the device at an IP (ADR-007). |

Probed but not used by etapo: `get_device_usage`, `get_current_power`, `get_device_running_info`. They are documented below because they are useful for checks and for the design.

---

## 3. `get_energy_usage`

The main telemetry call. Response from the probe (anonymised plug A, 2026-11-07 23:22 CET):

```json
{
  "current_power": 13032,
  "electricity_charge": [0, 0, 0],
  "local_time": "2026-11-07 23:22:56",
  "month_energy": 2315,
  "month_energy_mwh": 2315364,
  "month_runtime": 10042,
  "today_energy": 332,
  "today_energy_mwh": 332439,
  "today_runtime": 1402
}
```

| Field | Type, unit | Meaning | Observed (8 plugs) | Used by etapo |
|---|---|---|---|---|
| `current_power` | integer, **milliwatts** | Instantaneous power draw. | 0 to 175 154 mW | Yes. `EnergyReading.power_watts` = value / 1000. Feeds live power, peaks, averages, standby and the breakdown. |
| `today_energy` | integer, **watt-hours** | Energy since device-local midnight (device counter). | 0 to 956 Wh | Yes. `energy_today_kwh` = value / 1000. Feeds today's totals and cost. |
| `month_energy` | integer, **watt-hours** | Energy since the first of the device-local month. | 0 to 8 792 Wh | Yes. `energy_month_kwh` = value / 1000. |
| `today_energy_mwh` | integer, milliwatt-hours | Same as `today_energy` at higher resolution (`today_energy` is `today_energy_mwh` / 1000, rounded to the nearest Wh). | e.g. 332 439 | No. Not needed: the 1 Wh resolution is enough. |
| `month_energy_mwh` | integer, milliwatt-hours | As above, for the month. | e.g. 2 315 364 | No. |
| `today_runtime` | integer, **minutes** | Time the relay was on today. Counts relay-on time, not power draw. | 0 to 1 402 min | No. The runtime widget was removed in 1.10.0. |
| `month_runtime` | integer, minutes | Relay-on time this month. | 0 to 10 042 min | No. |
| `electricity_charge` | array of 3 integers | Cost data from the tariff set in the Tapo app. Per the `tapo` crate, the third element is the month's total charge. The first two are *unverified*. Unit not documented. | `[0, 0, 0]` on all plugs (no tariff set) | No. |
| `local_time` | string `YYYY-MM-DD HH:MM:SS` | Device clock, local time. | | No. etapo uses the server clock. |
| `voltage`, `current` | — | **Not returned by P115 firmware 1.4.8.** `parseEnergyUsage` reads them if present (mV and mA), otherwise voltage is set to 230 V and current is computed as power / 230 V. | absent on all 8 plugs | Fallback only. `voltage_v` and `current_ma` are therefore derived values, not measurements. |

Notes:

- Energy counters are whole watt-hours. They are device counters that only increase during a day and reset at device-local midnight (`today_energy`). The Tapo FAQ describes "today" as 0:00 to the current time. `month_energy` resets at the start of the month. The crate documents it as "current month", and the values agree, but this reset is not stated in a spec. The plugs and etapo both use `Europe/Amsterdam` here, so the reset times match. A plug with another time zone resets at its own midnight, which etapo does not follow.
- `current_power` is one sample at the time of the request, not an average. Etapo stores these samples and averages them.

---

## 4. `get_device_info`

Response from the probe (anonymised plug A). Identifiers, coordinates, the SSID and the MAC are redacted:

```json
{
  "auto_off_remain_time": 0,
  "auto_off_status": "off",
  "avatar": "plug",
  "charging_status": "normal",
  "default_states": {"type": "last_states"},
  "device_id": "<redacted>",
  "device_on": true,
  "fw_id": "00000000000000000000000000000000",
  "fw_ver": "1.4.8 Build 260804 Rel.045837",
  "has_set_location_info": true,
  "hw_id": "<redacted>",
  "hw_ver": "1.0",
  "ip": "192.168.x.x",
  "lang": "nl_NL",
  "latitude": "<redacted>",
  "longitude": "<redacted>",
  "mac": "60-15-6F-XX-XX-XX",
  "model": "P115",
  "nickname": "<base64 of the name in the Tapo app>",
  "oem_id": "<redacted>",
  "on_time": 878197,
  "overcurrent_status": "normal",
  "power_protection_status": "normal",
  "region": "Europe/Amsterdam",
  "rssi": -61,
  "signal_level": 2,
  "specs": "",
  "ssid": "<base64 of the Wi-Fi name>",
  "time_diff": 60,
  "type": "SMART.TAPOPLUG"
}
```

| Field | Type, unit | Meaning | Observed | Used by etapo |
|---|---|---|---|---|
| `device_on` | bool | Relay state. | `true` on all 8 | Yes. `DeviceInfo.IsOn`, stored as `plugs.is_on` on every poll. |
| `mac` | string, `AA-BB-CC-DD-EE-FF` | Device MAC address. | | Yes. Stored as `plugs.mac` and checked on every login (ADR-007). |
| `nickname` | string, base64 of UTF-8 | Name set in the Tapo app. | | Decoded into `DeviceInfo.Nickname`, not stored. The dashboard uses the name in `config.yaml`. |
| `rssi` | integer, dBm | Wi-Fi signal strength. | −73 to −25 | Decoded into `DeviceInfo.RSSI`, not stored or shown. |
| `signal_level` | integer | Coarse Wi-Fi level, presumably 0 to 3. *Unverified scale.* | 1 to 3 | No. |
| `model` | string | Model. | `P115` | Decoded, not used. The dashboard shows the model from `config.yaml`. |
| `type` | string | Product type. | `SMART.TAPOPLUG` | No. |
| `hw_ver`, `fw_ver`, `hw_id`, `fw_id`, `oem_id`, `device_id` | string | Hardware and firmware identifiers. `fw_ver` is `1.4.8 Build 260804 Rel.045837` on all 8. | | No. |
| `on_time` | integer, seconds | Time since the relay last switched on. | 878 197 | No. |
| `auto_off_status`, `auto_off_remain_time` | string, integer | Auto-off timer. | `off`, 0 | No. |
| `default_states` | object | Behaviour after a power cut. `last_states` = restore the last state. | | No. |
| `overcurrent_status`, `power_protection_status`, `charging_status` | string | Protection states. | `normal` on all 8 | No. Could raise alerts later. |
| `region` | string | Device time zone. Sets the reset time of `today_energy`. | `Europe/Amsterdam` | No. |
| `time_diff` | integer, minutes | Offset to UTC. *Inferred from the value 60 for UTC+1, unverified.* | 60 | No. |
| `latitude`, `longitude` | integer | Location, stored as an integer. *Scale inferred as degrees × 10 000, unverified.* Personal data. | | No. Not stored by etapo. |
| `ssid` | string, base64 | Wi-Fi network name. Personal data. | | No. |
| `lang`, `avatar`, `specs`, `has_set_location_info` | various | Display and setup settings. | | No. |

`get_device_running_info` returns the same fields as `get_device_info` except `mac`, `model`, `type`, `ssid`, `hw_ver`, `time_diff`, `fw_id`, `hw_id` and `oem_id`. It still has `device_id`. etapo does not use it.

---

## 5. Other read methods (probed, not used)

### `get_current_power`

```json
{"current_power": 13}
```

`current_power` is whole **watts** here, while `get_energy_usage.current_power` is **milliwatts**. Observed pairs (same moment, same plug): 13 W and 13 032 mW; 27 W and 27 131 mW; 175 W and 175 154 mW. The `tapo` crate documents this method as watts. Not used by etapo, because it has less resolution and needs a second request.

### `get_device_usage`

```json
{
  "power_usage": {
    "today": 332, "today_mwh": 332439,
    "past7": 2315, "past7_mwh": 2315364,
    "past30": 3354, "past30_mwh": 3354152
  },
  "saved_power": {
    "today": 1070, "today_mwh": 1069561,
    "past7": 7727, "past7_mwh": 7726636,
    "past30": 11294, "past30_mwh": 11293848
  },
  "time_usage": {"today": 1402, "past7": 10042, "past30": 14648}
}
```

| Field | Unit | Meaning |
|---|---|---|
| `power_usage.today`, `past7`, `past30` | Wh | Energy over today, the last 7 days and the last 30 days. On the probed plugs, `today` equalled `today_energy`. |
| `time_usage.today`, `past7`, `past30` | minutes | Relay-on time over the same periods. |
| `saved_power.*` | Wh | **Undocumented.** On plug A it is larger than the consumption (1 070 Wh against 332 Wh today), on plug B smaller (446 Wh against 956 Wh). It may be energy the plug did not use because of schedules or auto-off, but this is not confirmed. Do not rely on it. |

Not used by etapo. Its 7-day and 30-day figures could serve as a cross-check for the design.

### `get_device_running_info`

Same as `get_device_info` without `mac`, `model`, `type`, `ssid`, `hw_ver` and `time_diff`. Not used.

---

## 6. Units and conversions

| Quantity | Tapo unit | etapo unit (current) | Conversion |
|---|---|---|---|
| Power | mW (`get_energy_usage`), W (`get_current_power`) | W (float) | `/ 1000` for `get_energy_usage` |
| Energy today and this month | Wh (integer) | kWh (float) | `/ 1000` |
| Runtime | minutes | not used since 1.10.0 | — |
| Voltage | not returned | 230 V constant | — |
| Current | not returned | mA, derived as P / V | — |

---

## 7. Errors

Error responses carry `error_code` in the JSON body. Etapo logs them as `tapo API error code: N`. The names below come from the python-kasa `SmartErrorCode` list. Only the common ones are shown, and we have not seen most of them on P115 firmware 1.4.8.

| Code | Name (python-kasa) | Meaning |
|---|---|---|
| 0 | `SUCCESS` | OK. |
| 1003 | `TRANSPORT_UNKNOWN_CREDENTIALS_ERROR` | Credentials rejected. Reported after an upgrade that changed the encryption. |
| 1100 | `HAND_SHAKE_FAILED_ERROR` | Handshake failed. |
| 1111 | `LOGIN_FAILED_ERROR` | Login failed. |
| 1112 | `HTTP_TRANSPORT_FAILED_ERROR` | Transport failed (retryable). |
| 1200 | `MULTI_REQUEST_FAILED_ERROR` | A batched request failed. Etapo never batches. |
| −1002 | `UNKNOWN_METHOD_ERROR` | The method is not supported by this device. |
| −1003 | `JSON_DECODE_FAIL_ERROR` | The request JSON was not understood. |
| −1008 | `PARAMS_ERROR` | Invalid parameters. |
| −1301 | `DEVICE_ERROR` | Device-side error. |
| −1501 | `LOGIN_ERROR` | Login error. |

Over HTTP, a request that is refused returns a status other than 200. Etapo reports it as `request returned status N`.

---

## 8. Known quirks and risks

- **Batched requests are refused.** The `tapo-energy` integration reports that P115 firmware 1.4.x rejects `multipleRequest` with HTTP 403 after a successful login. Etapo sends one method per request, which avoids this.
- **Relay-on time is not active time.** `today_runtime` counts relay-on time. The dryer on plug H showed 843 minutes of runtime and 0 Wh. Anything called "runtime" in etapo must say which one it means.
- **Counters are whole Wh and reset at device midnight.** A poll missed around midnight can leave the last value of the previous day unknown.
- **No voltage on P115.** `voltage_v` in etapo's data is a constant, not a measurement.
- **A session belongs to the address it was opened at.** After a plug moves, the old address can answer for another plug on the same account. etapo checks the MAC address before and after each login to prevent this (ADR-007).
- **Two pollers on the same plugs.** If two etapo installations (for example one on a laptop and one on a Raspberry Pi) have the same plugs configured, each plug gets two sessions and each installation keeps its own database. Running both doubles the requests on each plug. We have not tested how the plugs behave with two concurrent sessions.
- **Privacy.** `latitude`, `longitude`, `ssid`, `device_id` and the MAC address are personal. Keep them out of logs, issues and documentation.

---

## 9. How etapo derives its values

| Etapo value | Source | How |
|---|---|---|
| `power_watts` | `get_energy_usage.current_power` | mW / 1000 |
| `energy_today_kwh` | `get_energy_usage.today_energy` | Wh / 1000 |
| `energy_month_kwh` | `get_energy_usage.month_energy` | Wh / 1000 |
| `voltage_v` | none | Constant 230 V (not measured) |
| `current_ma` | none | power / 230 V (derived) |
| `peak_watts_today` | readings since midnight | Maximum of `power_watts` |
| `avg_watts_today` | readings since midnight | Mean of `power_watts` |
| `standby_watts` | readings in the standby window | Mean of `power_watts` between `standby.start` and `standby.end` over the last N nights |
| `online` | latest reading age | Under three poll intervals plus one flush interval |
| `plugs.is_on` | `get_device_info.device_on` | Stored on every poll |
| `plugs.mac` | `get_device_info.mac` | Stored on the first successful poll |
