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

            Ok(Event::Job(payload)) => {
                log::info!("printing {} bytes", payload.len());
                printer::write(&uart, &payload)?;
                log::info!("printed");

                // Straight after a job is when the roll is likeliest to have
                // just run out.
                report_paper(&mut client, &uart, &mut paper)?;
            }

            Err(RecvTimeoutError::Timeout) => report_paper(&mut client, &uart, &mut paper)?,

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

/// Ask about the paper and tell the broker, but only when the answer changed.
/// A retained publish every five seconds is noise the broker keeps forever.
fn report_paper(
    client: &mut EspMqttClient<'_>,
    uart: &UartDriver,
    known: &mut bool,
) -> Result<(), EspError> {
    // Silence is not "no paper" -- see has_paper -- so keep the last answer.
    let Some(now) = printer::has_paper(uart) else {
        return Ok(());
    };

    if now == *known {
        return Ok(());
    }

    *known = now;
    log::info!("paper {}", if now { "loaded" } else { "OUT" });
    client.publish(config::STATE_TOPIC, QoS::AtLeastOnce, true, state(now))?;

    Ok(())
}
