//! Fax printer firmware: subscribe to a topic, hand what arrives to the
//! printer. The server renders the ESC/POS, so this is a byte pump.

mod config;
mod mqtt;
mod printer;
mod time;
mod usb;
mod wifi;

use std::time::Instant;

use esp_idf_svc::eventloop::EspSystemEventLoop;
use esp_idf_svc::hal::peripherals::Peripherals;
use esp_idf_svc::hal::reset::restart;
use esp_idf_svc::mqtt::client::{EspMqttClient, QoS};
use esp_idf_svc::nvs::EspDefaultNvsPartition;
use esp_idf_svc::sys::EspError;

use mqtt::Event;
use printer::{Printer, Transport};

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
    let printer = Printer::new(usb::open()?);

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
                // Without the subscription the device would look online and
                // never print. A reboot is the one reliable way back.
                if let Err(e) = on_connect(&mut client) {
                    log::error!("cannot subscribe after connecting: {e}; restarting");
                    restart();
                }
            }

            // A failed job must not end the loop: returning from main leaves
            // the board alive but deaf until someone power-cycles it.
            Event::Job { id, payload } => {
                if let Err(e) = on_job(&mut client, &printer, &id, &payload) {
                    log::error!("job {id} failed: {e}");
                }
            }
        }
    }

    log::error!("mqtt client gone; restarting");
    restart();
}

fn on_connect(client: &mut EspMqttClient<'_>) -> Result<(), EspError> {
    log::info!("connected to broker");

    client.subscribe(config::JOB_TOPIC, QoS::AtLeastOnce)?;
    client.publish(config::STATE_TOPIC, QoS::AtLeastOnce, true, config::ONLINE)?;

    log::info!("subscribed, announced online");

    Ok(())
}

fn on_job<T: Transport>(
    client: &mut EspMqttClient<'_>,
    printer: &Printer<T>,
    id: &str,
    payload: &[u8],
) -> Result<(), EspError> {
    // One clock for the whole job, so a missing or stalled printer costs at
    // most JOB_DEADLINE however many calls it would otherwise have stalled.
    let deadline = Instant::now() + config::JOB_DEADLINE;

    if let Err(e) = printer.wait_attached(config::ATTACH_WAIT) {
        log::warn!("job {id} not printed, no printer: {e}");
        return ack(client, id, config::ACK_NO_CONFIRM);
    }

    // Measured now, not cached: a browser is waiting on this ack, and a stale
    // answer either refuses a job that would have printed or blasts bytes at
    // an empty slot. Silence is not "no paper" -- see has_paper -- so an
    // unanswered query prints.
    if printer.has_paper() == Some(false) {
        log::warn!("job {id} refused, no paper");
        return ack(client, id, config::ACK_NO_PAPER);
    }

    log::info!("printing {} bytes for {id}", payload.len());

    // Over USB the write returns only once the printer has taken every byte,
    // which -- since it prints as its buffer drains -- means the job is on
    // paper. So a completed write is the confirmation; an error is a job that
    // never reached paper: the printer off, unplugged, or stalled.
    match printer.write(payload, deadline) {
        Ok(()) => {
            log::info!("printed {id}");
            ack(client, id, config::ACK_OK)
        }
        Err(e) => {
            log::warn!("could not print {id}: {e}");
            ack(client, id, config::ACK_NO_CONFIRM)
        }
    }
}

/// Publish the ack for one job. Not retained: a retained ack would be
/// redelivered on every reconnect, long after anyone was waiting for it.
fn ack(client: &mut EspMqttClient<'_>, id: &str, payload: &[u8]) -> Result<(), EspError> {
    let topic = format!("{}/{}", config::ACK_TOPIC, id);
    client.publish(&topic, QoS::AtLeastOnce, false, payload)?;

    Ok(())
}
