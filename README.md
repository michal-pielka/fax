# [fax.pielka.sh](https://fax.pielka.sh)

**Go print me something. Seriously. I read every one.**

<!-- TODO: hero video of a receipt coming out of the printer -->

---

## what is this

There's a thermal receipt printer sitting in my flat, hooked up to the
internet. When you write something on **[fax.pielka.sh](https://fax.pielka.sh)**,
it prints out on a little strip of paper a few seconds later, and I read it.

You get nine lines of 32 characters, plus **bold**, underline and
white-on-black. Or send a photo, and it gets dithered into glorious 1-bit
dots. When you're done, swipe the receipt off the top of the screen to send it.

<img width="1170" height="1428" alt="fax" src="https://github.com/user-attachments/assets/06aea23a-3358-4060-b0b3-5a38798ee363" />

## how it works

```
  you ─> browser ─> Caddy ─> Go services ─> MQTT ─> ESP32-S3 ─USB ─> printer ─> receipt ─> me
```

1. **Browser** (`frontend/`): a receipt you type on. Photos are scaled and
   dithered to 1-bit right there, then everything goes to `POST /api/print`.
2. **Caddy**: the only thing facing the internet. Handles HTTPS and routes
   the site, the API and the MQTT websocket.
3. **Go services** (`backend/`):
   - **gateway**: the public API. Checks the message fits the paper and calls
     the other two.
   - **renderer**: turns text or a photo into the printer's own command bytes
     (ESC/POS).
   - **dispatcher**: publishes the job over MQTT and waits for the printer to
     answer.
4. **Mosquitto**: the message broker. The printer's account can receive jobs
   but never create them.
5. **ESP32-S3** (`firmware/`, Rust): sits next to the printer and dials out
   to the broker over a secure websocket, so nothing at home is exposed. It
   checks for paper, pushes the bytes over USB and acks back.

The request waits the whole way, so the site only says "printed" once the
printer has actually taken the job. It all runs with Docker Compose on a
small VPS (`deploy/`).

<!-- TODO: photo of the hardware (ESP32-S3 + printer) -->

## run your own

```sh
cp .env.example .env    # set MQTT_PASSWORD
# create broker users `backend` and `printer`; see deploy/README.md
docker compose up -d --build
```

Flash the board (you'll need the [esp-rs](https://github.com/esp-rs) toolchain):

```sh
cd firmware
WIFI_SSID=... WIFI_PASS=... MQTT_PASS=... cargo run --release
```

More in [`deploy/README.md`](deploy/README.md).

---

<div align="center">

**[fax.pielka.sh](https://fax.pielka.sh)**

*the paper is waiting.*

<sub>Receipt font: <a href="https://fonts.google.com/specimen/VT323"><i>VT323</i></a> by Peter Hull, under the SIL Open Font License.</sub>

</div>
