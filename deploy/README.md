# Homelab deployment

Everything runs on the homelab host; your laptop connects over the tailnet and
plays the part of the ESP32 until the firmware exists.

## One-time setup

    cp .env.example .env
    # set BIND_ADDR to this host's tailnet address: tailscale ip -4
    # set MQTT_PASSWORD to something long

Create the broker accounts. `-c` creates the file, so it is only used once:

    docker run --rm -v ./deploy/mosquitto:/m eclipse-mosquitto:2 \
      mosquitto_passwd -c -b /m/passwd backend  'YOUR_MQTT_PASSWORD'
    docker run --rm -v ./deploy/mosquitto:/m eclipse-mosquitto:2 \
      mosquitto_passwd -b /m/passwd printer  'A_DIFFERENT_PASSWORD'

The printer password is what gets compiled into the firmware. Keep it separate
from the backend's: the ACL is only worth having if the two accounts are.

    docker compose up -d --build

## Testing from your laptop

Set `FAX=<homelab tailnet address>`.

Nothing prints until the device says it is there, because the dispatcher
refuses to publish into the void:

    curl -X POST http://$FAX:8080/api/print -d '{"text":"hello"}'
    # {"error":"printer is offline"}   503

Pretend to be the ESP32. This is the retained announcement plus the last will
the real firmware will register:

    mosquitto_pub -h $FAX -u printer -P 'PRINTER_PASSWORD' \
      -t fax/printer-1/state -r -m '{"online":true,"paper":true}'

Now subscribe as the printer and leave it running:

    mosquitto_sub -h $FAX -u printer -P 'PRINTER_PASSWORD' \
      -t 'fax/printer-1/job/+' -v

Print from the browser at http://$FAX:8080, or:

    curl -X POST http://$FAX:8080/api/print \
      -d '{"text":"hello world","spans":[{"start":0,"end":5,"style":{"bold":true}}]}'

The subscriber prints the ESC/POS bytes. Pipe through `xxd` to read them:

    mosquitto_sub -h $FAX -u printer -P 'PRINTER_PASSWORD' \
      -t 'fax/printer-1/job/+' -C 1 | xxd

Expect `1b40 1b45 01 hello 1b45 00 " world" 1b6403`.

## Things that will catch you out

Topic names are a contract. Change `DEVICE_ID` on the server and a subscriber
still listening to the old topic goes quiet with no error anywhere -- the
broker accepts publishes nobody is listening to.

`docker compose up` rebuilds only with `--build`. After changing Go code,
`docker compose up -d --build <service>`.

The dispatcher exits if the broker is unreachable at startup. That is
deliberate, and the restart policy covers the moment before mosquitto is
listening -- a few restarts in `docker compose logs dispatcher` on first boot
are expected.
