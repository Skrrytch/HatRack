# MVP 5: MQTT

MVP 5 publishes the current hat over MQTT, so other systems can show it or
react to it: Home Assistant, Node-RED, a "do not disturb" lamp at the door.
It is the `mqtt` module planned in [MVP 2](mvp-2.md), built on its own.

## What it does

- **One publisher.** The module runs where the core runs, on the server. One
  state per user means one sender, however many devices there are. Agents
  neither run nor report it.
- **Publish only.** It never subscribes; nothing outside can change the time
  log through MQTT. A way back (switching hats from Home Assistant) is not
  planned.
- **Topics,** all retained, so a subscriber knows the state at once, also
  after its own restart:

  | Topic | Payload |
  |---|---|
  | `<topic>/state` | `{"mode":"wearing","working":true,"hat":"entwicklung","hat_name":"Tickets","color":"#8c4ea3","activity":"Implementierung","since":"2026-11-04T19:51:00+01:00"}` |
  | `<topic>/availability` | `online`, or `offline` (also the last will, so a crash shows) |

  `mode` is `wearing`, `paused` or `none`; `working` is true while wearing.
  Details (ticket keys) are never sent.
- **Home Assistant discovery** (optional): the module announces a device
  "HatRack" with four entities, all named in English:

  | Entity | Kind | Value |
  |---|---|---|
  | `hat` | sensor | the hat's name, `none` without a hat; key, colour and mode as attributes |
  | `activity` | sensor | the activity, `none` without one |
  | `working` | binary sensor | on while a hat is worn |
  | `since` | sensor, timestamp | when the current state began |

  Turning discovery off removes the entities again.

## Settings

In Setup → Defaults → MQTT (not per hat):

| Setting | Meaning |
|---|---|
| `broker` | `mqtt://host:1883` or `mqtts://host:8883`; a bare host gets port 1883 |
| `username`, `password` | the broker login; the password as `${env:NAME}` |
| `topic` | base topic, default `hatrack` |
| `discovery` | Home Assistant discovery on or off |
| `discovery_prefix` | default `homeassistant` |

The password stays out of the configuration: the server reads it from its
environment, for the container from an `.env` file next to the compose file.

## Not in MVP 5

- Switching hats from MQTT or Home Assistant.
- TLS with a self-signed broker certificate (the scratch image has no CA
  store; `mqtts://` works only with a certificate it can verify).
