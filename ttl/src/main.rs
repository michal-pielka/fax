//! Fax printer firmware.
//!
//! Connects to WiFi, subscribes to an MQTT topic, and writes whatever arrives
//! straight out of a UART into the thermal printer.
//!
//! The payload is already finished ESC/POS -- the server renders it -- so
//! there is no printer command knowledge here at all. This is a byte pump.

use std::sync::mpsc;

use esp_idf_svc::eventloop::EspSystemEventLoop;
use esp_idf_svc::hal::delay::BLOCK;
use esp_idf_svc::hal::gpio::AnyIOPin;
use esp_idf_svc::hal::peripherals::Peripherals;
use esp_idf_svc::hal::uart::{config::Config as UartConfig, UartDriver};
use esp_idf_svc::hal::units::Hertz;
use esp_idf_svc::mqtt::client::{
    EspMqttClient, EventPayload, LwtConfiguration, MqttClientConfiguration, QoS,
};
use esp_idf_svc::nvs::EspDefaultNvsPartition;
use esp_idf_svc::sys::EspError;
use esp_idf_svc::wifi::{AuthMethod, BlockingWifi, ClientConfiguration, Configuration, EspWifi};

// Baked in at build time. A missing one is a compile error, which beats
// firmware that silently ships without credentials.
//
//   WIFI_SSID=... WIFI_PASS=... MQTT_URL=mqtt://192.168.0.251:1883 \
//   MQTT_PASS=... cargo run --release
const WIFI_SSID: &str = env!("WIFI_SSID");
const WIFI_PASS: &str = env!("WIFI_PASS");
const MQTT_URL: &str = env!("MQTT_URL");
const MQTT_PASS: &str = env!("MQTT_PASS");

const MQTT_USER: &str = "printer";
const CLIENT_ID: &str = "fax-printer-1";

const JOB_TOPIC: &str = "fax/printer-1/job/+";
const STATE_TOPIC: &str = "fax/printer-1/state";

/// Printed on the printer's self-test page (hold feed, then power on).
const BAUD_RATE: u32 = 9600;

/// `paper` is asserted, not measured. DTR on GPIO23 is wired but unread, so
/// this is a lie the gateway believes -- and has to be, since it refuses to
/// publish to a printer it thinks is empty.
const ONLINE: &[u8] = br#"{"online":true,"paper":true}"#;
const OFFLINE: &[u8] = br#"{"online":false,"paper":false}"#;

fn main() -> Result<(), EspError> {
    // Required once, or some runtime patches fail to link.
    esp_idf_svc::sys::link_patches();
    esp_idf_svc::log::EspLogger::initialize_default();

    let peripherals = Peripherals::take()?;
    let sysloop = EspSystemEventLoop::take()?;
    let nvs = EspDefaultNvsPartition::take()?;

    // ---- the printer ----
    //
    // Printer's 5-pin TTL header:
    //   pin 1 NC
    //   pin 2 TX  (printer out) -> GPIO16
    //   pin 3 RX  (printer in)  <- GPIO17
    //   pin 4 DTR (printer out) -> GPIO23, unused
    //   pin 5 GND
    //
    // UART0 is the USB console, so the printer gets UART1.
    let uart = UartDriver::new(
        peripherals.uart1,
        peripherals.pins.gpio17,      // our TX -> printer RX
        peripherals.pins.gpio16,      // our RX <- printer TX
        Option::<AnyIOPin>::None,     // no CTS
        Option::<AnyIOPin>::None,     // no RTS
        &UartConfig::default().baudrate(Hertz(BAUD_RATE)),
    )?;

    // ---- wifi ----
    //
    // Kept alive for the whole program: dropping it powers down the radio, and
    // MQTT would then fail with a confusing DNS error.
    let mut wifi = BlockingWifi::wrap(EspWifi::new(peripherals.modem, sysloop.clone(), Some(nvs))?, sysloop)?;

    wifi.set_configuration(&Configuration::Client(ClientConfiguration {
        ssid: WIFI_SSID.try_into().expect("SSID too long"),
        password: WIFI_PASS.try_into().expect("password too long"),
        auth_method: AuthMethod::WPA2Personal,
        ..Default::default()
    }))?;

    wifi.start()?;
    wifi.connect()?;

    // Associating is not the same as being routable -- DHCP has to finish, or
    // connecting to the broker fails.
    wifi.wait_netif_up()?;
    log::info!("wifi up, ip {}", wifi.wifi().sta_netif().get_ip_info()?.ip);

    // ---- mqtt ----
    let config = MqttClientConfiguration {
        client_id: Some(CLIENT_ID),
        username: Some(MQTT_USER),
        password: Some(MQTT_PASS),

        // Clean session: the broker must NOT hold jobs for us while we are
        // unplugged, or reconnecting would print hours of backlog at once.
        disable_clean_session: false,

        // If we die without disconnecting, the broker publishes this for us
        // and the gateway starts refusing jobs within seconds.
        lwt: Some(LwtConfiguration {
            topic: STATE_TOPIC,
            payload: OFFLINE,
            qos: QoS::AtLeastOnce,
            retain: true,
        }),

        ..Default::default()
    };

    // The callback runs on the ESP-IDF MQTT task, which is BLOCKED until the
    // callback returns. So it must not call the client and must not touch the
    // UART -- both need that task running, and doing either here deadlocks.
    // All it does is hand the job to the main thread.
    let (tx, rx) = mpsc::channel::<Vec<u8>>();

    let mut client = EspMqttClient::new_cb(MQTT_URL, &config, move |event| match event.payload() {
        EventPayload::Connected(_) => {
            // Cannot subscribe from here. Send an empty job as the signal --
            // see the loop below.
            let _ = tx.send(Vec::new());
        }
        EventPayload::Received { data, .. } => {
            let _ = tx.send(data.to_vec());
        }
        _ => {}
    })?;

    log::info!("broker {MQTT_URL}, waiting for jobs on {JOB_TOPIC}");

    // Everything real happens here, on the main thread, where the MQTT task is
    // free to service us.
    for payload in rx {
        if payload.is_empty() {
            // Connected. Subscriptions do not survive a reconnect, so this
            // runs every time, not once at startup.
            log::info!("connected to broker");
            client.subscribe(JOB_TOPIC, QoS::AtLeastOnce)?;
            client.publish(STATE_TOPIC, QoS::AtLeastOnce, true, ONLINE)?;
            log::info!("subscribed, announced online");
            continue;
        }

        log::info!("printing {} bytes", payload.len());

        // A single write may be partial, so loop until it has all gone out.
        let mut sent = 0;
        while sent < payload.len() {
            sent += uart.write(&payload[sent..])?;
        }

        // ESP-IDF returns before the bytes reach the wire, so block until they
        // actually have.
        uart.wait_tx_done(BLOCK)?;

        log::info!("printed");
    }

    Ok(())
}
