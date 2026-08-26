//! The broker connection.
//!
//! The important rule lives here: the ESP-IDF MQTT task is BLOCKED for as long
//! as our callback runs. Calling the client from inside it, or writing to the
//! UART from inside it, deadlocks -- both need that task to make progress.
//!
//! So the callback does one thing: hand the event to the main thread over a
//! channel. Everything real happens there.

use std::sync::mpsc::{self, Receiver};

use esp_idf_svc::mqtt::client::{
    EspMqttClient, EventPayload, LwtConfiguration, MqttClientConfiguration, QoS,
};
use esp_idf_svc::sys::EspError;

use crate::config;

/// What the callback sends to the main thread.
pub enum Event {
    /// (Re)connected. Subscriptions do not survive a reconnect, so the main
    /// thread has to resubscribe every time this arrives, not just once.
    Connected,
    /// A finished job: raw ESC/POS, ready for the printer.
    Job(Vec<u8>),
}

/// Connects to the broker and returns the client plus the event stream.
///
/// The client is returned so the main thread can subscribe and publish; the
/// receiver is how it learns anything happened.
pub fn connect() -> Result<(EspMqttClient<'static>, Receiver<Event>), EspError> {
    let options = MqttClientConfiguration {
        client_id: Some(config::CLIENT_ID),
        username: Some(config::MQTT_USER),
        password: Some(config::MQTT_PASS),

        // Clean session: the broker must NOT hold jobs for us while we are
        // unplugged, or reconnecting would print hours of backlog at once.
        disable_clean_session: false,

        lwt: Some(LwtConfiguration {
            topic: config::STATE_TOPIC,
            payload: config::OFFLINE,
            qos: QoS::AtLeastOnce,
            retain: true,
        }),

        ..Default::default()
    };

    let (tx, rx) = mpsc::channel();

    let client = EspMqttClient::new_cb(config::MQTT_URL, &options, move |event| {
        match event.payload() {
            EventPayload::Connected(_) => {
                let _ = tx.send(Event::Connected);
            }

            // Only one topic is subscribed, so anything arriving here is a job.
            //
            // Chunk reassembly is deliberately absent: the receive buffer is
            // 4KB and the gateway caps documents at 255 characters, so a
            // payload cannot exceed roughly 600 bytes. Raise that limit past
            // ~3500 and long receipts will silently arrive in pieces.
            EventPayload::Received { data, .. } => {
                let _ = tx.send(Event::Job(data.to_vec()));
            }

            EventPayload::Disconnected => log::warn!("disconnected from broker"),

            EventPayload::Error(e) => log::error!("mqtt error: {e}"),

            _ => {}
        }
    })?;

    Ok((client, rx))
}
