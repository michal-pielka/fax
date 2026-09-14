//! Fax printer firmware: subscribe to a topic, hand what arrives to the
//! printer. The server renders the ESC/POS, so this is a byte pump.

mod config;
mod mqtt;
mod printer;
mod time;
mod usb;
mod wifi;

use esp_idf_svc::eventloop::EspSystemEventLoop;
use esp_idf_svc::hal::peripherals::Peripherals;
use esp_idf_svc::mqtt::client::{EspMqttClient, QoS};
use esp_idf_svc::nvs::EspDefaultNvsPartition;
use esp_idf_svc::sys::EspError;

use mqtt::Event;

fn main() -> Result<(), EspError> {
    // Required once, or some runtime patches fail to link.
    esp_idf_svc::sys::link_patches();
    esp_idf_svc::log::EspLogger::initialize_default();

    // Logged before anything can fail, so a topic that disagrees with the
    // server is visible even when the connection never comes up.
    log::info!("broker    {}", config::MQTT_URL);
    log::info!("subscribe {}", config::JOB_TOPIC);
    log::info!("state     {}", config::STATE_TOPIC);

    let peripherals = Peripherals::take()?;
    let sysloop = EspSystemEventLoop::take()?;
    let nvs = EspDefaultNvsPartition::take()?;

    // The printer, on the S3's USB port. It need not be there yet: jobs
    // wait for it, and it may come and go.
    let printer = printer::Printer::new(usb::open()?);

    // Kept alive for the whole program: dropping it powers down the radio.
    let _wifi = wifi::connect(
        peripherals.modem,
        sysloop,
        nvs,
        config::WIFI_SSID,
        config::WIFI_PASS,
    )?;

    // Before MQTT: TLS checks certificate dates and this board has no clock.
    // Kept alive so it stays corrected.
    let _sntp = time::sync_blocking(std::time::Duration::from_secs(15))?;

    let (mut client, events) = mqtt::connect()?;

    // All client and printer work happens here; doing it in the callback
    // deadlocks (see mqtt.rs). recv returns Err only when the callback's
    // sender is gone, and the client is gone with it.
    while let Ok(event) = events.recv() {
        match event {
            Event::Connected => {
                log::info!("connected to broker");

                client.subscribe(config::JOB_TOPIC, QoS::AtLeastOnce)?;
                client.publish(config::STATE_TOPIC, QoS::AtLeastOnce, true, config::ONLINE)?;

                log::info!("subscribed, announced online");
            }

            Event::Job { id, payload } => {
                // Measured now, not cached: a browser is waiting on this ack,
                // and a stale answer either refuses a job that would have
                // printed or blasts bytes at an empty slot. Silence is not
                // "no paper" -- see has_paper -- so an unanswered query prints.
                if printer.has_paper() == Some(false) {
                    log::warn!("job {id} refused, no paper");
                    ack(&mut client, &id, config::ACK_NO_PAPER)?;
                    continue;
                }

                log::info!("printing {} bytes for {id}", payload.len());

                // A printer that is unplugged, off, or not taking bytes is a
                // job that did not print, not a reason for the board to die.
                let started = std::time::Instant::now();
                if let Err(e) = printer.write(&payload) {
                    log::warn!("could not write {id}: {e}");
                    ack(&mut client, &id, config::ACK_NO_CONFIRM)?;
                    continue;
                }
                log::info!("printer took {} bytes in {} ms", payload.len(), started.elapsed().as_millis());

                if printer.wait_done(payload.len()) {
                    log::info!("printed {id}");
                    ack(&mut client, &id, config::ACK_OK)?;
                } else {
                    // It may well have printed; we cannot say so.
                    log::warn!("no confirmation for {id}");
                    ack(&mut client, &id, config::ACK_NO_CONFIRM)?;
                }
            }
        }
    }

    Ok(())
}

/// Publish the ack for one job. Not retained: a retained ack would be
/// redelivered on every reconnect, long after anyone was waiting for it.
fn ack(client: &mut EspMqttClient<'_>, id: &str, payload: &[u8]) -> Result<(), EspError> {
    let topic = format!("{}/{}", config::ACK_TOPIC, id);
    client.publish(&topic, QoS::AtLeastOnce, false, payload)?;

    Ok(())
}
