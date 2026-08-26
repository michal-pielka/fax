//! Fax printer firmware.
//!
//! Subscribes to an MQTT topic and writes whatever arrives straight out of a
//! UART into the thermal printer. The payload is already finished ESC/POS --
//! the server renders it -- so there is no printer command knowledge here.
//! This is a byte pump.

mod config;
mod mqtt;
mod printer;
mod wifi;

use esp_idf_svc::eventloop::EspSystemEventLoop;
use esp_idf_svc::hal::peripherals::Peripherals;
use esp_idf_svc::mqtt::client::QoS;
use esp_idf_svc::nvs::EspDefaultNvsPartition;
use esp_idf_svc::sys::EspError;

use mqtt::Event;

fn main() -> Result<(), EspError> {
    // Required once, or some runtime patches fail to link.
    esp_idf_svc::sys::link_patches();
    esp_idf_svc::log::EspLogger::initialize_default();

    let peripherals = Peripherals::take()?;
    let sysloop = EspSystemEventLoop::take()?;
    let nvs = EspDefaultNvsPartition::take()?;

    let uart = printer::open(
        peripherals.uart1,
        peripherals.pins.gpio17, // our TX -> printer RX
        peripherals.pins.gpio16, // our RX <- printer TX
        config::BAUD_RATE,
    )?;

    // Kept alive for the whole program: dropping it powers down the radio.
    let _wifi = wifi::connect(
        peripherals.modem,
        sysloop,
        nvs,
        config::WIFI_SSID,
        config::WIFI_PASS,
    )?;

    let (mut client, events) = mqtt::connect()?;

    log::info!("broker    {}", config::MQTT_URL);
    log::info!("subscribe {}", config::JOB_TOPIC);
    log::info!("state     {}", config::STATE_TOPIC);

    // All client and printer work happens on this thread. Doing any of it in
    // the MQTT callback deadlocks -- see mqtt.rs.
    for event in events {
        match event {
            Event::Connected => {
                log::info!("connected to broker");

                client.subscribe(config::JOB_TOPIC, QoS::AtLeastOnce)?;
                client.publish(config::STATE_TOPIC, QoS::AtLeastOnce, true, config::ONLINE)?;

                log::info!("subscribed, announced online");
            }

            Event::Job(payload) => {
                log::info!("printing {} bytes", payload.len());
                printer::write(&uart, &payload)?;
                log::info!("printed");
            }
        }
    }

    Ok(())
}
