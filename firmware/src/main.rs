//! Fax printer firmware.
//!
//! Subscribes to an MQTT topic and writes whatever arrives straight out of a
//! UART into the thermal printer. The payload is already finished ESC/POS --
//! the server renders it -- so there is no printer command knowledge here.
//! This is a byte pump.

mod config;
mod mqtt;
mod printer;
mod time;
mod wifi;

use std::sync::mpsc::RecvTimeoutError;

use esp_idf_svc::eventloop::EspSystemEventLoop;
use esp_idf_svc::hal::peripherals::Peripherals;
use esp_idf_svc::hal::uart::UartDriver;
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

    // Before MQTT, not after: TLS checks certificate dates and this board has
    // no battery-backed clock, so an unsynced device fails every handshake.
    // Kept alive so the clock keeps being corrected.
    let _sntp = time::sync_blocking(std::time::Duration::from_secs(15))?;

    let (mut client, events) = mqtt::connect()?;

    // What we last told the broker. True to start, so a printer that never
    // answers behaves exactly as it did before any of this existed.
    let mut paper = true;

    // All client and printer work happens on this thread. Doing any of it in
    // the MQTT callback deadlocks -- see mqtt.rs.
    //
    // recv_timeout rather than a plain receive: the timeout is the only thing
    // that runs on an idle device, and it is where the paper poll lives.
    loop {
        match events.recv_timeout(config::PAPER_POLL) {
            Ok(Event::Connected) => {
                log::info!("connected to broker");

                client.subscribe(config::JOB_TOPIC, QoS::AtLeastOnce)?;

                // Measured before announcing: publishing a guess and correcting
                // it a moment later is how the gateway ends up rejecting a job
                // that would have printed fine.
                paper = printer::has_paper(&uart).unwrap_or_else(|| {
                    log::warn!("printer will not report paper -- assuming loaded");
                    true
                });
                client.publish(config::STATE_TOPIC, QoS::AtLeastOnce, true, state(paper))?;

                log::info!("subscribed, announced online, paper {paper}");
            }

            Ok(Event::Job { id, payload }) => {
                // Refuse rather than blast bytes at a dead engine. The gateway
                // should have caught this, but its view of the paper is a
                // cached value that can be seconds stale -- ours is not.
                if !paper {
                    log::warn!("job {id} refused, no paper");
                    ack(&mut client, &id, config::ACK_NO_PAPER)?;
                    continue;
                }

                log::info!("printing {} bytes for {id}", payload.len());
                printer::write(&uart, &payload)?;

                // Blocks until the printer has worked through the job. One
                // reply, two answers: it finished, and this is the paper
                // afterwards.
                match printer::wait_done(&uart, config::PRINT_TIMEOUT) {
                    Some(now) => {
                        log::info!("printed {id}");
                        ack(&mut client, &id, config::ACK_OK)?;
                        set_paper(&mut client, now, &mut paper)?;
                    }

                    None => {
                        // Silence here means the job may well have printed --
                        // we simply cannot say so. Claiming success would be
                        // the lie this whole exchange exists to remove.
                        log::warn!("no confirmation for {id}");
                        ack(&mut client, &id, config::ACK_NO_CONFIRM)?;
                    }
                }
            }

            Err(RecvTimeoutError::Timeout) => poll_paper(&mut client, &uart, &mut paper)?,

            // The callback's sender is gone, so the client is gone with it.
            Err(RecvTimeoutError::Disconnected) => break,
        }
    }

    Ok(())
}

fn state(paper: bool) -> &'static [u8] {
    if paper {
        config::ONLINE
    } else {
        config::NO_PAPER
    }
}

/// Publish the ack for one job. Not retained: a retained ack would be
/// redelivered on every reconnect, long after anyone was waiting for it.
fn ack(client: &mut EspMqttClient<'_>, id: &str, payload: &[u8]) -> Result<(), EspError> {
    let topic = format!("{}/{}", config::ACK_TOPIC, id);
    client.publish(&topic, QoS::AtLeastOnce, false, payload)?;

    Ok(())
}

/// Ask about the paper and tell the broker. Used on the idle tick; after a job
/// the answer arrives with the completion instead, so no second question.
fn poll_paper(
    client: &mut EspMqttClient<'_>,
    uart: &UartDriver,
    known: &mut bool,
) -> Result<(), EspError> {
    // Silence is not "no paper" -- see has_paper -- so keep the last answer.
    let Some(now) = printer::has_paper(uart) else {
        return Ok(());
    };

    set_paper(client, now, known)
}

/// Publish a paper *change*, so the two things that measure it cannot
/// disagree. Only on a change: a retained publish every five seconds is noise
/// the broker keeps forever. The connect arm publishes unconditionally instead,
/// because a fresh session needs the retained message refreshed either way.
fn set_paper(client: &mut EspMqttClient<'_>, now: bool, known: &mut bool) -> Result<(), EspError> {
    if now == *known {
        return Ok(());
    }

    *known = now;
    log::info!("paper {}", if now { "loaded" } else { "OUT" });
    client.publish(config::STATE_TOPIC, QoS::AtLeastOnce, true, state(now))?;

    Ok(())
}
