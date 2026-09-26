<div align="center">

```
 ________________________________
|                                |
|        *** F  A  X ***         |
|                                |
|   a receipt printer in my      |
|   flat. you type, it prints.   |
|   on real paper. for real.     |
|                                |
|  ----------------------------  |
|   NO APP        NO ACCOUNT     |
|   NO ALGORITHM  NO LIKES       |
|  ----------------------------  |
|                                |
|   >> fax.pielka.sh <<          |
|                                |
|      THANK YOU, COME AGAIN     |
|________________________________|
 \/\/\/\/\/\/\/\/\/\/\/\/\/\/\/\/
```

# [fax.pielka.sh](https://fax.pielka.sh)

**Go print me something. Seriously. I read every one.**

<!-- TODO: hero video of a receipt coming out of the printer -->

</div>

---

## what is this

There's a thermal receipt printer sitting in my flat, hooked up to the
internet. When you write something on **[fax.pielka.sh](https://fax.pielka.sh)**,
it prints out on a little strip of paper a few seconds later, and I read it.

You get nine lines of 32 characters, plus **bold**, underline and
white-on-black. Or send a photo, and it gets dithered into glorious 1-bit
dots. When you're done, swipe the receipt off the top of the screen to send it.

<!-- TODO: photo wall of received receipts -->

## how it works

```
  you ─▶ browser ─▶ Caddy ─▶ Go services ─▶ MQTT ─▶ ESP32-S3 ─USB─▶ printer ─▶ receipt ─▶ me
```

| piece         | what it does                                                               |
| ------------- | -------------------------------------------------------------------------- |
| `frontend/`   | a receipt you type on. Vanilla JS, no build step, dithers photos in-browser |
| `backend/`    | three tiny Go services: validate, render to ESC/POS, dispatch over MQTT     |
| `firmware/`   | Rust on an ESP32-S3, acting as USB host for the printer                    |
| `deploy/`     | Caddy + Mosquitto + Docker Compose on a small VPS                          |

The site only says "printed" once the printer confirms it had paper and
took the bytes.

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
