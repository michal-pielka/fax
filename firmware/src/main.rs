//! Fax printer firmware: subscribe to a topic, write what arrives straight out
//! of a UART. The server renders the ESC/POS, so this is a byte pump.

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

    // Before MQTT: TLS checks certificate dates and this board has no clock.
    // Kept alive so it stays corrected.
    let _sntp = time::sync_blocking(std::time::Duration::from_secs(15))?;

    let (mut client, events) = mqtt::connect()?;

    // What we last told the broker. True to start, so a printer that never
    // answers behaves exactly as it did before any of this existed.
    let mut paper = true;

    // All client and printer work happens here; doing it in the callback
    // deadlocks (see mqtt.rs). The timeout is where the paper poll lives.
    loop {
        match events.recv_timeout(config::PAPER_POLL) {
            Ok(Event::Connected) => {
                log::info!("connected to broker");

                client.subscribe(config::JOB_TOPIC, QoS::AtLeastOnce)?;

                // Measured before announcing: a guess corrected a moment later
                // is how the gateway rejects a job that would have printed.
                paper = printer::has_paper(&uart).unwrap_or_else(|| {
                    log::warn!("printer will not report paper -- assuming loaded");
                    true
                });
                client.publish(config::STATE_TOPIC, QoS::AtLeastOnce, true, state(paper))?;

                log::info!("subscribed, announced online, paper {paper}");
            }

            Ok(Event::Job { id, payload }) => {
                // Refuse rather than blast bytes at a dead engine: the
                // gateway's view of the paper can be seconds stale.
                if !paper {
                    log::warn!("job {id} refused, no paper");
                    ack(&mut client, &id, config::ACK_NO_PAPER)?;
                    continue;
                }

                log::info!("printing {} bytes for {id}", payload.len());
                printer::write(&uart, &payload)?;

                // One reply, two answers: it finished, and this is the paper
                // afterwards.
                match printer::wait_done(&uart, config::PRINT_TIMEOUT) {
                    Some(now) => {
                        log::info!("printed {id}");
                        ack(&mut client, &id, config::ACK_OK)?;
                        set_paper(&mut client, now, &mut paper)?;
                    }

                    None => {
                        // It may well have printed; we cannot say so.
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

/// Publish a paper *change* only: a retained publish every five seconds is
/// noise the broker keeps forever. The connect arm publishes unconditionally.
fn set_paper(client: &mut EspMqttClient<'_>, now: bool, known: &mut bool) -> Result<(), EspError> {
    if now == *known {
        return Ok(());
    }

    *known = now;
    log::info!("paper {}", if now { "loaded" } else { "OUT" });
    client.publish(config::STATE_TOPIC, QoS::AtLeastOnce, true, state(now))?;

    Ok(())
}
